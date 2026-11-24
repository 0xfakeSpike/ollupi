package main

import (
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
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/host"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

type mixTurn struct {
	kind, thread, turnID string
	submitted, first     time.Time
}

func mix(args []string) {
	fs := flag.NewFlagSet("mix", flag.ExitOnError)
	c := fs.Int("c", 256, "conversations whose user sends a turn as soon as the last one ends")
	delegate := fs.Float64("delegate", 0.2, "share of user turns that hand the work to a subagent")
	peak := fs.Int("peak", 1000, "scheduled-job turns that fall due at once, each in a conversation of its own")
	peakAt := fs.Duration("peak-at", time.Minute, "when into the run the scheduled-job turns fall due")
	d := fs.Duration("d", 4*time.Minute, "how long users keep sending turns")
	slots := fs.Int("slots", 512, "agent slots")
	calls := fs.Int("calls", 256, "model calls the connection and the provider allow in flight")
	latency := fs.Duration("latency", 5*time.Second, "model latency")
	jitter := fs.Duration("jitter", 2*time.Second, "model latency varies by up to this much either way")
	toolLatency := fs.Duration("tool", time.Second, "tool latency")
	rounds := fs.Int("rounds", 2, "tool calls in each turn that does the work, each followed by a model call")
	fs.Parse(args)

	var mu sync.Mutex
	turns := map[string]*mixTurn{}
	p := &provider{latency: *latency, jitter: *jitter, limit: *calls,
		next: func(ask string, answered int) (string, string) {
			kind, id, _ := strings.Cut(ask, "#")
			switch {
			case kind == "delegate" && answered == 0:
				return string(agent.GeneralSub), fmt.Sprintf(`{"description":"Do the work","prompt":"sub#%s"}`, id)
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
	dir := tempHome()
	defer os.RemoveAll(dir)
	tr := newTracker()
	e := newEngine(host.Config{
		Home: dir, Dial: fakeDial(fakeConnection(srv.URL, *calls)),
		Tools: []kit.Provider{work{latency: *toolLatency}},
		Slots: *slots,
	})
	e.Store.Feed().SubscribeAll(func(ev store.Event) {
		if ev.Change != nil && ev.Change.Header != nil && ev.Change.Header.Parent != "" {
			for _, entry := range ev.Change.Entries {
				if m, ok := entry.Body.(conversation.UserMessage); ok {
					mu.Lock()
					turns[m.Text] = &mixTurn{kind: "sub", thread: ev.Change.Header.Parent, turnID: entry.Turn, submitted: time.Now()}
					mu.Unlock()
				}
			}
		}
		tr.on(ev)
	})
	e.run()
	var seq int
	send := func(kind, thread string) {
		mu.Lock()
		seq++
		text := fmt.Sprintf("%s#%d", kind, seq)
		t := &mixTurn{kind: kind, thread: thread, submitted: time.Now()}
		turns[text] = t
		mu.Unlock()
		id := tr.start(e, defaultUser, thread, text)
		mu.Lock()
		t.turnID = id
		mu.Unlock()
	}
	users := make([]string, *c)
	tr.mu.Lock()
	for i := range users {
		users[i] = e.newThread(defaultUser)
		tr.done[users[i]] = make(chan struct{}, 1)
	}
	tr.mu.Unlock()
	scheduled := make([]string, *peak)
	for i := range scheduled {
		scheduled[i] = e.newThread(defaultUser)
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
	waitFor("the scheduled-job turns", func() bool {
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
	e.stop()
}
