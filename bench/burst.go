package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

// provider is a fake OpenAI-compatible endpoint. A request takes latency plus or minus jitter and is answered with
// the call next names, or with the turn's answer when it names none; a request beyond limit in flight is refused
// with 429, as an account's concurrency limit would refuse it.
type provider struct {
	latency, jitter time.Duration
	// sigma, when above zero, draws the latency from a log-normal distribution with latency as its median, the long
	// tail real providers show, in place of the even spread of jitter.
	sigma float64
	limit int
	// others, when above zero, is the most requests other clients of the same account keep in flight; how many they
	// hold changes every second, and they count against limit as this agent's requests do.
	others int
	// fail is the share of requests answered at once with a 500.
	fail float64
	// fanout is how many calls a response carries when it calls a tool, all of them the same call; one when zero.
	fanout int
	// next names the call that answers a request made after the model answered the user's message ask so many times.
	next func(ask string, answered int) (name, arguments string)
	// arrived, when set, hears the ask of each request as it arrives.
	arrived func(ask string)
	// hold, while open, keeps every request waiting, so a backlog builds before any turn can finish.
	hold                                      chan struct{}
	inFlight, peak, requests, refused, failed atomic.Int64
}

func (p *provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/stats" {
		fmt.Fprintf(w, "%d %d %d %d", p.peak.Load(), p.refused.Load(), p.requests.Load(), p.failed.Load())
		return
	}
	n := p.inFlight.Add(1)
	defer p.inFlight.Add(-1)
	p.requests.Add(1)
	for old := p.peak.Load(); n > old && !p.peak.CompareAndSwap(old, n); old = p.peak.Load() {
	}
	others := 0
	if p.others > 0 {
		// Seeded by the second, so every request in that second sees the same load.
		others = rand.New(rand.NewPCG(uint64(time.Now().Unix()), 0)).IntN(p.others + 1)
	}
	if n+int64(others) > int64(p.limit) {
		p.refused.Add(1)
		http.Error(w, `{"error":{"message":"too many requests in flight","type":"rate_limit"}}`, http.StatusTooManyRequests)
		return
	}
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	answered, ask := 0, ""
	for i := len(body.Messages) - 1; i >= 0; i-- {
		if body.Messages[i].Role == "user" {
			_ = json.Unmarshal(body.Messages[i].Content, &ask)
			ask = ask[strings.LastIndex(ask, "\n")+1:] // after the "[sent …]" line the context adds
			break
		}
		if body.Messages[i].Role == "assistant" {
			answered++
		}
	}
	if p.arrived != nil {
		p.arrived(ask)
	}
	if rand.Float64() < p.fail {
		p.failed.Add(1)
		http.Error(w, `{"error":{"message":"internal error","type":"server_error"}}`, http.StatusInternalServerError)
		return
	}
	name, arguments := p.next(ask, answered)
	if p.hold != nil {
		<-p.hold
	}
	delay := p.latency + time.Duration((rand.Float64()*2-1)*float64(p.jitter))
	if p.sigma > 0 {
		delay = time.Duration(float64(p.latency) * math.Exp(p.sigma*rand.NormFloat64()))
	}
	time.Sleep(delay)

	w.Header().Set("Content-Type", "text/event-stream")
	send := func(delta map[string]any, finish any) {
		raw, _ := json.Marshal(map[string]any{"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "fake",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\n", raw)
	}
	if name != "" {
		var calls []any
		for i := range max(p.fanout, 1) {
			calls = append(calls, map[string]any{"index": i, "id": fmt.Sprintf("call_%d_%d", p.requests.Load(), i),
				"type": "function", "function": map[string]any{"name": name, "arguments": arguments}})
		}
		send(map[string]any{"tool_calls": calls}, nil)
		send(map[string]any{}, "tool_calls")
	} else {
		send(map[string]any{"content": "done"}, nil)
		send(map[string]any{}, "stop")
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// work is a tool that takes latency, as a command or a fetch would; it holds the turn's slot but no model call.
// With a log it first writes down the token it was called with, so a call that runs twice shows.
type work struct {
	latency time.Duration
	log     *os.File
}

func (work) Name() (string, string) { return "bench", "Bench tools." }

func (work) ToolList(context.Context) ([]*tools.Spec, error) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"token": map[string]any{"type": "string"}}, "additionalProperties": false}
	return []*tools.Spec{{Name: "bench__work", Description: "Does one unit of work.", InputSchema: schema,
		Annotations: &tools.Annotations{ReadOnlyHint: true}}}, nil
}

