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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/ask"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/host"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool/builtin/web"
	"github.com/0xfakeSpike/ollupi/tool/builtin/workspace"
	"github.com/0xfakeSpike/ollupi/tool/kit"
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
	limits := map[string]llm.Limits{model: {Context: number("OLLUPI_CONTEXT", 128_000), Output: number("OLLUPI_OUTPUT", 8_192)}}
	var client llm.Client
	var err error
	switch api := cmp.Or(os.Getenv("OLLUPI_API"), "openai"); api {
	case "openai":
		client = llm.NewOpenAI(baseURL, key, 4, limits)
	case "anthropic":
		client = llm.NewAnthropic(baseURL, key, 4, limits)
	case "gemini":
		if client, err = llm.NewGemini(baseURL, key, 4, limits); err != nil {
			log.Fatalf("eval: %v", err)
		}
	default:
		log.Fatalf("eval: unknown OLLUPI_API %q", api)
	}
	dir, err := os.Getwd()
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	home, err := os.MkdirTemp("", "ollupi-eval-")
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	files, err := workspace.New(workspace.Options{
		Config: workspace.Config{WorkspaceDirs: []string{dir}, SandboxMode: string(sandbox.ModeFullAccess)},
		Agent:  workspace.AgentFiles{Home: home},
	})
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	defer files.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h, err := host.New(ctx, host.Config{
		Home: home, Name: "ollupi",
		Models:      []llm.Model{{Name: model, Connection: "model"}},
		Connections: map[string]llm.Client{"model": client},
		Tools:       []kit.Provider{files, web.New()},
		Permissions: policy.Settings{ApprovalPolicy: string(policy.Never)},
		Boxes:       func(string) (sandbox.Box, error) { return sandbox.Here(), nil },
	})
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	defer h.Close()
	if err := h.App.EnsureUser(ctx, user); err != nil {
		log.Fatalf("eval: %v", err)
	}
	th, err := h.App.StartThread(ctx, user, "", "", "eval")
	if err != nil {
		log.Fatalf("eval: %v", err)
	}

	ended := make(chan conversation.Ending, 1)
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
				ended <- *t.Ended
			}
		}
	})
	defer sub.Close()
	running := make(chan error, 1)
	go func() { running <- h.Run(ctx) }()

	startedAt := time.Now()
	turn, _, err := h.App.Send(ctx, user, th.ID, app.Input{Text: instruction})
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	var saving sync.Mutex
	save := func() conversation.View {
		saving.Lock()
		defer saving.Unlock()
		var v conversation.View
		if err := h.Store.Read(context.Background(), func(r store.Reader) (err error) { v, err = r.View(th.ID); return err }); err != nil {
			log.Fatalf("eval: %v", err)
		}
		if *out != "" {
			if err := writeRun(*out, v, turn.ID); err != nil {
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
	select {
	case end = <-ended:
	case err := <-running:
		save()
		log.Printf("eval: stopped before the turn ended: %v", cmp.Or(err, errors.New("interrupted")))
		os.Exit(1)
	}
	v := save()
	items := v.ItemsOf(turn.ID)
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == conversation.ItemAgentMessage {
			fmt.Println(items[i].Text)
			break
		}
	}
	log.Printf("eval: turn %s after %s, %d items", end.Status, time.Since(startedAt).Round(time.Second), len(items))
	stop()
	<-running
	if end.Status != conversation.Completed {
		log.Printf("eval: %s", cmp.Or(end.Reason, "the turn did not complete"))
		os.Exit(1)
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

func writeRun(path string, v conversation.View, turnID string) error {
	t, _ := v.TurnOf(turnID)
	run := runTurn{Status: "inProgress", Model: t.Model, Usage: t.Usage, StartedAt: t.QueuedAt}
	if t.Ended != nil {
		at := t.Ended.At
		run.Status, run.Error, run.CompletedAt = string(t.Ended.Status), t.Ended.Reason, &at
	}
	stats := map[string]conversation.ContextStats{}
	for _, r := range v.Responses {
		stats[r.ID] = r.Stats
	}
	items := []runItem{}
	for _, it := range v.ItemsOf(turnID) {
		ri := runItem{ID: it.ID, Type: it.Kind, Status: it.Status, ResponseID: it.Response, Text: it.Text, Error: it.Error,
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
