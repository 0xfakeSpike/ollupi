package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

// steady keeps c conversations busy, each sending its next turn as soon as the last one ends, as c people using
// the service at once would; when the turns start taking longer than they would alone, the process is full.
func steady(args []string) {
	fs := flag.NewFlagSet("steady", flag.ExitOnError)
	c := fs.Int("c", 1024, "conversations running at once")
	turns := fs.Int("turns", 3, "turns each conversation runs, one after another")
	slots := fs.Int("slots", 0, "agent slots (default: c)")
	calls := fs.Int("calls", 0, "model calls the connection and the provider allow in flight (default: c)")
	latency := fs.Duration("latency", 5*time.Second, "model latency")
	jitter := fs.Duration("jitter", 2*time.Second, "model latency varies by up to this much either way")
	sigma := fs.Float64("sigma", 0, "draw model latency log-normally around -latency with this sigma, in place of -jitter")
	others := fs.Int("others", 0, "the most requests other clients of the account hold in flight against the provider's limit")
	fail := fs.Float64("fail", 0, "share of model requests answered with a 500")
	toolLatency := fs.Duration("tool", time.Second, "tool latency")
	rounds := fs.Int("rounds", 2, "tool calls in each turn, each followed by a model call")
	profile := fs.String("cpuprofile", "", "write a CPU profile of the run to this file")
	fs.Parse(args)
	if *slots == 0 {
		*slots = *c
	}
	if *calls == 0 {
		*calls = *c
	}

	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	served := exec.Command(exe, "provider", "-latency", latency.String(), "-jitter", jitter.String(),
		"-sigma", strconv.FormatFloat(*sigma, 'g', -1, 64), "-others", strconv.Itoa(*others), "-fail", strconv.FormatFloat(*fail, 'g', -1, 64),
		"-rounds", strconv.Itoa(*rounds), "-limit", strconv.Itoa(*calls))
	served.Stderr = os.Stderr
	out, err := served.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}
	if err := served.Start(); err != nil {
		log.Fatal(err)
	}
	defer served.Process.Kill()
	url, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	url = strings.TrimSpace(url)
	// As in burst: keep a connection per call in flight, as a real provider's HTTP/2 connection would serve them.
	http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost = *calls
	dir, err := os.MkdirTemp("", "ollupi-bench-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	db, err := repo.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	dbs := watchDB(db)
	tr := &tracker{startedAt: map[string]time.Time{}, endedAt: map[string]time.Time{}, done: map[string]chan struct{}{}}
	ag := agent.New(agent.Config{
		Name: "bench", Models: []llm.Model{{Name: "fake", Connection: "fake"}},
		Connections: map[string]llm.Client{"fake": llm.NewOpenAI(url, "", *calls, map[string]llm.Limits{"fake": {Context: 200_000, Output: 32_000}})},
		Providers:   []tools.Provider{work{latency: *toolLatency}},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		Slots:       *slots, DB: db, OnEvent: tr.on,
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := ag.Start(ctx); err != nil {
		log.Fatal(err)
	}
	threads := make([]string, *c)
	tr.mu.Lock()
	for i := range threads {
		th, err := ag.StartThread(repo.DefaultUser, "", "bench", permission.Settings{})
		if err != nil {
			log.Fatal(err)
		}
		threads[i] = th.ID
		tr.done[th.ID] = make(chan struct{}, 1)
	}
	tr.mu.Unlock()

	if *profile != "" {
		f, err := os.Create(*profile)
		if err != nil {
			log.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal(err)
		}
	}
	var peakRSS, peakHeap atomic.Value
	peakRSS.Store(0.0)
	peakHeap.Store(0.0)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Second):
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if heap := float64(m.HeapInuse) / (1 << 20); heap > peakHeap.Load().(float64) {
					peakHeap.Store(heap)
				}
				if rss := rssMB(); rss > peakRSS.Load().(float64) {
					peakRSS.Store(rss)
				}
			}
		}
	}()
	dbs.start()
	busyCPU, startAt := cpu(), time.Now()
	var mu sync.Mutex
	var ids []string
	var wg sync.WaitGroup
	for _, id := range threads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range *turns {
				turnID := tr.start(ag, id, "work")
				mu.Lock()
				ids = append(ids, turnID)
				mu.Unlock()
				select {
				case <-tr.done[id]:
				case <-time.After(time.Hour):
					log.Fatal("timed out waiting for a turn")
				}
			}
		}()
	}
	wg.Wait()
	total := time.Since(startAt)
	busyCPU = cpu() - busyCPU
	close(stop)
	pprof.StopCPUProfile()

	var peak, refused, requests, failed int64
	if resp, err := http.Get(url + "/stats"); err == nil {
		fmt.Fscan(resp.Body, &peak, &refused, &requests, &failed)
		resp.Body.Close()
	}
	took := tr.took(ids)
	alone := time.Duration(*rounds+1)*(*latency) + time.Duration(*rounds)*(*toolLatency)
	n := len(ids)
	fmt.Printf("c=%d slots=%d calls=%d turns=%d total_s=%.1f turns_per_s=%.1f model_calls_per_s=%.0f alone_s=%.1f "+
		"p50_s=%.1f p95_s=%.1f p99_s=%.1f peak_in_flight=%d refused_429=%d failed_500=%d failed_turns=%d rss_peak_mb=%.0f heap_peak_mb=%.0f db_kb_per_turn=%.1f cpu_cores=%.2f cpu_ms_per_turn=%.1f %s\n",
		*c, *slots, *calls, n, total.Seconds(), float64(n)/total.Seconds(), float64(n*(*rounds+1))/total.Seconds(), alone.Seconds(),
		pct(took, .5).Seconds(), pct(took, .95).Seconds(), pct(took, .99).Seconds(), peak, refused, failed, tr.failed.Load(),
		peakRSS.Load().(float64), peakHeap.Load().(float64), dbMB(dir)*1024/float64(n), busyCPU.Seconds()/total.Seconds(), float64(busyCPU)/float64(time.Millisecond)/float64(n), dbs.report(n))
	cancel()
	ag.Wait()
}

// providerMain serves the fake provider in a process of its own, so what it holds is not counted against the agent;
// it prints its URL, and /stats answers with the peak in flight, the requests refused, all requests and those failed.
func providerMain(args []string) {
	fs := flag.NewFlagSet("provider", flag.ExitOnError)
	latency := fs.Duration("latency", 5*time.Second, "model latency")
	jitter := fs.Duration("jitter", 2*time.Second, "model latency varies by up to this much either way")
	rounds := fs.Int("rounds", 2, "tool calls in each turn, each followed by a model call")
	limit := fs.Int("limit", 1024, "requests allowed in flight before answering 429")
	sigma := fs.Float64("sigma", 0, "draw latency log-normally around -latency with this sigma, in place of -jitter")
	others := fs.Int("others", 0, "the most requests other clients of the account hold in flight against -limit")
	fail := fs.Float64("fail", 0, "share of requests answered with a 500")
	fs.Parse(args)
	p := &provider{latency: *latency, jitter: *jitter, sigma: *sigma, limit: *limit, others: *others, fail: *fail, next: func(_ string, answered int) (string, string) {
		if answered < *rounds {
			return "bench__work", "{}"
		}
		return "", ""
	}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("http://%s\n", ln.Addr())
	log.Fatal(http.Serve(ln, p))
}
