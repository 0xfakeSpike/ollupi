package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/gateway"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/router"
	"github.com/coder/websocket"
)

const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"bench","version":"1"}}}`

// conns holds idle client connections on a shard's gateway, one user each, straight or through a router, and at each
// step reports what they cost the shard and the router: resident memory, goroutines, file descriptors, and the CPU the
// pings take while nothing else happens. The shard and the router run as processes of their own, so the clients'
// side of the connections is not counted. With -storm it then kills the shard, starts it again on the same database,
// and times the clients' return, each reconnecting as the README tells clients to.
func conns(args []string) {
	fs := flag.NewFlagSet("conns", flag.ExitOnError)
	steps := fs.String("n", "1000,2000,4000", "connections held at each step, one user each")
	viaRouter := fs.Bool("router", false, "clients reach the shard through a router")
	idle := fs.Duration("idle", 35*time.Second, "how long each step holds its connections idle before measuring; longer than the gateway's ping interval, so pings are counted")
	wait := fs.Duration("wait", 10*time.Minute, "how long a step may take to connect before the experiment stops")
	storm := fs.Bool("storm", false, "after the last step, kill the shard, start it again and time the clients' return")
	sources := fs.String("sources", "127.0.0.1", "local addresses clients dial from, in turn; each adds its own ephemeral ports")
	fs.Parse(args)
	var ns []int
	for _, s := range strings.Split(*steps, ",") {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 || len(ns) > 0 && n <= ns[len(ns)-1] {
			log.Fatalf("-n %s: steps must be positive and growing", *steps)
		}
		ns = append(ns, n)
	}

	dir, err := os.MkdirTemp("", "ollupi-bench-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	shardAddr := freeAddress()
	shard := startChild("connsshard", "-dir", dir, "-addr", shardAddr)
	defer func() { shard.stop() }()
	target, via := shardAddr, "shard"
	var router *child
	if *viaRouter {
		target, via = freeAddress(), "router"
		router = startChild("connsrouter", "-addr", target, "-shard", "http://"+shardAddr)
		defer router.stop()
	}
	f := &fleet{url: "ws://" + target}
	for _, s := range strings.Split(*sources, ",") {
		ip := net.ParseIP(s)
		if ip == nil {
			log.Fatalf("-sources: %q is not an address", s)
		}
		dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: ip}, Timeout: 10 * time.Second}
		f.clients = append(f.clients, &http.Client{Transport: &http.Transport{DialContext: dialer.DialContext}})
	}

	baseShard := shard.stats()
	var baseRouter map[string]float64
	if router != nil {
		baseRouter = router.stats()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	held := 0
	for _, n := range ns {
		at, refused, failed := time.Now(), f.refused.Load(), f.failed.Load()
		for ; held < n; held++ {
			go f.hold(ctx, fmt.Sprintf("user%06d", held), f.clients[held%len(f.clients)])
		}
		connected := until(*wait, func() bool { return f.ready.Load() >= int64(n) })
		line := fmt.Sprintf("step n=%d via=%s connected=%d connect_s=%.1f refused=%d failed=%d", n, via, f.ready.Load(),
			time.Since(at).Seconds(), f.refused.Load()-refused, f.failed.Load()-failed)
		if !connected {
			fmt.Println(line + " stopped=not_all_connected")
			if last := f.lastErr.Load(); last != nil {
				log.Printf("conns: the last connection that failed: %s", *last)
			}
			return
		}
		shardCPU := shard.stats()["cpu_ms"]
		var routerCPU float64
		if router != nil {
			routerCPU = router.stats()["cpu_ms"]
		}
		time.Sleep(*idle)
		line += costs("shard", baseShard, shard.stats(), shardCPU, n, *idle)
		if router != nil {
			line += costs("router", baseRouter, router.stats(), routerCPU, n, *idle)
		}
		fmt.Println(line)
	}
	if !*storm {
		return
	}

	n := ns[len(ns)-1]
	refused, failed := f.refused.Load(), f.failed.Load()
	f.mu.Lock()
	f.back = nil
	f.mu.Unlock()
	killedAt := time.Now()
	shard.stop()
	shard = startChild("connsshard", "-dir", dir, "-addr", shardAddr)
	restart := time.Since(killedAt)
	back := until(*wait, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.back) >= n
	})
	allBack := time.Since(killedAt)
	f.mu.Lock()
	took := slices.Clone(f.back)
	f.mu.Unlock()
	slices.Sort(took)
	fmt.Printf("storm n=%d via=%s restart_ms=%d back=%d all_back=%t all_back_s=%.1f p50_s=%.1f p99_s=%.1f refused=%d failed=%d\n",
		n, via, restart.Milliseconds(), len(took), back, allBack.Seconds(), pct(took, .5).Seconds(), pct(took, .99).Seconds(),
		f.refused.Load()-refused, f.failed.Load()-failed)
}

// costs reads what n connections add to a process over its numbers before any, and the CPU it took per second idle.
func costs(name string, base, now map[string]float64, cpuBefore float64, n int, idle time.Duration) string {
	perConn := func(k string) float64 { return (now[k] - base[k]) / float64(n) }
	var b strings.Builder
	for _, f := range []struct {
		key    string
		format string
		value  float64
	}{
		{"rss_mb", "%.1f", now["rss_mb"]},
		{"heap_mb", "%.1f", now["heap_mb"]},
		{"kb_per_conn", "%.1f", perConn("rss_mb") * 1024},
		{"heap_kb_per_conn", "%.1f", perConn("heap_mb") * 1024},
		{"goroutines", "%.0f", now["goroutines"]},
		{"goroutines_per_conn", "%.2f", perConn("goroutines")},
		{"fds", "%.0f", now["fds"]},
		{"fds_per_conn", "%.2f", perConn("fds")},
		{"idle_cpu_ms_per_s", "%.1f", (now["cpu_ms"] - cpuBefore) / idle.Seconds()},
	} {
		fmt.Fprintf(&b, " %s_%s="+f.format, name, f.key, f.value)
	}
	return b.String()
}

// until says whether done came true within d.
func until(d time.Duration, done func() bool) bool {
	for deadline := time.Now().Add(d); !done(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			return false
		}
	}
	return true
}

// fleet is the clients: each holds one connection, reading it so that it answers the gateway's pings, and connects
// again whenever it drops.
type fleet struct {
	url     string
	clients []*http.Client
	ready   atomic.Int64
	// refused counts the 503s that said when to come back; failed counts every other try that did not connect.
	refused, failed atomic.Int64
	lastErr         atomic.Pointer[string]

	mu sync.Mutex
	// back is how long each connection that dropped took to be back.
	back []time.Duration
}

func (f *fleet) hold(ctx context.Context, user string, client *http.Client) {
	var lostAt time.Time
	for {
		ws := f.connect(ctx, user, client)
		if ws == nil {
			return
		}
		if !lostAt.IsZero() {
			f.mu.Lock()
			f.back = append(f.back, time.Since(lostAt))
			f.mu.Unlock()
		}
		f.ready.Add(1)
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				break
			}
		}
		ws.CloseNow()
		f.ready.Add(-1)
		lostAt = time.Now()
	}
}

// connect tries until a connection is up and initialized, or ctx ends. It waits as long as a refusal's Retry-After
// says, and otherwise backs off exponentially, from 100 ms up to 5 s, at a random point under each bound.
func (f *fleet) connect(ctx context.Context, user string, client *http.Client) *websocket.Conn {
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		ws, retryAfter, err := f.dial(ctx, user, client)
		if err == nil {
			return ws
		}
		wait := rand.N(backoff)
		backoff = min(2*backoff, 5*time.Second)
		if retryAfter > 0 {
			f.refused.Add(1)
			wait = retryAfter
		} else if ctx.Err() == nil {
			f.failed.Add(1)
			msg := err.Error()
			f.lastErr.Store(&msg)
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	return nil
}

// dial connects as user and says hello. A refusal that says when to come back gives how long to wait.
func (f *fleet) dial(ctx context.Context, user string, client *http.Client) (*websocket.Conn, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ws, res, err := websocket.Dial(ctx, f.url, &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": {"Bearer " + user}}})
	if err != nil {
		if res != nil && res.StatusCode == http.StatusServiceUnavailable {
			if seconds, convErr := strconv.Atoi(res.Header.Get("Retry-After")); convErr == nil && seconds > 0 {
				return nil, time.Duration(seconds) * time.Second, err
			}
		}
		return nil, 0, err
	}
	if err := ws.Write(ctx, websocket.MessageText, []byte(initialize)); err != nil {
		ws.CloseNow()
		return nil, 0, err
	}
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			ws.CloseNow()
			return nil, 0, err
		}
		var m struct {
			ID    json.RawMessage `json:"id"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(data, &m) == nil && string(m.ID) == "1" {
			if m.Error != nil {
				ws.CloseNow()
				return nil, 0, fmt.Errorf("initialize: %s", m.Error)
			}
			return ws, 0, nil
		}
	}
}

