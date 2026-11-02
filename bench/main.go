// Command bench runs ollupi's experiments against fake models and tools:
//
//	bench parked  what conversations that are not running cost the turns that are
//	bench burst   how a burst of turns drains through the slots and a provider's concurrency limit
//	bench steady  how many conversations busy at once the process serves before their turns slow down
//	bench crash   whether turns survive the process being killed at random moments
//	bench isolated  the memory of one process for each conversation, the baseline the pool replaces
//	bench resume  how long a conversation idle in the database takes to answer again, and a restart takes
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
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

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
)

const askArgs = `{"questions":[{"id":"pick","header":"Pick","question":"Which one?","options":[{"label":"A"},{"label":"B"}]}]}`

// model answers after a fixed delay: a message containing "ASK" gets a request_user_input call, anything else
// answer, or "ok" when answer is empty. onCall, when set, hears of each request as it arrives.
type model struct {
	latency atomic.Int64
	calls   atomic.Int64
	answer  string
	onCall  atomic.Pointer[func()]
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

// tracker counts parked conversations and times turns from StartTurn to turn_completed.
type tracker struct {
	asked, ended, failed atomic.Int64
	mu                   sync.Mutex
	startedAt            map[string]time.Time
	endedAt              map[string]time.Time
	// done signals a workload conversation's ended turn, so the workload never polls the database it measures.
	done map[string]chan struct{}
}

func (t *tracker) on(e agent.Event) {
	switch e.Kind {
	case agent.EventUserInputRequested:
		t.asked.Add(1)
	case agent.EventTurnCompleted:
		t.ended.Add(1)
		if e.Turn.Status != repo.TurnCompleted {
			t.failed.Add(1)
		}
		t.mu.Lock()
		t.endedAt[e.Turn.ID] = time.Now()
		ch := t.done[e.Thread.ID]
		t.mu.Unlock()
		if ch != nil {
			ch <- struct{}{}
		}
	}
}

// start holds no lock while StartTurn runs: the agent's loop delivers events to on, which takes it.
func (t *tracker) start(ag *agent.Agent, threadID, text string) string {
	at := time.Now()
	turn, err := ag.StartTurn(threadID, agent.Input{Text: text})
	if err != nil {
		log.Fatalf("start turn: %v", err)
	}
	t.mu.Lock()
	t.startedAt[turn.ID] = at
	t.mu.Unlock()
	return turn.ID
}

// took lists how long the given turns ran, shortest first.
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

func rssMB() float64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return -1
	}
	kb, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return kb / 1024
}

// memory is the process's resident memory and the Go heap in use after a collection, in MB.
func memory() (rss, heap float64) {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return rssMB(), float64(m.HeapInuse) / (1 << 20)
}

// dbMB is the size of the database under home, its write-ahead log included, in MB.
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

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	// SetDefault routes the log package through slog at Info, below the level kept; the bench's own errors must show.
	log.SetOutput(os.Stderr)
	experiments := map[string]func([]string){"parked": parked, "burst": burst, "steady": steady, "crash": crash, "crashchild": crashChild, "provider": providerMain, "isolated": isolated, "lone": lone, "resume": resume}
	if len(os.Args) < 2 || experiments[os.Args[1]] == nil {
		log.Fatal("usage: bench parked|burst|steady|crash|isolated|resume [flags]")
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
	m := &model{}
	tr := &tracker{startedAt: map[string]time.Time{}, endedAt: map[string]time.Time{}}
	ag := agent.New(agent.Config{
		Name: "bench", Models: []llm.Model{{Name: "fake", Connection: "fake"}},
		Connections: map[string]llm.Client{"fake": m},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		Slots:       *slots, DB: db, OnEvent: tr.on,
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := ag.Start(ctx); err != nil {
		log.Fatal(err)
	}
	newThread := func() string {
		th, err := ag.StartThread("", "bench", permission.Settings{})
		if err != nil {
			log.Fatal(err)
		}
		return th.ID
	}

	setupAt := time.Now()
	text := "hello"
	if *state == "waiting" {
		text = "ASK"
	}
	for range *parked {
		tr.start(ag, newThread(), text)
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
		ids[i] = newThread()
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
				turnID := tr.start(ag, id, "hello")
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
	cancel()
	ag.Wait()
}
