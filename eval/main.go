package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/ask"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/host"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	_ "golang.org/x/crypto/x509roots/fallback"
)

var version = "dev"

const noOneThere = "No one is available to answer: decide for yourself and carry on with the task."

const user = "eval"

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	out := flag.String("out", "", "keep the turn and its items in this file as JSON, rewritten as each commit changes them")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	instruction := required("OLLUPI_INSTRUCTION")
	model := required("OLLUPI_MODEL")
	key := required("OLLUPI_API_KEY")
	baseURL := os.Getenv("OLLUPI_BASE_URL")
	// The deployment serves one model, the one every turn runs on.
	provider := account.ModelProvider{ID: "api", API: account.ModelAPI(cmp.Or(os.Getenv("OLLUPI_API"), "openai")), BaseURL: baseURL, APIKey: key,
		Models: []account.ProviderModel{{Name: model, Calls: 4, Limits: llm.Limits{Context: number("OLLUPI_CONTEXT", 128_000), Output: number("OLLUPI_OUTPUT", 8_192)}}}}
	if err := provider.Check(); err != nil {
		log.Fatalf("eval: %v", err)
	}
	dir, err := os.Getwd()
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	home, err := os.MkdirTemp("", "ollupi-eval-")
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h, err := host.New(ctx, host.Config{
		Home: home, Name: "ollupi",
		Permissions: policy.Settings{ApprovalPolicy: string(policy.Never)},
		Sandboxes: map[string]sandbox.Provider{"local": sandbox.Local{
			Dir: filepath.Join(home, "users"), Config: sandbox.Config{Mode: string(sandbox.ModeFullAccess)}}},
		Sandbox: "local",
	})
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	defer h.Close()
	if err := h.App.SetCatalog(ctx, []account.ModelProvider{provider}); err != nil {
		log.Fatalf("eval: %v", err)
	}
	if err := h.App.EnsureUser(ctx, user); err != nil {
		log.Fatalf("eval: %v", err)
	}
	project, err := h.App.AddWorkspace(ctx, user, "", dir, "")
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	th, err := h.App.StartThread(ctx, user, project.ID, "", "eval")
	if err != nil {
		log.Fatalf("eval: %v", err)
	}

	messages := strings.Split(instruction, "\n"+nextMessage+"\n")
	ended := make(chan endedTurn, len(messages))
	changed := make(chan struct{}, 1)
	sub := h.Store.Feed().Subscribe(user, func(e store.Event) {
		if e.Change == nil {
			return
		}
		for _, it := range e.Change.Items {
			if it.Status != conversation.Pending && it.Status != conversation.InProgress {
				logItem(it)
			}
			if it.WaitsForInput() {
				go answer(h.App, e.Thread, it)
			}
		}
		if e.Thread != th.ID {
			return
		}
		select {
		case changed <- struct{}{}:
		default:
		}
		for _, t := range e.Change.Turns {
			if t.Ended != nil {
				ended <- endedTurn{t.ID, *t.Ended}
			}
		}
	})
	defer sub.Close()
	running := make(chan error, 1)
	go func() { running <- h.Run(ctx) }()

	startedAt := time.Now()
	var turns []string
	var saving sync.Mutex
	save := func() conversation.View {
		saving.Lock()
		defer saving.Unlock()
		var v conversation.View
		if err := h.Store.Read(context.Background(), func(r store.Reader) (err error) { v, err = r.View(th.ID); return err }); err != nil {
			log.Fatalf("eval: %v", err)
		}
		if *out != "" && len(turns) > 0 {
			if err := writeRun(*out, v, turns); err != nil {
				log.Printf("eval: %v", err)
			}
		}
		return v
	}
	go func() {
		for range changed {
			save()
		}
	}()
	var end conversation.Ending
	for _, message := range messages {
		turn, _, err := h.App.Send(ctx, user, th.ID, app.Input{Text: strings.TrimSpace(message)})
		if err != nil {
			log.Fatalf("eval: %v", err)
		}
		saving.Lock()
		turns = append(turns, turn.ID)
		saving.Unlock()
		end = waitFor(turn.ID, ended, running, save)
		if end.Status != conversation.Completed {
			break
		}
	}
	v := save()
	items := v.ItemsOf(turns[len(turns)-1])
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == conversation.ItemAgentMessage {
			fmt.Println(items[i].Text)
			break
		}
	}
	log.Printf("eval: %d of %d turns, the last %s after %s", len(turns), len(messages), end.Status, time.Since(startedAt).Round(time.Second))
	stop()
	<-running
	if end.Status != conversation.Completed {
		log.Printf("eval: %s", cmp.Or(end.Reason, "the turn did not complete"))
		os.Exit(1)
	}
}