func (w work) Invoke(ctx context.Context, _ string, args map[string]any) (tools.Result, error) {
	if w.log != nil {
		if _, err := fmt.Fprintln(w.log, args["token"]); err != nil {
			return tools.Result{}, err
		}
	}
	select {
	case <-time.After(w.latency):
		return tools.Result{Output: "ok"}, nil
	case <-ctx.Done():
		return tools.Result{}, ctx.Err()
	}
}

func burst(args []string) {
	fs := flag.NewFlagSet("burst", flag.ExitOnError)
	n := fs.Int("n", 1000, "turns submitted at once, each in its own conversation")
	slots := fs.Int("slots", 64, "agent slots")
	calls := fs.Int("calls", 16, "model calls the connection allows in flight")
	limit := fs.Int("limit", 0, "requests the provider allows in flight before answering 429 (default: calls)")
	latency := fs.Duration("latency", 300*time.Millisecond, "model latency")
	jitter := fs.Duration("jitter", 100*time.Millisecond, "model latency varies by up to this much either way")
	sigma := fs.Float64("sigma", 0, "draw model latency log-normally around -latency with this sigma, in place of -jitter")
	others := fs.Int("others", 0, "the most requests other clients of the account hold in flight against -limit")
	fail := fs.Float64("fail", 0, "share of model requests answered with a 500")
	toolLatency := fs.Duration("tool", 200*time.Millisecond, "tool latency")
	rounds := fs.Int("rounds", 2, "tool calls in each turn, each followed by a model call")
	probeEvery := fs.Duration("probe", 5*time.Second, "start a new conversation this often during the burst and time its turn")
	profile := fs.String("cpuprofile", "", "write a CPU profile of the burst to this file")
	hold := fs.Bool("hold", false, "answer no model request until every turn is submitted, and time the burst from then")
	timeline := fs.String("timeline", "", "write the seconds into the burst, the requests in flight and the turns ended, every 250 ms, to this CSV file")
	fs.Parse(args)
	if *limit == 0 {
		*limit = *calls
	}

	p := &provider{latency: *latency, jitter: *jitter, sigma: *sigma, limit: *limit, others: *others, fail: *fail, next: func(_ string, answered int) (string, string) {
		if answered < *rounds {
			return "bench__work", "{}"
		}
		return "", ""
	}}
	if *hold {
		p.hold = make(chan struct{})
	}
	srv := httptest.NewServer(p)
	defer srv.Close()
	// The client keeps two idle connections a host by default; over plain HTTP/1.1 to the fake provider, thousands of
	// calls in flight would then open and close a connection each, as a real provider's HTTP/2 connection does not.
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
	tr := &tracker{startedAt: map[string]time.Time{}, endedAt: map[string]time.Time{}}
	ag := agent.New(agent.Config{
		Name: "bench", Models: []llm.Model{{Name: "fake", Connection: "fake"}},
		Connections: map[string]llm.Client{"fake": llm.NewOpenAI(srv.URL, "", *calls, map[string]llm.Limits{"fake": {Context: 200_000, Output: 32_000}})},
		Providers:   []tools.Provider{work{latency: *toolLatency}},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		Slots:       *slots, DB: db, OnEvent: tr.on,
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := ag.Start(ctx); err != nil {
		log.Fatal(err)
	}
	newThread := func() string {
		th, err := ag.StartThread(repo.DefaultUser, "", "", "bench", permission.Settings{})
		if err != nil {
			log.Fatal(err)
		}
		return th.ID
	}
	threads := make([]string, *n)
	for i := range threads {
		threads[i] = newThread()
	}

	if *profile != "" {
		f, err := os.Create(*profile)
		if err != nil {
			log.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal(err)
		}
	}
	var peakRSS atomic.Value
	peakRSS.Store(0.0)
	var samples []string
	var sampleAt atomic.Value
	sampleAt.Store(time.Now())
	done := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			select {
			case <-done:
				return
			case <-time.After(250 * time.Millisecond):
				if rss := rssMB(); rss > peakRSS.Load().(float64) {
					peakRSS.Store(rss)
				}
				samples = append(samples, fmt.Sprintf("%.2f,%d,%d", time.Since(sampleAt.Load().(time.Time)).Seconds(), p.inFlight.Load(), tr.ended.Load()))
			}
		}
	}()
	busyCPU, startAt := cpu(), time.Now()
	sampleAt.Store(startAt)
	turns := make([]string, 0, *n)
	for _, id := range threads {
		turns = append(turns, tr.start(ag, id, "work"))
	}
	submit := time.Since(startAt)
	if *hold {
		startAt = time.Now()
		sampleAt.Store(startAt)
		close(p.hold)
	}

	var probes []string
	var probesMu sync.Mutex
	probing := make(chan struct{})
	go func() {
		defer close(probing)
		for {
			select {
			case <-done:
				return
			case <-time.After(*probeEvery):
				id := tr.start(ag, newThread(), "work")
				probesMu.Lock()
				probes = append(probes, id)
				probesMu.Unlock()
			}
		}
	}()
	waitFor("the burst", func() bool { return len(tr.took(turns)) == len(turns) })
	total := time.Since(startAt)
	close(done)
	<-sampled
	if *timeline != "" {
		if err := os.WriteFile(*timeline, []byte("seconds,in_flight,turns_ended\n"+strings.Join(samples, "\n")+"\n"), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	<-probing
	probesMu.Lock()
	waitFor("the probes", func() bool { return len(tr.took(probes)) == len(probes) })
	probesMu.Unlock()
	busyCPU = cpu() - busyCPU
	pprof.StopCPUProfile()

	took := tr.took(turns)
	perTurn := time.Duration(*rounds+1)*(*latency) + time.Duration(*rounds)*(*toolLatency)
	bound := max(time.Duration(*n)*time.Duration(*rounds+1)*(*latency)/time.Duration(min(*calls, *limit)),
		time.Duration(*n)*perTurn/time.Duration(*slots))
	tr.mu.Lock()
	var ends []time.Time
	for _, id := range turns {
		ends = append(ends, tr.endedAt[id])
	}
	tr.mu.Unlock()
	rates := perSecond(ends)
	// Each probe as when it started into the burst, then how long its turn took.
	var probeTook []string
	tr.mu.Lock()
	for _, id := range probes {
		probeTook = append(probeTook, fmt.Sprintf("%.0f:%.1f", tr.startedAt[id].Sub(startAt).Seconds(), tr.endedAt[id].Sub(tr.startedAt[id]).Seconds()))
	}
	tr.mu.Unlock()
	fmt.Printf("n=%d slots=%d calls=%d limit=%d total_s=%.1f bound_s=%.1f efficiency=%.2f submit_s=%.2f turns_per_s=%.1f "+
		"p50_s=%.1f p95_s=%.1f p99_s=%.1f per_s_p10=%d per_s_p50=%d per_s_p90=%d peak_in_flight=%d requests=%d refused_429=%d failed_500=%d "+
		"failed_turns=%d rss_peak_mb=%.0f cpu_ms_per_turn=%.2f probes_at_s:took_s=[%s]\n",
		*n, *slots, *calls, *limit, total.Seconds(), bound.Seconds(), bound.Seconds()/total.Seconds(), submit.Seconds(),
		float64(*n)/total.Seconds(), pct(took, .5).Seconds(), pct(took, .95).Seconds(), pct(took, .99).Seconds(),
		pctInt(rates, .1), pctInt(rates, .5), pctInt(rates, .9), p.peak.Load(), p.requests.Load(), p.refused.Load(), p.failed.Load(),
		tr.failed.Load(), peakRSS.Load().(float64), float64(busyCPU)/float64(time.Millisecond)/float64(*n), strings.Join(probeTook, " "))
	cancel()
	ag.Wait()
}

// perSecond counts the turns ending in each whole second from the first ending to the last, so a stall shows as a low count.
func perSecond(ends []time.Time) []int {
	if len(ends) == 0 {
		return nil
	}
	first, last := slices.MinFunc(ends, time.Time.Compare), slices.MaxFunc(ends, time.Time.Compare)
	counts := make([]int, int(last.Sub(first)/time.Second)+1)
	for _, e := range ends {
		counts[int(e.Sub(first)/time.Second)]++
	}
	slices.Sort(counts)
	return counts
}

func pctInt(v []int, p float64) int {
	if len(v) == 0 {
		return 0
	}
	return v[min(len(v)-1, int(float64(len(v))*p))]
}
