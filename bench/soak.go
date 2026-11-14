package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

// soak keeps conversations busy for a long time, each turn adding to its conversation's history, and samples what
// should stay flat: turn time, memory, goroutines, collector pauses, and how the database and its log grow.
func soak(args []string) {
	fs := flag.NewFlagSet("soak", flag.ExitOnError)
	c := fs.Int("c", 512, "conversations whose user sends a turn as soon as the last one ends")
	d := fs.Duration("d", 30*time.Minute, "how long the run lasts")
	every := fs.Duration("every", time.Minute, "how often to sample")
	slots := fs.Int("slots", 0, "agent slots (default: c)")
	calls := fs.Int("calls", 0, "model calls the connection and the provider allow in flight (default: c)")
	latency := fs.Duration("latency", 5*time.Second, "model latency")
	jitter := fs.Duration("jitter", 2*time.Second, "model latency varies by up to this much either way")
	toolLatency := fs.Duration("tool", time.Second, "tool latency")
	rounds := fs.Int("rounds", 2, "tool calls in each turn, each followed by a model call")
	out := fs.String("csv", "", "write each sample to this CSV file")
	pprofAddr := fs.String("pprof", "", "serve net/http/pprof on this address, to profile the run while it goes")
	fs.Parse(args)
	if *pprofAddr != "" {
		go func() { log.Println(http.ListenAndServe(*pprofAddr, nil)) }()
	}
	if *slots == 0 {
		*slots = *c
	}
	if *calls == 0 {
		*calls = *c
	}

	// As in steady, the provider runs in a process of its own, so what it holds is not counted against the agent.
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	served := exec.Command(exe, "provider", "-latency", latency.String(), "-jitter", jitter.String(),
		"-rounds", strconv.Itoa(*rounds), "-limit", strconv.Itoa(*calls))
	served.Stderr = os.Stderr
	pipe, err := served.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}
	if err := served.Start(); err != nil {
		log.Fatal(err)
	}
	defer served.Process.Kill()
	url, err := bufio.NewReader(pipe).ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	url = strings.TrimSpace(url)
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
	ag := orchestrator.New(orchestrator.Config{
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
		th, err := ag.StartThread(repo.DefaultUser, "", "", "bench", permission.Settings{})
		if err != nil {
			log.Fatal(err)
		}
		threads[i] = th.ID
		tr.done[th.ID] = make(chan struct{}, 1)
	}
	tr.mu.Unlock()

	startAt := time.Now()
	deadline := startAt.Add(*d)
	var mu sync.Mutex
	var ids []string
	var wg sync.WaitGroup
	for _, id := range threads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				turnID := tr.start(ag, repo.DefaultUser, id, "work")
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

	size := func(name string) float64 {
		info, err := os.Stat(filepath.Join(dir, "data", name))
		if err != nil {
			return 0
		}
		return float64(info.Size()) / (1 << 20)
	}
	pauses := []metrics.Sample{{Name: "/sched/pauses/total/gc:seconds"}}
	var lastPauses *metrics.Float64Histogram
	rows := []string{"minutes,turns_ended,history_turns,p50_s,p99_s,rss_mb,heap_mb,goroutines,gc_pause_p99_ms,gc_pause_max_ms,db_mb,wal_mb," +
		"cpu_ms_per_turn,db_read_ms_per_turn,db_write_ms_per_turn,conn_wait_ms_per_turn"}
	var seen int
	lastCPU, lastPool := cpu(), dbs.pool.Stats()
	var lastRead, lastWrite time.Duration
	sample := func() {
		mu.Lock()
		all := slices.Clone(ids)
		mu.Unlock()
		tr.mu.Lock()
		var ended []time.Duration
		var n int
		for _, id := range all {
			if end, ok := tr.endedAt[id]; ok {
				n++
				if end.After(time.Now().Add(-*every)) {
					ended = append(ended, end.Sub(tr.startedAt[id]))
				}
			}
		}
		tr.mu.Unlock()
		slices.Sort(ended)
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		metrics.Read(pauses)
		h := pauses[0].Value.Float64Histogram()
		p99, worst := pauseQuantiles(h, lastPauses)
		// Read fills the same histogram each time, so the counts to compare the next sample with are copied.
		lastPauses = &metrics.Float64Histogram{Counts: slices.Clone(h.Counts), Buckets: h.Buckets}
		used, pool := cpu(), dbs.pool.Stats()
		dbs.mu.Lock()
		read, write := dbs.readTime, dbs.writeTime
		dbs.mu.Unlock()
		per := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) / float64(max(n-seen, 1)) }
		rows = append(rows, fmt.Sprintf("%.1f,%d,%d,%.2f,%.2f,%.0f,%.0f,%d,%.2f,%.2f,%.1f,%.1f,%.1f,%.2f,%.2f,%.2f",
			time.Since(startAt).Minutes(), n-seen, n / *c, pct(ended, .5).Seconds(), pct(ended, .99).Seconds(),
			rssMB(), float64(m.HeapInuse)/(1<<20), runtime.NumGoroutine(), p99*1000, worst*1000,
			size("ollupi.sqlite"), size("ollupi.sqlite-wal"),
			per(used-lastCPU), per(read-lastRead), per(write-lastWrite), per(pool.WaitDuration-lastPool.WaitDuration)))
		seen, lastCPU, lastPool, lastRead, lastWrite = n, used, pool, read, write
		fmt.Println(rows[len(rows)-1])
	}
	fmt.Println(rows[0])
	for time.Now().Before(deadline) {
		time.Sleep(min(*every, time.Until(deadline)))
		sample()
	}
	wg.Wait()
	if *out != "" {
		if err := os.WriteFile(*out, []byte(strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	cancel()
	ag.Wait()
}

// pauseQuantiles reads the 99th percentile and the longest of the collector pauses since the histogram before, in
// seconds, from the upper bounds of their buckets.
func pauseQuantiles(h, before *metrics.Float64Histogram) (float64, float64) {
	counts := slices.Clone(h.Counts)
	if before != nil {
		for i := range counts {
			counts[i] -= before.Counts[i]
		}
	}
	var total uint64
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return 0, 0
	}
	var p99, worst float64
	var seen uint64
	for i, n := range counts {
		if n == 0 {
			continue
		}
		seen += n
		upper := h.Buckets[i+1]
		if p99 == 0 && float64(seen) >= 0.99*float64(total) {
			p99 = upper
		}
		worst = upper
	}
	return p99, worst
}
