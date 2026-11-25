package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/host"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
)

const defaultUser = "default"

const askArgs = `{"questions":[{"id":"pick","header":"Pick","question":"Which one?","options":[{"label":"A"},{"label":"B"}]}]}`

type model struct {
	latency atomic.Int64
	calls   atomic.Int64
	answer  string
	onCall  atomic.Pointer[func()]
}

func here(string) (sandbox.Box, error) { return sandbox.Here() }

func tempHome() string {
	dir, err := os.MkdirTemp("", "ollupi-bench-")
	if err != nil {
		log.Fatal(err)
	}
	return dir
}

func spawn(args ...string) (*exec.Cmd, string) {
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		cmd.Process.Kill()
		log.Fatalf("%s did not start: %v", args[0], err)
	}
	return cmd, strings.TrimSpace(line)
}

// fakeProvider is the model the bench deployment serves; the engine's Dial
// answers it with the bench's client.
var fakeProvider = account.ModelProvider{ID: "bench", API: account.OpenAI,
	Models: []account.ProviderModel{{Name: "fake", Limits: llm.Limits{Context: 200_000, Output: 32_000}}}}

func fakeDial(client llm.Client) host.Dial {
	return func(account.ModelProvider, account.ProviderModel) (llm.Client, error) { return client, nil }
}

// addUser makes a user, as a sign-up would.
func (e *engine) addUser(user string) {
	if err := e.App.EnsureUser(e.ctx, user); err != nil {
		log.Fatal(err)
	}
}

func fakeConnection(url string, calls int) llm.Client {
	return llm.NewOpenAI(url, "", calls, map[string]llm.Limits{"fake": {Context: 200_000, Output: 32_000}})
}

func newTracker() *tracker {
	return &tracker{startedAt: map[string]time.Time{}, endedAt: map[string]time.Time{}, done: map[string]chan struct{}{}}
}

type engine struct {
	*host.Host
	ctx    context.Context
	cancel context.CancelFunc
	done   chan error
}

func newEngine(cfg host.Config) *engine {
	cfg.Name, cfg.Boxes = "bench", here
	cfg.Permissions = policy.Settings{ApprovalPolicy: string(policy.Never)}
	ctx, cancel := context.WithCancel(context.Background())
	h, err := host.New(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := h.App.SetCatalog(ctx, []account.ModelProvider{fakeProvider}); err != nil {
		log.Fatal(err)
	}
	e := &engine{Host: h, ctx: ctx, cancel: cancel, done: make(chan error, 1)}
	e.addUser(defaultUser)
	return e
}

func (e *engine) run() {
	go func() { e.done <- e.Run(e.ctx) }()
}

func (e *engine) newThread(user string) string {
	th, err := e.App.StartThread(context.Background(), user, "", "", "bench")
	if err != nil {
		log.Fatal(err)
	}
	return th.ID
}

func (e *engine) stop() {
	e.cancel()
	if err := <-e.done; err != nil {
		log.Printf("the host stopped with %v", err)
	}
	if err := e.Close(); err != nil {
		log.Printf("close the host: %v", err)
	}
}

func (m *model) Chat(ctx context.Context, req llm.ChatRequest, _ func(llm.Delta)) (llm.ChatResponse, error) {
	n := m.calls.Add(1)
	if f := m.onCall.Load(); f != nil {
		(*f)()
	}
	select {
	case <-time.After(time.Duration(m.latency.Load())):
	case <-ctx.Done():
		return llm.ChatResponse{}, ctx.Err()
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role == llm.RoleUser && strings.Contains(last.Content, "ASK") {
		return llm.ChatResponse{FinishReason: "tool_calls", Message: llm.ChatMessage{Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{ID: "call_" + strconv.FormatInt(n, 10), Name: "request_user_input", Arguments: askArgs}}}}, nil
	}
	answer := m.answer
	if answer == "" {
		answer = "ok"
	}
	return llm.ChatResponse{FinishReason: "stop", Message: llm.ChatMessage{Role: llm.RoleAssistant, Content: answer}}, nil
}

func (m *model) Limits(string) (llm.Limits, error) {
	return llm.Limits{Context: 200_000, Output: 32_000}, nil
}

type tracker struct {
	asked, ended, failed atomic.Int64
	mu                   sync.Mutex
	startedAt            map[string]time.Time
	endedAt              map[string]time.Time
	done                 map[string]chan struct{}
}

func (t *tracker) on(e store.Event) {
	if e.Change == nil {
		return
	}
	for _, entry := range e.Change.Entries {
		switch body := entry.Body.(type) {
		case conversation.InputAsked:
			t.asked.Add(1)
		case conversation.TurnEnded:
			t.ended.Add(1)
			if body.Status != conversation.Completed {
				t.failed.Add(1)
			}
			t.mu.Lock()
			t.endedAt[entry.Turn] = time.Now()
			ch := t.done[e.Thread]
			t.mu.Unlock()
			if ch != nil {
				ch <- struct{}{}
			}
		}
	}
}

func (t *tracker) start(e *engine, user, threadID, text string) string {
	at := time.Now()
	turn, _, err := e.App.Send(context.Background(), user, threadID, app.Input{Text: text})
	if err != nil {
		log.Fatalf("start turn: %v", err)
	}
	t.mu.Lock()
	t.startedAt[turn.ID] = at
	t.mu.Unlock()
	return turn.ID
}

func (t *tracker) took(turnIDs []string) []time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []time.Duration
	for _, id := range turnIDs {
		if end, ok := t.endedAt[id]; ok {
			out = append(out, end.Sub(t.startedAt[id]))
		}
	}
	slices.Sort(out)
	return out
}

