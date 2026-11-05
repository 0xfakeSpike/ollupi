package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

// mixTurn is one turn of the mix: its kind, its conversation, when it was submitted, its first model request
// and its end. A subagent's turn counts as submitted when it becomes its thread's current turn.
type mixTurn struct {
	kind, thread, turnID string
	submitted, first     time.Time
}

// mix keeps conversations busy with users who send a turn as soon as the last one ends, some of which hand the work
// to a subagent, and drops a peak of scheduled-task turns among them. It reports, for each kind of turn, how long it
// waited for its first model request and how long it took, and how evenly the users' conversations were served.
func mix(args []string) {
	fs := flag.NewFlagSet("mix", flag.ExitOnError)
	c := fs.Int("c", 256, "conversations whose user sends a turn as soon as the last one ends")
	delegate := fs.Float64("delegate", 0.2, "share of user turns that hand the work to a subagent")
	peak := fs.Int("peak", 1000, "scheduled-task turns that fall due at once, each in a conversation of its own")
	peakAt := fs.Duration("peak-at", time.Minute, "when into the run the scheduled-task turns fall due")
	d := fs.Duration("d", 4*time.Minute, "how long users keep sending turns")
	slots := fs.Int("slots", 512, "agent slots")
	calls := fs.Int("calls", 256, "model calls the connection and the provider allow in flight")
	latency := fs.Duration("latency", 5*time.Second, "model latency")
	jitter := fs.Duration("jitter", 2*time.Second, "model latency varies by up to this much either way")
	toolLatency := fs.Duration("tool", time.Second, "tool latency")
	rounds := fs.Int("rounds", 2, "tool calls in each turn that does the work, each followed by a model call")
	fs.Parse(args)

	var mu sync.Mutex
	turns := map[string]*mixTurn{} // by the text the turn was sent with
	p := &provider{latency: *latency, jitter: *jitter, limit: *calls,
		next: func(ask string, answered int) (string, string) {
			kind, id, _ := strings.Cut(ask, "#")
			switch {
			case kind == "delegate" && answered == 0:
				return subagent.Tool, fmt.Sprintf(`{"description":"Do the work","prompt":"sub#%s"}`, id)
			case kind != "delegate" && answered < *rounds:
				return "bench__work", "{}"
			}
			return "", ""
		},
		arrived: func(ask string) {
			mu.Lock()
			if t := turns[ask]; t != nil && t.first.IsZero() {
				t.first = time.Now()
			}
			mu.Unlock()
		}}
	srv := httptest.NewServer(p)
	defer srv.Close()
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
	tr := &tracker{startedAt: map[string]time.Time{}, endedAt: map[string]time.Time{}, done: map[string]chan struct{}{}}
	ag := agent.New(agent.Config{
		Name: "bench", Models: []llm.Model{{Name: "fake", Connection: "fake"}},
		Connections: map[string]llm.Client{"fake": llm.NewOpenAI(srv.URL, "", *calls, map[string]llm.Limits{"fake": {Context: 200_000, Output: 32_000}})},
		Providers:   []tools.Provider{work{latency: *toolLatency}},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		Slots:       *slots, DB: db,
		OnEvent: func(e agent.Event) {
			if e.Kind == agent.EventTurnStarted && e.Thread.ParentThreadID != "" {
				mu.Lock()
				turns[e.Turn.Input] = &mixTurn{kind: "sub", thread: string(e.Thread.ParentThreadID), turnID: e.Turn.ID, submitted: time.Now()}
				mu.Unlock()
			}
			tr.on(e)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := ag.Start(ctx); err != nil {
		log.Fatal(err)
	}
	newThread := func() string {
		th, err := ag.StartThread(repo.DefaultUser, "", "bench", permission.Settings{})
		if err != nil {
			log.Fatal(err)
		}
		return th.ID
	}
	var seq int
	send := func(kind, thread string) {
		mu.Lock()
		seq++
		text := fmt.Sprintf("%s#%d", kind, seq)
		t := &mixTurn{kind: kind, thread: thread, submitted: time.Now()}
		turns[text] = t
		mu.Unlock()
		id := tr.start(ag, thread, text)
		mu.Lock()
		t.turnID = id
		mu.Unlock()
	}
	users := make([]string, *c)
	tr.mu.Lock()
	for i := range users {
		users[i] = newThread()
		tr.done[users[i]] = make(chan struct{}, 1)
	}
	tr.mu.Unlock()
	scheduled := make([]string, *peak)
	for i := range scheduled {
		scheduled[i] = newThread()
	}

	startAt := time.Now()
	deadline := startAt.Add(*d)
	var wg sync.WaitGroup
	for _, th := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				kind := "work"
				if rand.Float64() < *delegate {
					kind = "delegate"
				}
				send(kind, th)
				select {
				case <-tr.done[th]:
				case <-time.After(time.Hour):
					log.Fatal("timed out waiting for a user turn")
				}
			}
		}()
	}
	time.Sleep(time.Until(startAt.Add(*peakAt)))
	peakSent := time.Now()
	for _, th := range scheduled {
		send("cron", th)
	}
	wg.Wait()
	waitFor("the scheduled-task turns", func() bool {
		mu.Lock()
		defer mu.Unlock()
		var ids []string
		for _, t := range turns {
			if t.kind == "cron" {
				ids = append(ids, t.turnID)
			}
		}
		return len(tr.took(ids)) == len(ids)
	})

	// The peak drains when the last scheduled-task turn sends its first model request.
	mu.Lock()
	tr.mu.Lock()
	var drained time.Time
	for _, t := range turns {
		if t.kind == "cron" && t.first.After(drained) {
			drained = t.first
		}
	}
	waits, took := map[string][]time.Duration{}, map[string][]time.Duration{}
	perUser := map[string][]time.Duration{}
	for _, t := range turns {
		if t.first.IsZero() {
			continue
		}
		key := t.kind
		if t.kind == "work" || t.kind == "delegate" {
			key = "user_outside_peak"
			if !t.submitted.Before(peakSent) && t.submitted.Before(drained) {
				key = "user_during_peak"
			}
			waits["user"] = append(waits["user"], t.first.Sub(t.submitted))
		}
		waits[key] = append(waits[key], t.first.Sub(t.submitted))
		if end, ok := tr.endedAt[t.turnID]; ok {
			took[t.kind] = append(took[t.kind], end.Sub(t.submitted))
			if t.kind == "work" {
				perUser[t.thread] = append(perUser[t.thread], end.Sub(t.submitted))
			}
		}
	}
	tr.mu.Unlock()
	mu.Unlock()

	// Jain's index over each user's mean work turn: 1 when every conversation is served alike, 1/n when one gets all.
	var sum, squares float64
	for _, ds := range perUser {
		var total time.Duration
		for _, x := range ds {
			total += x
		}
		mean := total.Seconds() / float64(len(ds))
		sum += mean
		squares += mean * mean
	}
	jain := sum * sum / (float64(len(perUser)) * squares)

	ms := func(x time.Duration) string { return fmt.Sprintf("%.0f", float64(x)/float64(time.Millisecond)) }
	fmt.Printf("c=%d delegate=%.2f peak=%d slots=%d calls=%d run_s=%.0f peak_drained_s=%.1f failed_turns=%d jain_user=%.3f\n",
		*c, *delegate, *peak, *slots, *calls, time.Since(startAt).Seconds(), drained.Sub(peakSent).Seconds(), tr.failed.Load(), jain)
	for _, kind := range []string{"user", "user_outside_peak", "user_during_peak", "sub", "cron"} {
		w := waits[kind]
		slices.Sort(w)
		fmt.Printf("wait kind=%s n=%d p50_ms=%s p99_ms=%s max_ms=%s\n", kind, len(w), ms(pct(w, .5)), ms(pct(w, .99)), ms(pct(w, 1)))
	}
	for _, kind := range []string{"work", "delegate", "sub", "cron"} {
		t := took[kind]
		slices.Sort(t)
		fmt.Printf("took kind=%s n=%d p50_s=%.1f p99_s=%.1f max_s=%.1f\n", kind, len(t), pct(t, .5).Seconds(), pct(t, .99).Seconds(), pct(t, 1).Seconds())
	}
	cancel()
	ag.Wait()
}
