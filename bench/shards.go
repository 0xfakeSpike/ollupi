package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

func shards(args []string) {
	fs := flag.NewFlagSet("shards", flag.ExitOnError)
	steps := fs.String("n", "1,2,4", "shard counts, one step each")
	direct := fs.Bool("direct", false, "first run one shard that clients reach without the router")
	c := fs.Int("c", 4096, "conversations running at once, spread over the shards")
	perUser := fs.Int("per-user", 64, "conversations per user; each user has one connection")
	turns := fs.Int("turns", 3, "turns each conversation runs, one after another")
	latency := fs.Duration("latency", 5*time.Second, "model latency")
	jitter := fs.Duration("jitter", 2*time.Second, "model latency varies by up to this much either way")
	toolLatency := fs.Duration("tool", time.Second, "tool latency")
	rounds := fs.Int("rounds", 2, "tool calls in each turn, each followed by a model call")
	fs.Parse(args)

	type step struct {
		shards int
		router bool
	}
	var plan []step
	if *direct {
		plan = append(plan, step{1, false})
	}
	for _, s := range strings.Split(*steps, ",") {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			log.Fatalf("-n %s: shard counts must be positive", *steps)
		}
		plan = append(plan, step{n, true})
	}
	alone := time.Duration(*rounds+1)*(*latency) + time.Duration(*rounds)*(*toolLatency)
	for _, s := range plan {
		load := shardLoad{sessions: *c, perUser: *perUser, turns: *turns, latency: *latency, jitter: *jitter, tool: *toolLatency, rounds: *rounds}
		fmt.Println(load.run(s.shards, s.router, alone))
	}
}

type shardLoad struct {
	sessions, perUser, turns, rounds int
	latency, jitter, tool            time.Duration
}

func (l shardLoad) run(n int, viaRouter bool, alone time.Duration) string {
	users := (l.sessions + l.perUser - 1) / l.perUser
	perShard := (users + n - 1) / n * l.perUser
	served, providerURL := spawn("provider", "-latency", l.latency.String(), "-jitter", l.jitter.String(),
		"-rounds", strconv.Itoa(l.rounds), "-limit", strconv.Itoa(l.sessions))
	defer served.Process.Kill()

	var nodes []*child
	var urls []string
	for range n {
		dir := tempHome()
		defer os.RemoveAll(dir)
		addr := freeAddress()
		nodes = append(nodes, startChild("serve", "-dir", dir, "-url", providerURL, "-addr", addr, "-tool", l.tool.String(),
			"-calls", strconv.Itoa(perShard), "-slots", strconv.Itoa(perShard)))
		urls = append(urls, "http://"+addr)
	}
	defer func() {
		for _, node := range nodes {
			node.stop()
		}
	}()
	target, via := strings.TrimPrefix(urls[0], "http://"), "shard"
	var front *child
	if viaRouter {
		target, via = freeAddress(), "router"
		front = startChild("connsrouter", "-addr", target, "-shard", strings.Join(urls, ","))
		defer front.stop()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fleet{url: "ws://" + target}
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: users}}
	conns := make([]*rpcConn, users)
	var wg sync.WaitGroup
	for u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ws := f.connect(ctx, fmt.Sprintf("user%06d", u), client)
			conns[u] = newRPCConn(ctx, ws)
		}()
	}
	wg.Wait()
	threads := make([]struct {
		conn *rpcConn
		id   string
	}, l.sessions)
	for i := range threads {
		conn := conns[i/l.perUser]
		var res struct {
			Thread struct{ ID string } `json:"thread"`
		}
		if err := conn.call(ctx, "thread/start", map[string]string{"title": "bench"}, &res); err != nil {
			log.Fatalf("thread/start: %v", err)
		}
		threads[i].conn, threads[i].id = conn, res.Thread.ID
	}

	before := map[*child]map[string]float64{}
	for _, node := range append(slices.Clone(nodes), front) {
		if node != nil {
			before[node] = node.stats()
		}
	}
	requestsAt := providerRequests(providerURL)
	providerCPU, clientCPU, startAt := processCPU(served.Process.Pid), cpu(), time.Now()
	var mu sync.Mutex
	var took []time.Duration
	var failed atomic.Int64
	for _, th := range threads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range l.turns {
				at := time.Now()
				status, err := th.conn.turn(ctx, th.id, "work")
				if err != nil {
					log.Fatalf("turn: %v", err)
				}
				completed := status == "completed"
				if !completed {
					failed.Add(1)
				}
				mu.Lock()
				took = append(took, time.Since(at))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	total := time.Since(startAt)
	clientCPU = cpu() - clientCPU
	providerCPU = processCPU(served.Process.Pid) - providerCPU
	requests := providerRequests(providerURL) - requestsAt
	slices.Sort(took)

	cores := func(node *child) (float64, float64) {
		now := node.stats()
		return (now["cpu_ms"] - before[node]["cpu_ms"]) / 1000 / total.Seconds(), now["rss_mb"]
	}
	var shardCores, shardRSS []string
	var coresSum float64
	for _, node := range nodes {
		used, rss := cores(node)
		coresSum += used
		shardCores = append(shardCores, fmt.Sprintf("%.2f", used))
		shardRSS = append(shardRSS, fmt.Sprintf("%.0f", rss))
	}
	routerPart := ""
	allCores := coresSum + clientCPU.Seconds()/total.Seconds() + providerCPU.Seconds()/total.Seconds()
	if front != nil {
		used, rss := cores(front)
		allCores += used
		routerPart = fmt.Sprintf(" router_cpu_cores=%.2f router_rss_mb=%.0f", used, rss)
	}
	return fmt.Sprintf("step shards=%d via=%s sessions=%d users=%d turns=%d total_s=%.1f alone_s=%.1f p50_s=%.1f p95_s=%.1f p99_s=%.1f "+
		"turns_per_s=%.1f model_calls_per_s=%.0f failed_turns=%d shard_cpu_cores=[%s] shards_cpu_cores=%.2f shard_rss_mb=[%s]%s client_cpu_cores=%.2f "+
		"provider_cpu_cores=%.2f all_cpu_cores=%.2f",
		n, via, l.sessions, users, len(took), total.Seconds(), alone.Seconds(), pct(took, .5).Seconds(), pct(took, .95).Seconds(), pct(took, .99).Seconds(),
		float64(len(took))/total.Seconds(), float64(requests)/total.Seconds(), failed.Load(), strings.Join(shardCores, " "), coresSum,
		strings.Join(shardRSS, " "), routerPart, clientCPU.Seconds()/total.Seconds(), providerCPU.Seconds()/total.Seconds(), allCores)
}