const nextMessage = "=== next message ==="

type endedTurn struct {
	id     string
	ending conversation.Ending
}

func waitFor(turn string, ended <-chan endedTurn, running <-chan error, save func() conversation.View) conversation.Ending {
	for {
		select {
		case e := <-ended:
			if e.id == turn {
				return e.ending
			}
		case err := <-running:
			save()
			log.Printf("eval: stopped before the turn ended: %v", cmp.Or(err, errors.New("interrupted")))
			os.Exit(1)
		}
	}
}

func required(name string) string {
	v := os.Getenv(name)
	if strings.TrimSpace(v) == "" {
		log.Fatalf("eval: set %s", name)
	}
	return v
}

func number(name string, fallback int) int {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Fatalf("eval: %s must be a positive number of tokens, not %q", name, v)
	}
	return n
}

type runTurn struct {
	Status      string             `json:"status"`
	Error       string             `json:"error,omitempty"`
	Model       llm.Model          `json:"model"`
	Usage       conversation.Usage `json:"usage"`
	StartedAt   time.Time          `json:"startedAt"`
	CompletedAt *time.Time         `json:"completedAt,omitempty"`
}

type runItem struct {
	ID           string                     `json:"id"`
	Turn         int                        `json:"turn"`
	Type         conversation.ItemKind      `json:"type"`
	Status       conversation.ItemStatus    `json:"status"`
	Tool         string                     `json:"tool,omitempty"`
	Arguments    string                     `json:"arguments,omitempty"`
	ResponseID   string                     `json:"responseId,omitempty"`
	Text         string                     `json:"text,omitempty"`
	Error        string                     `json:"error,omitempty"`
	Result       *conversation.Result       `json:"result,omitempty"`
	ContextStats *conversation.ContextStats `json:"contextStats,omitempty"`
	StartedAt    time.Time                  `json:"startedAt"`
	CompletedAt  *time.Time                 `json:"completedAt,omitempty"`
}

func writeRun(path string, v conversation.View, turnIDs []string) error {
	first, _ := v.TurnOf(turnIDs[0])
	run := runTurn{Status: "inProgress", Model: first.Model, StartedAt: first.QueuedAt}
	stats := map[string]conversation.ContextStats{}
	for _, r := range v.Responses {
		stats[r.ID] = r.Stats
	}
	items := []runItem{}
	for n, id := range turnIDs {
		t, _ := v.TurnOf(id)
		run.Usage = run.Usage.Plus(t.Usage)
		run.Status, run.Error, run.CompletedAt = "inProgress", "", nil
		if t.Ended != nil {
			at := t.Ended.At
			run.Status, run.Error, run.CompletedAt = string(t.Ended.Status), t.Ended.Reason, &at
		}
		for _, it := range v.ItemsOf(id) {
			ri := runItem{ID: it.ID, Turn: n, Type: it.Kind, Status: it.Status, ResponseID: it.Response, Text: it.Text, Error: it.Error,
				Result: it.Result, ContextStats: it.Stats, StartedAt: it.StartedAt, CompletedAt: it.CompletedAt}
			if it.Call != nil {
				ri.Tool, ri.Arguments = it.Call.Tool, it.Call.Arguments
			}
			if s, ok := stats[it.Response]; ok && ri.ContextStats == nil {
				ri.ContextStats = &s
				delete(stats, it.Response)
			}
			items = append(items, ri)
		}
	}
	raw, err := json.MarshalIndent(struct {
		Turn  runTurn   `json:"turn"`
		Items []runItem `json:"items"`
	}{run, items}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func logItem(it conversation.Item) {
	switch it.Kind {
	case conversation.ItemToolCall:
		log.Printf("tool %s %s: %s", it.Call.Tool, it.Status, clip(it.Call.Arguments, 200))
	case conversation.ItemAgentMessage:
		log.Printf("message: %s", clip(it.Text, 200))
	case conversation.ItemError:
		log.Printf("error: %s", clip(cmp.Or(it.Error, it.Text), 400))
	}
}

func answer(a *app.App, thread string, it conversation.Item) {
	req, err := ask.Decode(it.Input.Request)
	if err != nil {
		log.Printf("eval: the question could not be read: %v", err)
		return
	}
	answers := ask.Answers{}
	for _, q := range req.Questions {
		answers[q.ID] = ask.Answer{Answers: []string{noOneThere}}
	}
	if err := a.Answer(context.Background(), user, thread, it.ID, answers); err != nil {
		log.Printf("eval: the question could not be answered: %v", err)
	}
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