// freeAddress finds a loopback address with a port nothing listens on, for a process to listen on, again after a
// restart.
func freeAddress() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// child is a process the experiment starts and asks for its numbers.
type child struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	lines <-chan string
}

// startChild runs this executable as the experiment named and waits until it is ready.
func startChild(args ...string) *child {
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		log.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	lines := make(chan string, 4)
	go func() {
		defer close(lines)
		for s := bufio.NewScanner(out); s.Scan(); {
			lines <- s.Text()
		}
	}()
	c := &child{cmd: cmd, in: in, lines: lines}
	c.await("ready")
	return c
}

func (c *child) await(prefix string) string {
	for line := range c.lines {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	log.Fatalf("%s ended before it said %q", c.cmd.Args[1], prefix)
	return ""
}

// stats asks the process for its numbers, as answerStats gives them.
func (c *child) stats() map[string]float64 {
	if _, err := fmt.Fprintln(c.in, "stats"); err != nil {
		log.Fatalf("%s: %v", c.cmd.Args[1], err)
	}
	stats := map[string]float64{}
	for _, field := range strings.Fields(c.await("stats "))[1:] {
		if k, v, ok := strings.Cut(field, "="); ok {
			stats[k], _ = strconv.ParseFloat(v, 64)
		}
	}
	return stats
}

func (c *child) stop() {
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()
}

// answerStats says the process is ready, then answers each "stats" on stdin with its numbers after a collection, until
// stdin closes.
func answerStats() {
	fmt.Println("ready")
	for s := bufio.NewScanner(os.Stdin); s.Scan(); {
		if s.Text() != "stats" {
			continue
		}
		rss, heap := memory()
		// Each entry is one of the process's open descriptors, the one reading them among them.
		fds, err := os.ReadDir("/dev/fd")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("stats rss_mb=%.1f heap_mb=%.1f goroutines=%d fds=%d cpu_ms=%d\n", rss, heap, runtime.NumGoroutine(),
			len(fds), cpu().Milliseconds())
	}
}

// benchUser takes each client's token to be its user.
func benchUser(token string) (string, bool) {
	return token, strings.HasPrefix(token, "user")
}

// quiet keeps a process the experiment starts from logging each connection that comes and goes, by the thousand.
func quiet() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

// connsShard is one shard's gateway and agent on a database, as conns starts it.
func connsShard(args []string) {
	fs := flag.NewFlagSet("connsshard", flag.ExitOnError)
	dir := fs.String("dir", "", "the shard's home")
	addr := fs.String("addr", "", "address to listen on")
	fs.Parse(args)
	quiet()
	db, err := repo.Open(*dir)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	var server *gateway.Server
	ag := orchestrator.New(orchestrator.Config{
		Name: "bench", Models: []llm.Model{{Name: "fake", Connection: "fake"}},
		Connections: map[string]llm.Client{"fake": &model{}},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		DB:          db, OnEvent: func(e orchestrator.Event) { server.Deliver(e) },
	})
	server, err = gateway.NewServer(ag, gateway.Options{DB: db, Authenticate: benchUser})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ag.Start(ctx); err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		if err := server.Serve(ctx, ln); err != nil {
			log.Fatal(err)
		}
	}()
	answerStats()
}

// oneShard places every user on the one shard at its address.
type oneShard string

func (oneShard) Assign(context.Context, string) (string, error)    { return "s", nil }
func (s oneShard) Address(context.Context, string) (string, error) { return string(s), nil }

// connsRouter is a router in front of one shard, as conns starts it.
func connsRouter(args []string) {
	fs := flag.NewFlagSet("connsrouter", flag.ExitOnError)
	addr := fs.String("addr", "", "address to listen on")
	shard := fs.String("shard", "", "the shard's URL")
	fs.Parse(args)
	quiet()
	locator, err := router.NewLocator(benchUser, oneShard(*shard))
	if err != nil {
		log.Fatal(err)
	}
	front := router.NewRouter(locator)
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		srv := &http.Server{Handler: front, ReadHeaderTimeout: 5 * time.Second}
		if err := srv.Serve(ln); err != nil {
			log.Fatal(err)
		}
	}()
	answerStats()
}
