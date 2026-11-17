package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/host"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// fanout has turns each hand work to several subagents at once, more of them in all than there are slots, and
// checks that every turn ends: a parent waiting on its subagents must leave its slot to them. It reports how long the
// whole took against the least the slots and the provider allow, and the most parents ever holding a slot or waiting
// on their subagents without one.
func fanout(args []string) {
	fs := flag.NewFlagSet("fanout", flag.ExitOnError)
	parents := fs.Int("parents", 64, "turns submitted at once, each handing work to -n subagents")
	n := fs.Int("n", 8, "subagents each turn starts at once")
	slots := fs.Int("slots", 32, "agent slots")
	calls := fs.Int("calls", 256, "model calls the connection and the provider allow in flight")
	latency := fs.Duration("latency", 5*time.Second, "model latency")
	jitter := fs.Duration("jitter", 2*time.Second, "model latency varies by up to this much either way")
	toolLatency := fs.Duration("tool", time.Second, "tool latency")
	rounds := fs.Int("rounds", 2, "tool calls in each subagent's turn, each followed by a model call")
	fs.Parse(args)

	p := &provider{latency: *latency, jitter: *jitter, limit: *calls, fanout: *n,
		next: func(ask string, answered int) (string, string) {
			switch {
			case ask == "delegate" && answered == 0:
				return string(agent.GeneralSub), `{"description":"Do the work","prompt":"work"}`
			case ask == "work" && answered < *rounds:
				return "bench__work", "{}"
			}
			return "", ""
		}}
	srv := httptest.NewServer(p)
	defer srv.Close()
	http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost = *calls
	dir, err := os.MkdirTemp("", "ollupi-bench-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	tr := &tracker{startedAt: map[string]time.Time{}, endedAt: map[string]time.Time{}}
	e := newEngine(host.Config{
		Home: dir, Models: []llm.Model{{Name: "fake", Connection: "fake"}},
		Connections: map[string]llm.Client{"fake": llm.NewOpenAI(srv.URL, "", *calls, map[string]llm.Limits{"fake": {Context: 200_000, Output: 32_000}})},
		Tools:       []kit.Provider{work{latency: *toolLatency}},
		Slots:       *slots,
	})
	e.Store.Feed().SubscribeAll(tr.on)
	e.run()
	threads := make([]string, *parents)
	for i := range threads {
		threads[i] = e.newThread(defaultUser)
	}
	// A second connection reads, beside the host's, how many parents can run, holding a slot or waiting for one, and
	// how many wait on their subagents: the store keeps no record of slots.
	g, err := gorm.Open(sqlite.Open(filepath.Join(dir, "data", "store.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		log.Fatal(err)
	}
	var holding, waiting atomic.Int64
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(250 * time.Millisecond):
			}
			var c struct{ Holding, Waiting int64 }
			if err := g.Raw(`SELECT coalesce(sum(runnable_since IS NOT NULL), 0) AS holding,
				coalesce(sum(status = 'active' AND runnable_since IS NULL), 0) AS waiting FROM threads WHERE parent_id IS NULL`).Scan(&c).Error; err != nil {
				log.Fatal(err)
			}
			holding.Store(max(holding.Load(), c.Holding))
			waiting.Store(max(waiting.Load(), c.Waiting))
		}
	}()

	startAt := time.Now()
	var ids []string
	for _, th := range threads {
		ids = append(ids, tr.start(e, defaultUser, th, "delegate"))
	}
	waitFor("the parents", func() bool { return len(tr.took(ids)) == len(ids) })
	total := time.Since(startAt)
	close(stop)

	// Each parent makes at least three model calls of its own: one launches the subagents, one answers while they run,
	// and one answers their reports, of which those that come apart wake it more than once; each subagent makes
	// rounds+1 calls and rounds tool calls. The answer while they run is not on the parent's way to its end.
	child := time.Duration(*rounds+1)*(*latency) + time.Duration(*rounds)*(*toolLatency)
	children := *parents * *n
	bound := max(2*(*latency)+child,
		time.Duration(children)*child/time.Duration(*slots),
		time.Duration(*parents*3+children*(*rounds+1))*(*latency)/time.Duration(*calls))
	took := tr.took(ids)
	fmt.Printf("parents=%d n=%d subagents=%d slots=%d calls=%d total_s=%.1f bound_s=%.1f efficiency=%.2f p50_s=%.1f max_s=%.1f "+
		"failed_turns=%d turns_ended=%d parents_holding_slot_peak=%d parents_waiting_peak=%d refused_429=%d\n",
		*parents, *n, children, *slots, *calls, total.Seconds(), bound.Seconds(), bound.Seconds()/total.Seconds(),
		pct(took, .5).Seconds(), pct(took, 1).Seconds(), tr.failed.Load(), tr.ended.Load(), holding.Load(), waiting.Load(), p.refused.Load())
	e.stop()
}
