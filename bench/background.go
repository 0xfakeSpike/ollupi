package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/0xfakeSpike/ollupi/host"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

const probeUser = "probe"

func background(args []string) {
	fs := flag.NewFlagSet("background", flag.ExitOnError)
	mode := fs.String("mode", "background", "how each long call runs: background (its own queue) or foreground (inside its turn)")
	long := fs.Int("long", 256, "conversations whose turn makes one long call")
	seconds := fs.Duration("seconds", 30*time.Second, "how long a long call takes")
	nUsers := fs.Int("users", 64, "users the long conversations belong to, in turn")
	slots := fs.Int("slots", 32, "agent slots")
	taskSlots := fs.Int("task-slots", 32, "slots of the background queue")
	userTaskSlots := fs.Int("user-task-slots", 4, "the most background slots one user may hold")
	calls := fs.Int("calls", 64, "model calls the connection and the provider allow in flight")
	latency := fs.Duration("latency", time.Second, "model latency")
	jitter := fs.Duration("jitter", 200*time.Millisecond, "model latency varies by up to this much either way")
	toolLatency := fs.Duration("tool", time.Second, "latency of a probe's tool calls")
	probeEvery := fs.Duration("probe", time.Second, "start a probe conversation this often, by a user of its own, and time its turn")
	fs.Parse(args)
	if *mode != "background" && *mode != "foreground" {
		log.Fatalf("-mode %s: background or foreground", *mode)
	}

	longArgs := fmt.Sprintf(`{"seconds":%g,"background":%t}`, seconds.Seconds(), *mode == "background")
	var mu sync.Mutex
	first := map[string]time.Time{}
	p := &provider{latency: *latency, jitter: *jitter, limit: *calls,
		next: func(ask string, answered int) (string, string) {
			if strings.HasPrefix(ask, "long") {
				if answered == 0 {
					return "bench__work", longArgs
				}
				return "", ""
			}
			if answered < 2 {
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
	http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost = *calls
	dir := tempHome()
	defer os.RemoveAll(dir)
	tr := newTracker()
	e := newEngine(host.Config{
		Home: dir, Dial: fakeDial(fakeConnection(srv.URL, *calls)),
		Tools: []kit.Provider{work{latency: *toolLatency}},
		Slots: *slots, TaskSlots: *taskSlots, UserTaskSlots: *userTaskSlots,
	})
	users := []string{probeUser}
	for i := range *nUsers {
		users = append(users, fmt.Sprintf("user%03d", i))
	}
	for _, u := range users {
		e.addUser(u)
	}
	e.Store.Feed().SubscribeAll(tr.on)
	e.run()
	type conv struct{ user, thread string }
	convs := make([]conv, *long)
	for i := range convs {
		u := users[1+i%*nUsers]
		convs[i] = conv{u, e.newThread(u)}
	}

	var peakRSS atomic.Value
	peakRSS.Store(0.0)
	var peakGoroutines atomic.Int64
	done := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			select {
			case <-done:
				return
			case <-time.After(time.Second):
				if rss := rssMB(); rss > peakRSS.Load().(float64) {
					peakRSS.Store(rss)
				}
				raise(&peakGoroutines, int64(runtime.NumGoroutine()))
			}
		}
	}()
	startAt, busyCPU := time.Now(), cpu()
	longTurns := make([]string, len(convs))
	for i, c := range convs {
		longTurns[i] = tr.start(e, c.user, c.thread, fmt.Sprintf("long#%d", i))
	}
	var probes []string
	probeText := map[string]string{}
	probing := make(chan struct{})
	go func() {
		defer close(probing)
		for k := 0; ; k++ {
			select {
			case <-done:
				return
			case <-time.After(*probeEvery):
				text := fmt.Sprintf("probe#%d", k)
				id := tr.start(e, probeUser, e.newThread(probeUser), text)
				mu.Lock()
				probes = append(probes, id)
				probeText[id] = text
				mu.Unlock()
			}
		}
	}()
	waitFor("the long turns", func() bool { return len(tr.took(longTurns)) == len(longTurns) })
	total := time.Since(startAt)
	close(done)
	<-sampled
	<-probing
	waitFor("the probes", func() bool { return len(tr.took(probes)) == len(probes) })
	busyCPU = cpu() - busyCPU

	mu.Lock()
	tr.mu.Lock()
	var waits []time.Duration
	for _, id := range probes {
		if at, ok := first[probeText[id]]; ok {
			waits = append(waits, at.Sub(tr.startedAt[id]))
		}
	}
	tr.mu.Unlock()
	mu.Unlock()
	slices.Sort(waits)
	longTook, probeTook := tr.took(longTurns), tr.took(probes)
	commands := *slots
	if *mode == "background" {
		commands = min(*taskSlots, *nUsers**userTaskSlots)
	}
	bound := time.Duration((*long+commands-1)/commands) * *seconds
	probeAlone := 3*(*latency) + 2*(*toolLatency)
	ms := func(d time.Duration) string { return fmt.Sprintf("%.0f", float64(d)/float64(time.Millisecond)) }
	fmt.Printf("mode=%s long=%d seconds=%g slots=%d task_slots=%d user_task_slots=%d users=%d total_s=%.1f bound_s=%.1f long_p50_s=%.1f long_p99_s=%.1f "+
		"probes=%d probe_alone_s=%.1f probe_wait_p50_ms=%s probe_wait_p99_ms=%s probe_wait_max_ms=%s probe_took_p50_s=%.1f probe_took_p99_s=%.1f "+
		"failed_turns=%d requests=%d refused_429=%d rss_peak_mb=%.0f goroutines_peak=%d cpu_ms_per_turn=%.1f\n",
		*mode, *long, seconds.Seconds(), *slots, *taskSlots, *userTaskSlots, *nUsers, total.Seconds(), bound.Seconds(),
		pct(longTook, .5).Seconds(), pct(longTook, .99).Seconds(), len(probes), probeAlone.Seconds(),
		ms(pct(waits, .5)), ms(pct(waits, .99)), ms(pct(waits, 1)), pct(probeTook, .5).Seconds(), pct(probeTook, .99).Seconds(),
		tr.failed.Load(), p.requests.Load(), p.refused.Load(), peakRSS.Load().(float64), peakGoroutines.Load(),
		float64(busyCPU)/float64(time.Millisecond)/float64(len(longTook)+len(probeTook)))
	e.stop()
}
