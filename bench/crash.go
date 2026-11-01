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
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
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
	fs.Parse(args)

	var token atomic.Int64
	p := &provider{latency: *latency, jitter: *latency / 4, limit: *calls, next: func(ask string, answered int) (string, string) {
		switch {
		case ask == kindAsk && answered == 0:
			return userinput.Tool, askArgs
		case ask == kindDelegate && answered == 0:
			return "Agent", `{"description":"Do the work","prompt":"work"}`
		case ask == kindWork && answered < 2:
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
			"-calls", strconv.Itoa(*calls), "-tool", toolLatency.String(), "-submit", strconv.Itoa(submit))
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
		cmd, lines = start(0)
		ready := await(lines, "ready")
		restarts = append(restarts, time.Since(at))
		recoveries = append(recoveries, strings.TrimPrefix(ready, "ready "))
	}
	if !drained {
		await(lines, "drained")
	}
	_ = cmd.Wait()

	g, err := gorm.Open(sqlite.Open(filepath.Join(dir, "data", "ollupi.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
	if err := g.Raw("SELECT status, count(*) AS n FROM turns GROUP BY status ORDER BY status").Scan(&statuses).Error; err != nil {
		log.Fatal(err)
	}
	var byStatus []string
	for _, s := range statuses {
		byStatus = append(byStatus, fmt.Sprintf("%s:%d", s.Status, s.N))
	}
	var completedTokens []string
	if err := g.Raw("SELECT json_extract(arguments, '$.token') FROM items WHERE tool = 'bench__work' AND status = 'completed'").Scan(&completedTokens).Error; err != nil {
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
		count("SELECT count(*) FROM turns WHERE NOT (status = 'completed' OR status = 'failed' OR status = 'interrupted')"),
		count("SELECT count(*) FROM items WHERE status = 'pending' OR status = 'inProgress'"),
		count("SELECT count(*) FROM items WHERE error LIKE 'the app stopped while this call was running%'"),
		count("SELECT count(*) FROM items WHERE error = 'the app stopped before this finished'"),
		len(ran), twice, unrun, pct(restarts, .5).Milliseconds(), pct(restarts, 1).Milliseconds(), p.requests.Load())
	fmt.Printf("recoveries: %s\n", strings.Join(recoveries, " | "))
}

// crashChild is the process crash kills: it runs the agent on the database in dir, answers every question it is
// asked, submits the workload when told to, and says when no turn is left open.
func crashChild(args []string) {
	fs := flag.NewFlagSet("crashchild", flag.ExitOnError)
	dir := fs.String("dir", "", "home of the database")
	url := fs.String("url", "", "the fake provider")
	slots := fs.Int("slots", 32, "agent slots")
	calls := fs.Int("calls", 8, "model calls the connection allows in flight")
	toolLatency := fs.Duration("tool", 300*time.Millisecond, "tool latency")
	submit := fs.Int("submit", 0, "conversations to submit once started")
	fs.Parse(args)

	toolLog, err := os.OpenFile(filepath.Join(*dir, "tools.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	db, err := repo.Open(*dir)
	if err != nil {
		log.Fatal(err)
	}
	var ag *agent.Agent
	answer := func(threadID, itemID string) {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := ag.AnswerUserInput(threadID, itemID, userinput.Answers{"pick": {Answers: []string{"A"}}}); err != nil {
				log.Printf("answer %s: %v", itemID, err)
			}
		}()
	}
	ag = agent.New(agent.Config{
		Name: "bench", Models: []llm.Model{{Name: "fake", Connection: "fake"}},
		Connections: map[string]llm.Client{"fake": llm.NewOpenAI(*url, "", *calls, map[string]llm.Limits{"fake": {Context: 200_000, Output: 32_000}})},
		Providers:   []tools.Provider{work{latency: *toolLatency, log: toolLog}},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		Slots:       *slots, DB: db,
		OnEvent: func(e agent.Event) {
			if e.Kind == agent.EventUserInputRequested {
				answer(e.Thread.ID, e.Item.ID)
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	startAt := time.Now()
	if err := ag.Start(ctx); err != nil {
		log.Fatal(err)
	}
	started := time.Since(startAt)
	pending := ag.AllPendingRequests()
	for _, r := range pending {
		answer(r.Thread.ID, r.Item.ID)
	}
	kinds := []string{kindWork, kindWork, kindWork, kindAsk, kindDelegate}
	for i := range *submit {
		th, err := ag.StartThread("", "bench", permission.Settings{})
		if err != nil {
			log.Fatal(err)
		}
		if _, err := ag.StartTurn(th.ID, agent.Input{Text: kinds[i%len(kinds)]}); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf("ready start_ms=%d answered=%d\n", started.Milliseconds(), len(pending))

	g, err := gorm.Open(sqlite.Open(filepath.Join(*dir, "data", "ollupi.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		log.Fatal(err)
	}
	for {
		var open int64
		if err := g.Raw("SELECT count(*) FROM turns WHERE NOT (status = 'completed' OR status = 'failed' OR status = 'interrupted')").Scan(&open).Error; err != nil {
			log.Fatal(err)
		}
		if open == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	cancel()
	ag.Wait()
	fmt.Println("drained")
}
