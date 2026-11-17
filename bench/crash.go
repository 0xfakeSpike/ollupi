package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/ask"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/host"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/repo/catalog"
	"github.com/0xfakeSpike/ollupi/shard"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The kinds of conversation the crash workload mixes: a turn that calls the work tool twice, one that asks the user,
// and one that hands work to a subagent.
const (
	kindWork     = "work"
	kindAsk      = "ask"
	kindDelegate = "delegate"
)

// crash kills a process running turns at random moments and starts it again on the same database, then checks
// that every turn ended, that no tool call ran twice, and that the calls cut off mid-run were reported as such.
func crash(args []string) {
	fs := flag.NewFlagSet("crash", flag.ExitOnError)
	n := fs.Int("n", 300, "conversations submitted at the start")
	kills := fs.Int("kills", 20, "times the process is killed")
	minUp := fs.Duration("min", 300*time.Millisecond, "shortest time the process runs before a kill")
	maxUp := fs.Duration("max", 2*time.Second, "longest time the process runs before a kill")
	slots := fs.Int("slots", 32, "agent slots")
	calls := fs.Int("calls", 8, "model calls the connection allows in flight")
	latency := fs.Duration("latency", 200*time.Millisecond, "model latency")
	toolLatency := fs.Duration("tool", 300*time.Millisecond, "tool latency")
	lease := fs.Duration("lease", 0, "with a lease, a standby process already waits on the shard when one is killed")
	fs.Parse(args)

	var token atomic.Int64
	p := &provider{latency: *latency, jitter: *latency / 4, limit: *calls, next: func(asked string, answered int) (string, string) {
		switch {
		case asked == kindAsk && answered == 0:
			return ask.Tool, askArgs
		case asked == kindDelegate && answered == 0:
			return string(agent.GeneralSub), `{"description":"Do the work","prompt":"work"}`
		case asked == kindWork && answered < 2:
			return "bench__work", fmt.Sprintf(`{"token":"t%d"}`, token.Add(1))
		}
		return "", ""
	}}
	srv := httptest.NewServer(p)
	defer srv.Close()
	dir, err := os.MkdirTemp("", "ollupi-bench-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	start := func(submit int) (*exec.Cmd, <-chan string) {
		cmd := exec.Command(exe, "crashchild", "-dir", dir, "-url", srv.URL, "-slots", strconv.Itoa(*slots),
			"-calls", strconv.Itoa(*calls), "-tool", toolLatency.String(), "-submit", strconv.Itoa(submit), "-lease", lease.String())
		cmd.Stderr = os.Stderr
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
		return cmd, lines
	}
	await := func(lines <-chan string, prefix string) string {
		for line := range lines {
			if strings.HasPrefix(line, prefix) {
				return line
			}
		}
		log.Fatalf("the process ended before it said %q", prefix)
		return ""
	}

	cmd, lines := start(*n)
	await(lines, "ready")
	var standby *exec.Cmd
	var standbyLines <-chan string
	if *lease > 0 {
		standby, standbyLines = start(0)
	}
	var restarts []time.Duration
	var recoveries []string
	killed, drained := 0, false
	for killed < *kills && !drained {
		select {
		case line := <-lines:
			drained = strings.HasPrefix(line, "drained")
			continue
		case <-time.After(*minUp + time.Duration(rand.Int64N(int64(*maxUp-*minUp)))):
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		killed++
		at := time.Now()
		if *lease > 0 {
			cmd, lines = standby, standbyLines
		} else {
			cmd, lines = start(0)
		}
		ready := await(lines, "ready")
		if *lease > 0 {
			standby, standbyLines = start(0)
		}
		restarts = append(restarts, time.Since(at))
		recoveries = append(recoveries, strings.TrimPrefix(ready, "ready "))
	}
	if !drained {
		await(lines, "drained")
	}
	_ = cmd.Wait()
	if standby != nil {
		await(standbyLines, "drained")
		_ = standby.Wait()
	}

	g, err := gorm.Open(sqlite.Open(filepath.Join(dir, "data", "store.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		log.Fatal(err)
	}
	count := func(query string, args ...any) int64 {
		var c int64
		if err := g.Raw(query, args...).Scan(&c).Error; err != nil {
			log.Fatal(err)
		}
		return c
	}
	var statuses []struct {
		Status string
		N      int64
	}
	if err := g.Raw("SELECT coalesce(json_extract(body, '$.Ended.Status'), 'open') AS status, count(*) AS n FROM turns GROUP BY status ORDER BY status").Scan(&statuses).Error; err != nil {
		log.Fatal(err)
	}
	var byStatus []string
	for _, s := range statuses {
		byStatus = append(byStatus, fmt.Sprintf("%s:%d", s.Status, s.N))
	}
	var completedTokens []string
	if err := g.Raw("SELECT json_extract(json_extract(body, '$.Call.Arguments'), '$.token') FROM items WHERE json_extract(body, '$.Call.Tool') = 'bench__work' AND status = 'completed'").Scan(&completedTokens).Error; err != nil {
		log.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tools.log"))
	if err != nil {
		log.Fatal(err)
	}
	ran := map[string]int{}
	for _, t := range strings.Fields(string(raw)) {
		ran[t]++
	}
	twice, unrun := 0, 0
	for _, c := range ran {
		if c > 1 {
			twice++
		}
	}
	for _, t := range completedTokens {
		if ran[t] == 0 {
			unrun++
		}
	}
	slices.Sort(restarts)
	fmt.Printf("n=%d kills=%d turns=[%s] open_turns=%d open_items=%d cut_off_calls=%d cut_off_messages=%d tool_runs=%d "+
		"calls_run_twice=%d completed_calls_never_run=%d restart_p50_ms=%d restart_max_ms=%d requests=%d\n",
		*n, killed, strings.Join(byStatus, " "),
		count(openTurns),
		count("SELECT count(*) FROM items WHERE status = 'pending' OR status = 'inProgress'"),
		count("SELECT count(*) FROM items WHERE json_extract(body, '$.Error') LIKE 'the call was cut off%'"),
		// A model's answer cut off is not kept, and its request is made again: the requests answered that no response
		// was kept for.
		p.requests.Load()-p.refused.Load()-count("SELECT count(*) FROM entries WHERE kind = ?", string(conversation.KindModelResponded)),
		len(ran), twice, unrun, pct(restarts, .5).Milliseconds(), pct(restarts, 1).Milliseconds(), p.requests.Load())
	fmt.Printf("recoveries: %s\n", strings.Join(recoveries, " | "))
}

// openTurns counts the turns of every conversation, subagents' among them, that have not ended.
const openTurns = "SELECT count(*) FROM turns WHERE ended_seq IS NULL"

// crashChild is the process crash kills: it runs a host on the store in dir, answers every question it is asked,
// submits the workload when told to, and says when no turn is left open.
func crashChild(args []string) {
	fs := flag.NewFlagSet("crashchild", flag.ExitOnError)
	dir := fs.String("dir", "", "home of the store")
	url := fs.String("url", "", "the fake provider")
	slots := fs.Int("slots", 32, "agent slots")
	calls := fs.Int("calls", 8, "model calls the connection allows in flight")
	toolLatency := fs.Duration("tool", 300*time.Millisecond, "tool latency")
	submit := fs.Int("submit", 0, "conversations to submit once started")
	lease := fs.Duration("lease", 0, "how long a claim on the shard lasts")
	fs.Parse(args)

	toolLog, err := os.OpenFile(filepath.Join(*dir, "tools.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	// A standby puts its host together before it holds the shard, and writes nothing until it does.
	startAt := time.Now()
	h, err := host.New(context.Background(), host.Config{
		Home: *dir, Name: "bench", Models: []llm.Model{{Name: "fake", Connection: "fake"}},
		Connections: map[string]llm.Client{"fake": llm.NewOpenAI(*url, "", *calls, map[string]llm.Limits{"fake": {Context: 200_000, Output: 32_000}})},
		Tools:       []kit.Provider{work{latency: *toolLatency, log: toolLog}},
		Permissions: policy.Settings{ApprovalPolicy: string(policy.Never)},
		Boxes:       here, Slots: *slots,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()
	answer := func(threadID, itemID string) {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := h.App.Answer(context.Background(), defaultUser, threadID, itemID, ask.Answers{"pick": {Answers: []string{"A"}}}); err != nil {
				log.Printf("answer %s: %v", itemID, err)
			}
		}()
	}
	h.Store.Feed().SubscribeAll(func(e store.Event) {
		if e.Change == nil {
			return
		}
		for _, entry := range e.Change.Entries {
			if _, asked := entry.Body.(conversation.InputAsked); asked {
				answer(e.Thread, entry.Item)
			}
		}
	})
	run := func(ctx context.Context) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		ran := make(chan error, 1)
		go func() { ran <- h.Run(ctx) }()
		return crashServe(ctx, cancel, ran, h, *dir, *submit, time.Since(startAt), answer)
	}
	// With a lease the shard is held through a catalog beside the store, as a standby waits on it.
	if *lease == 0 {
		err = run(context.Background())
	} else {
		cat, cerr := catalog.OpenSQL(filepath.Join(*dir, "catalog.sqlite"))
		if cerr != nil {
			log.Fatal(cerr)
		}
		defer cat.Close()
		m := shard.Member{ID: "bench", Accepting: true, Catalog: cat, TTL: *lease}
		err = m.Run(context.Background(), h.Store, run)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func crashServe(ctx context.Context, cancel context.CancelFunc, ran <-chan error, h *host.Host, dir string, submit int, started time.Duration, answer func(threadID, itemID string)) error {
	if err := h.App.EnsureUser(ctx, defaultUser); err != nil {
		log.Fatal(err)
	}
	pending, err := h.App.Requests(ctx, defaultUser, "")
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range pending {
		answer(r.Thread.ID, r.Item.ID)
	}
	kinds := []string{kindWork, kindWork, kindWork, kindAsk, kindDelegate}
	for i := range submit {
		th, err := h.App.StartThread(ctx, defaultUser, "", "", "bench")
		if err != nil {
			log.Fatal(err)
		}
		if _, _, err := h.App.Send(ctx, defaultUser, th.ID, app.Input{Text: kinds[i%len(kinds)]}); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf("ready start_ms=%d answered=%d\n", started.Milliseconds(), len(pending))

	g, err := gorm.Open(sqlite.Open(filepath.Join(dir, "data", "store.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		log.Fatal(err)
	}
	for {
		var open int64
		if err := g.Raw(openTurns).Scan(&open).Error; err != nil {
			log.Fatal(err)
		}
		if open == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	cancel()
	if err := <-ran; err != nil {
		return err
	}
	fmt.Println("drained")
	return nil
}