func processCPU(pid int) time.Duration {
	out, err := exec.Command("ps", "-o", "time=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		log.Fatalf("the CPU time of process %d: %v", pid, err)
	}
	var total float64
	for _, part := range strings.Split(strings.TrimSpace(string(out)), ":") {
		v, err := strconv.ParseFloat(part, 64)
		if err != nil {
			log.Fatalf("the CPU time of process %d: %q", pid, out)
		}
		total = total*60 + v
	}
	return time.Duration(total * float64(time.Second))
}

func providerRequests(url string) int64 {
	resp, err := http.Get(url + "/stats")
	if err != nil {
		log.Fatalf("the provider's stats: %v", err)
	}
	defer resp.Body.Close()
	var peak, refused, requests int64
	if _, err := fmt.Fscan(resp.Body, &peak, &refused, &requests); err != nil {
		log.Fatalf("the provider's stats: %v", err)
	}
	return requests
}

type rpcConn struct {
	ws   *websocket.Conn
	next atomic.Int64
	done chan struct{}

	mu      sync.Mutex
	replies map[int64]chan rpcReply
	ended   map[string]string
	waiting map[string]chan string
	err     error
}

type rpcReply struct {
	result json.RawMessage
	err    json.RawMessage
}

func newRPCConn(ctx context.Context, ws *websocket.Conn) *rpcConn {
	c := &rpcConn{ws: ws, done: make(chan struct{}), replies: map[int64]chan rpcReply{}, ended: map[string]string{}, waiting: map[string]chan string{}}
	c.next.Store(1)
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","method":"initialized","params":{}}`)); err != nil {
		log.Fatalf("initialized: %v", err)
	}
	go c.read(ctx)
	return c
}

func (c *rpcConn) read(ctx context.Context) {
	defer close(c.done)
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			c.mu.Lock()
			c.err = err
			c.mu.Unlock()
			return
		}
		var m struct {
			ID     *int64          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
			Method string          `json:"method"`
			Params struct {
				Turn struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"turn"`
			} `json:"params"`
		}
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		turnEnded := m.Method == "turn/changed" && m.Params.Turn.Status != "queued" && m.Params.Turn.Status != "inProgress"
		c.mu.Lock()
		switch {
		case m.ID != nil:
			if reply, ok := c.replies[*m.ID]; ok {
				reply <- rpcReply{m.Result, m.Error}
				delete(c.replies, *m.ID)
			}
		case turnEnded:
			if wait, ok := c.waiting[m.Params.Turn.ID]; ok {
				wait <- m.Params.Turn.Status
				delete(c.waiting, m.Params.Turn.ID)
			} else {
				c.ended[m.Params.Turn.ID] = m.Params.Turn.Status
			}
		}
		c.mu.Unlock()
	}
}

func (c *rpcConn) call(ctx context.Context, method string, params, result any) error {
	id := c.next.Add(1)
	reply := make(chan rpcReply, 1)
	c.mu.Lock()
	c.replies[id] = reply
	c.mu.Unlock()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	if err := c.ws.Write(ctx, websocket.MessageText, raw); err != nil {
		return err
	}
	select {
	case r := <-reply:
		if r.err != nil {
			return fmt.Errorf("%s: %s", method, r.err)
		}
		return json.Unmarshal(r.result, result)
	case <-c.done:
		return fmt.Errorf("%s: the connection closed: %w", method, c.err)
	}
}

func (c *rpcConn) turn(ctx context.Context, thread, text string) (string, error) {
	var res struct {
		Turn struct{ ID string } `json:"turn"`
	}
	input := []map[string]string{{"type": "text", "text": text}}
	if err := c.call(ctx, "turn/start", map[string]any{"threadId": thread, "input": input}, &res); err != nil {
		return "", err
	}
	wait := make(chan string, 1)
	c.mu.Lock()
	status, ended := c.ended[res.Turn.ID]
	if ended {
		delete(c.ended, res.Turn.ID)
	} else {
		c.waiting[res.Turn.ID] = wait
	}
	c.mu.Unlock()
	if ended {
		return status, nil
	}
	select {
	case status := <-wait:
		return status, nil
	case <-c.done:
		return "", errors.New("the connection closed before the turn ended")
	}
}
