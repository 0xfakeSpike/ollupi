package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/host"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

const heavyUser = "heavy"

func users(args []string) {
	fs := flag.NewFlagSet("users", flag.ExitOnError)
	n := fs.Int("users", 32, "users beside the heavy one")
	c := fs.Int("c", 8, "conversations each of them keeps busy")
	heavy := fs.Int("heavy", 1024, "conversations the heavy user keeps busy")
	calls := fs.Int("calls", 16, "model calls each other user's key allows in flight")
	heavyCalls := fs.Int("heavy-calls", 32, "model calls the heavy user's key allows in flight")
	heavyLimit := fs.Int("heavy-limit", 0, "requests the provider lets the heavy user's key hold before it answers 429 (default: heavy-calls)")
	slots := fs.Int("slots", 512, "agent slots")
	userSlots := fs.Int("user-slots", 0, "the most slots one user may hold (default: all)")
	d := fs.Duration("d", 3*time.Minute, "how long users keep sending turns")
	latency := fs.Duration("latency", 5*time.Second, "model latency")
	jitter := fs.Duration("jitter", 2*time.Second, "model latency varies by up to this much either way")
	toolLatency := fs.Duration("tool", time.Second, "tool latency")
	rounds := fs.Int("rounds", 2, "tool calls in each turn, each followed by a model call")
	fs.Parse(args)
	if *heavyLimit == 0 {
		*heavyLimit = *heavyCalls
	}

	ids := []string{heavyUser}
	for i := range *n {
		ids = append(ids, fmt.Sprintf("user%03d", i))
	}
	keys := map[string]*quota{heavyUser: {limit: int64(*heavyLimit)}}
	for _, u := range ids[1:] {
		keys[u] = &quota{limit: int64(*calls)}
	}
	var mu sync.Mutex
	first := map[string]time.Time{}
	p := &provider{latency: *latency, jitter: *jitter, limit: *heavyLimit + *n**calls, keys: keys,
		next: func(_ string, answered int) (string, string) {
			if answered < *rounds {
				return "bench__work", "{}"
			}
			return "", ""
		},
		arrived: func(ask string) {
			mu.Lock()
			if _, ok := first[ask]; !ok {
				first[ask] = time.Now()
			}
			mu.Unlock()
		}}
	srv := httptest.NewServer(p)
	defer srv.Close()
	http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost = p.limit

	dir, err := os.MkdirTemp("", "ollupi-bench-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	limits := map[string]llm.Limits{"fake": {Context: 200_000, Output: 32_000}}
	clients := map[string]llm.Client{heavyUser: llm.NewOpenAI(srv.URL, heavyUser, *heavyCalls, limits)}
	for _, u := range ids[1:] {
		clients[u] = llm.NewOpenAI(srv.URL, u, *calls, limits)
	}
	var failed sync.Map
	tr := &tracker{startedAt: map[string]time.Time{}, endedAt: map[string]time.Time{}, done: map[string]chan struct{}{}}
	e := newEngine(host.Config{
		Home: dir, Models: []llm.Model{{Name: "fake", Connection: "fake"}},
		Connections: map[string]llm.Client{"fake": clients[heavyUser]},
		ClientFor: func(user, _ string) (llm.Client, error) {
			if cl := clients[user]; cl != nil {
				return cl, nil
			}
			return nil, fmt.Errorf("no key for user %s", user)
		},
		Tools: []kit.Provider{work{latency: *toolLatency}},
		Slots: *slots, UserSlots: *userSlots,
	})
	for _, u := range ids {
		if err := e.App.EnsureUser(context.Background(), u); err != nil {
			log.Fatal(err)
		}
	}
	e.Store.Feed().SubscribeAll(func(ev store.Event) {
		if ev.Change != nil {
			for _, entry := range ev.Change.Entries {
				if end, ok := entry.Body.(conversation.TurnEnded); ok && end.Status != conversation.Completed {
					count, _ := failed.LoadOrStore(ev.User, new(atomic.Int64))
					count.(*atomic.Int64).Add(1)
				}
			}
		}
		tr.on(ev)
	})
	e.run()

	type busy struct{ user, thread string }
	var conversations []busy
	tr.mu.Lock()
	for _, u := range ids {
		count := *c
		if u == heavyUser {
			count = *heavy
		}
		for range count {
			id := e.newThread(u)
			conversations = append(conversations, busy{u, id})
			tr.done[id] = make(chan struct{}, 1)
		}
	}
	tr.mu.Unlock()

	type sentTurn struct{ user, text string }
	sent := map[string]sentTurn{}
	var seq atomic.Int64
	startAt := time.Now()
	deadline := startAt.Add(*d)
	var wg sync.WaitGroup
	for _, cv := range conversations {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				text := fmt.Sprintf("%s#%d", cv.user, seq.Add(1))
				id := tr.start(e, cv.user, cv.thread, text)
				mu.Lock()
				sent[id] = sentTurn{cv.user, text}
				mu.Unlock()
				select {
				case <-tr.done[cv.thread]:
				case <-time.After(time.Hour):
					log.Fatal("timed out waiting for a turn")
				}
			}
		}()
	}
	wg.Wait()
	run := time.Since(startAt)

	mu.Lock()
	tr.mu.Lock()
	waits, took := map[string][]time.Duration{}, map[string][]time.Duration{}
	perUser := map[string][]time.Duration{}
	for id, s := range sent {
		class := "others"
		if s.user == heavyUser {
			class = heavyUser
		}
		end, ended := tr.endedAt[id]
		if at, ok := first[s.text]; ok {
			waits[class] = append(waits[class], at.Sub(tr.startedAt[id]))
		}
		if ended {
			took[class] = append(took[class], end.Sub(tr.startedAt[id]))
			if class == "others" {
				perUser[s.user] = append(perUser[s.user], end.Sub(tr.startedAt[id]))
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
	var othersFailed, othersRefused int64
	for _, u := range ids[1:] {
		if count, ok := failed.Load(u); ok {
			othersFailed += count.(*atomic.Int64).Load()
		}
		othersRefused += keys[u].refused.Load()
	}
	heavyFailed := int64(0)
	if count, ok := failed.Load(heavyUser); ok {
		heavyFailed = count.(*atomic.Int64).Load()
	}

	alone := time.Duration(*rounds+1)*(*latency) + time.Duration(*rounds)*(*toolLatency)
	fmt.Printf("users=%d c=%d heavy=%d calls=%d heavy_calls=%d heavy_limit=%d slots=%d user_slots=%d run_s=%.0f alone_s=%.1f jain_others=%.3f "+
		"heavy_peak_in_flight=%d heavy_refused_429=%d heavy_failed_turns=%d others_refused_429=%d others_failed_turns=%d\n",
		*n, *c, *heavy, *calls, *heavyCalls, *heavyLimit, *slots, max(*userSlots, 0), run.Seconds(), alone.Seconds(), jain,
		keys[heavyUser].peak.Load(), keys[heavyUser].refused.Load(), heavyFailed, othersRefused, othersFailed)
	ms := func(x time.Duration) string { return fmt.Sprintf("%.0f", float64(x)/float64(time.Millisecond)) }
	for _, class := range []string{"others", heavyUser} {
		w, t := waits[class], took[class]
		slices.Sort(w)
		slices.Sort(t)
		fmt.Printf("class=%s turns=%d wait_p50_ms=%s wait_p99_ms=%s took_p50_s=%.1f took_p99_s=%.1f took_max_s=%.1f\n",
			class, len(t), ms(pct(w, .5)), ms(pct(w, .99)), pct(t, .5).Seconds(), pct(t, .99).Seconds(), pct(t, 1).Seconds())
	}
	e.stop()
}