func waitFor(what string, done func() bool) {
	deadline := time.Now().Add(time.Hour)
	for !done() {
		if time.Now().After(deadline) {
			log.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func cpu() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func childCPU() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_CHILDREN, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func rssMB() float64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return -1
	}
	kb, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return kb / 1024
}

func memory() (rss, heap float64) {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return rssMB(), float64(m.HeapInuse) / (1 << 20)
}

func dbMB(home string) float64 {
	entries, _ := os.ReadDir(filepath.Join(home, "data"))
	var size int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			size += info.Size()
		}
	}
	return float64(size) / (1 << 20)
}

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	return d[min(len(d)-1, int(float64(len(d))*p))]
}

var experiments = map[string]func([]string){"parked": parked, "burst": burst, "steady": steady, "crash": crash, "crashchild": crashChild, "provider": providerMain, "isolated": isolated, "lone": lone, "resume": resume, "mix": mix, "fanout": fanout, "soak": soak, "users": users, "background": background, "serve": serve, "shards": shards, "conns": conns, "connsshard": connsShard, "connsrouter": connsRouter}

func main() {
	if len(os.Args) < 2 || experiments[os.Args[1]] == nil {
		log.Fatal("usage: bench parked|burst|steady|crash|isolated|resume|mix|fanout|soak|users|background|serve|shards|conns [flags]")
	}
	experiments[os.Args[1]](os.Args[2:])
}

func parked(args []string) {
	fs := flag.NewFlagSet("parked", flag.ExitOnError)
	parked := fs.Int("n", 1000, "conversations parked beside the workload")
	state := fs.String("state", "waiting", "how parked conversations sit: waiting (on the user) or idle (turn ended)")
	active := fs.Int("active", 16, "conversations running the workload at once")
	rounds := fs.Int("rounds", 10, "turns each active conversation runs, one after another")
	latency := fs.Duration("latency", 100*time.Millisecond, "model latency during the workload")
	slots := fs.Int("slots", 32, "agent slots")
	profile := fs.String("cpuprofile", "", "write a CPU profile of the workload to this file")
	fs.Parse(args)

	dir := tempHome()
	defer os.RemoveAll(dir)
	m := &model{}
	tr := newTracker()
	e := newEngine(host.Config{
		Home: dir, Dial: fakeDial(m), Slots: *slots,
	})
	e.Store.Feed().SubscribeAll(tr.on)
	e.run()

	setupAt := time.Now()
	text := "hello"
	if *state == "waiting" {
		text = "ASK"
	}
	for range *parked {
		tr.start(e, defaultUser, e.newThread(defaultUser), text)
	}
	waitFor("parked conversations", func() bool {
		if *state == "waiting" {
			return tr.asked.Load() >= int64(*parked)
		}
		return tr.ended.Load() >= int64(*parked)
	})
	setup := time.Since(setupAt)

	idleCPU := cpu()
	time.Sleep(3 * time.Second)
	idleCPU = cpu() - idleCPU
	rssParked, heapParked := memory()

	m.latency.Store(int64(*latency))
	m.calls.Store(0)
	ids := make([]string, *active)
	tr.mu.Lock()
	tr.done = map[string]chan struct{}{}
	for i := range ids {
		ids[i] = e.newThread(defaultUser)
		tr.done[ids[i]] = make(chan struct{}, 1)
	}
	tr.mu.Unlock()
	if *profile != "" {
		f, err := os.Create(*profile)
		if err != nil {
			log.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal(err)
		}
	}
	before, busyCPU := tr.ended.Load(), cpu()
	runAt := time.Now()
	var wg sync.WaitGroup
	var idsMu sync.Mutex
	var workload []string
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range *rounds {
				turnID := tr.start(e, defaultUser, id, "hello")
				idsMu.Lock()
				workload = append(workload, turnID)
				idsMu.Unlock()
				select {
				case <-tr.done[id]:
				case <-time.After(time.Hour):
					log.Fatal("timed out waiting for a workload turn")
				}
			}
		}()
	}
	wg.Wait()
	run := time.Since(runAt)
	pprof.StopCPUProfile()
	busyCPU = cpu() - busyCPU
	turns := tr.ended.Load() - before

	took := tr.took(workload)
	ms := func(d time.Duration) string { return fmt.Sprintf("%.0f", float64(d)/float64(time.Millisecond)) }
	fmt.Printf("state=%s n=%d setup_s=%.1f idle_cpu_ms_per_s=%.1f rss_parked_mb=%.1f heap_parked_mb=%.1f db_mb=%.1f turns=%d run_s=%.2f turns_per_s=%.1f cpu_ms_per_turn=%.1f p50_ms=%s p95_ms=%s p99_ms=%s overhead_p50_ms=%s\n",
		*state, *parked, setup.Seconds(), float64(idleCPU)/float64(time.Millisecond)/3, rssParked, heapParked, dbMB(dir),
		turns, run.Seconds(), float64(turns)/run.Seconds(), float64(busyCPU)/float64(time.Millisecond)/float64(turns),
		ms(pct(took, .5)), ms(pct(took, .95)), ms(pct(took, .99)), ms(pct(took, .5)-*latency))
	e.stop()
}
