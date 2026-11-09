package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
)

// resume times what bringing work back costs: a message to a conversation idle in the database, by the length of its
// history, until its model request goes out; and a restart of the process with conversations waiting and turns
// running, until the running turns have sent their requests again.
func resume(args []string) {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	histories := fs.String("history", "10,100,1000", "turns of history in the conversations resumed")
	tries := fs.Int("tries", 20, "messages timed for each history length")
	waiting := fs.Int("waiting", 10000, "conversations waiting on the user across the restart")
	running := fs.Int("running", 500, "turns in a model call when the process stops")
	fs.Parse(args)

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
	answer := strings.Repeat("A paragraph of an answer, about as long as a short reply in a chat. ", 6)
	open := func(m *model, tr *tracker) (*agent.Agent, context.CancelFunc, time.Duration) {
		ag := agent.New(agent.Config{
			Name: "bench", Models: []llm.Model{{Name: "fake", Connection: "fake"}},
			Connections: map[string]llm.Client{"fake": m},
			Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
			Slots:       *running + 32, DB: db, OnEvent: tr.on,
		})
		ctx, cancel := context.WithCancel(context.Background())
		at := time.Now()
		if err := ag.Start(ctx); err != nil {
			log.Fatal(err)
		}
		return ag, cancel, time.Since(at)
	}
	m := &model{answer: answer}
	tr := &tracker{startedAt: map[string]time.Time{}, endedAt: map[string]time.Time{}, done: map[string]chan struct{}{}}
	ag, cancel, _ := open(m, tr)
	newThread := func() string {
		th, err := ag.StartThread(repo.DefaultUser, "", "", "bench", permission.Settings{})
		if err != nil {
			log.Fatal(err)
		}
		tr.mu.Lock()
		tr.done[th.ID] = make(chan struct{}, 1)
		tr.mu.Unlock()
		return th.ID
	}
	turn := func(id, text string) {
		tr.start(ag, repo.DefaultUser, id, text)
		select {
		case <-tr.done[id]:
		case <-time.After(time.Minute):
			log.Fatal("timed out waiting for a turn")
		}
	}

	for _, h := range strings.Split(*histories, ",") {
		n, err := strconv.Atoi(h)
		if err != nil {
			log.Fatal(err)
		}
		id := newThread()
		message := strings.Repeat("A question from the user, of a sentence or two. ", 3)
		for range n {
			turn(id, message)
		}
		var took []time.Duration
		for range *tries {
			called := make(chan time.Time, 1)
			f := func() {
				select {
				case called <- time.Now():
				default:
				}
			}
			m.onCall.Store(&f)
			at := time.Now()
			turn(id, message)
			took = append(took, (<-called).Sub(at))
		}
		m.onCall.Store(nil)
		slices.Sort(took)
		fmt.Printf("history_turns=%d first_request_p50_ms=%.1f first_request_p95_ms=%.1f\n", n,
			float64(pct(took, .5).Microseconds())/1000, float64(pct(took, .95).Microseconds())/1000)
	}

	for range *waiting {
		tr.start(ag, repo.DefaultUser, newThread(), "ASK")
	}
	waitFor("the waiting conversations", func() bool { return tr.asked.Load() >= int64(*waiting) })
	m.latency.Store(int64(time.Hour))
	before := m.calls.Load()
	for range *running {
		tr.start(ag, repo.DefaultUser, newThread(), "work")
	}
	waitFor("the running turns", func() bool { return m.calls.Load()-before >= int64(*running) })
	cancel()
	ag.Wait()

	m2 := &model{answer: answer}
	m2.latency.Store(int64(time.Hour))
	tr2 := &tracker{startedAt: map[string]time.Time{}, endedAt: map[string]time.Time{}}
	at := time.Now()
	ag2, cancel2, started := open(m2, tr2)
	waitFor("the running turns to send again", func() bool { return m2.calls.Load() >= int64(*running) })
	fmt.Printf("restart waiting=%d running=%d start_ms=%d resent_ms=%d\n", *waiting, *running, started.Milliseconds(), time.Since(at).Milliseconds())
	cancel2()
	ag2.Wait()
}
