package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/host"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

func resume(args []string) {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	histories := fs.String("history", "10,100,1000", "turns of history in the conversations resumed")
	tries := fs.Int("tries", 20, "messages timed for each history length")
	waiting := fs.Int("waiting", 10000, "conversations waiting on the user across the restart")
	running := fs.Int("running", 500, "turns in a model call when the process stops")
	fs.Parse(args)

	dir := tempHome()
	defer os.RemoveAll(dir)
	answer := strings.Repeat("A paragraph of an answer, about as long as a short reply in a chat. ", 6)
	open := func(m *model, tr *tracker) (*engine, time.Duration) {
		at := time.Now()
		e := newEngine(host.Config{
			Home: dir, Models: fakeModels,
			Connections: map[string]llm.Client{"fake": m}, Slots: *running + 32,
		})
		e.Store.Feed().SubscribeAll(tr.on)
		e.run()
		return e, time.Since(at)
	}
	m := &model{answer: answer}
	tr := newTracker()
	e, _ := open(m, tr)
	newThread := func() string {
		id := e.newThread(defaultUser)
		tr.mu.Lock()
		tr.done[id] = make(chan struct{}, 1)
		tr.mu.Unlock()
		return id
	}
	turn := func(id, text string) {
		tr.start(e, defaultUser, id, text)
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
		tr.start(e, defaultUser, newThread(), "ASK")
	}
	waitFor("the waiting conversations", func() bool { return tr.asked.Load() >= int64(*waiting) })
	m.latency.Store(int64(time.Hour))
	before := m.calls.Load()
	for range *running {
		tr.start(e, defaultUser, newThread(), "work")
	}
	waitFor("the running turns", func() bool { return m.calls.Load()-before >= int64(*running) })
	e.stop()

	m2 := &model{answer: answer}
	m2.latency.Store(int64(time.Hour))
	tr2 := newTracker()
	at := time.Now()
	e2, started := open(m2, tr2)
	waitFor("the running turns to send again", func() bool { return m2.calls.Load() >= int64(*running) })
	fmt.Printf("restart waiting=%d running=%d start_ms=%d resent_ms=%d\n", *waiting, *running, started.Milliseconds(), time.Since(at).Milliseconds())
	e2.stop()
}
