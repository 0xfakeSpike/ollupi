// Command eval runs one benchmark task through ollupi's agent inside the task's container and exits: one user, one
// conversation, one turn. The container is the sandbox, so commands run with full access and nothing asks for
// approval; a question the model asks is answered that no one is there. It is configured by the environment (see
// eval/README.md), prints the agent's last message, and exits 1 when the turn does not complete.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
	"github.com/0xfakeSpike/ollupi/tools/web"
	"github.com/0xfakeSpike/ollupi/tools/workspace"
	// Some task images ship no CA certificates; with none on the system, TLS falls back to the roots built in here.
	_ "golang.org/x/crypto/x509roots/fallback"
)

// version is set at build time, by build.sh, to the commit it was built from.
var version = "dev"

const noOneThere = "No one is available to answer: decide for yourself and carry on with the task."

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	out := flag.String("out", "", "keep the turn and its items in this file as JSON, rewritten as each item completes")
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
	store, err := db.Open(home)
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	defer store.Close()
	runner, err := workspace.New(workspace.Options{
		Config: workspace.Config{WorkspaceDirs: []string{dir}, SandboxMode: string(sandbox.ModeFullAccess)},
		Agent:  workspace.AgentFiles{Home: home},
	})
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	defer runner.Close()

	var ag *orchestrator.Orchestrator
	ended := make(chan db.TurnRow, 1)
	// changed tells the writer below that the turn has a new item, so that -out holds the run so far even when the
	// harness kills the run at its time limit.
	changed := make(chan struct{}, 1)
	ag = orchestrator.New(orchestrator.Config{
		Name:        "ollupi",
		Models:      []llm.Model{{Name: model, Connection: "model"}},
		Connections: map[string]llm.Client{"model": client},
		Providers:   []tools.Provider{runner, web.New(web.Options{})},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		DB:          store,
		OnEvent: func(e event.Event) {
			switch e.Kind {
			case event.ItemCompleted:
				logItem(e.Item)
				if e.Thread.ParentThreadID == "" {
					select {
					case changed <- struct{}{}:
					default:
					}
				}
			case event.UserInputRequested:
				// Answered outside the event, which the agent's loop is delivering.
				go answer(ag, e.Thread.ID, e.Item)
			case event.TurnCompleted:
				// A subagent's turns end in conversations of their own.
				if e.Thread.ParentThreadID == "" {
					ended <- e.Turn
				}
			}
		},
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := ag.Start(ctx); err != nil {
		log.Fatalf("eval: %v", err)
	}
	th, err := ag.StartThread(db.DefaultUser, "", "", "eval", permission.Settings{})
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	startedAt := time.Now()
	started, err := ag.StartTurn(ctx, db.DefaultUser, th.ID, orchestrator.Input{Text: instruction})
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	var saving sync.Mutex
	save := func() (db.TurnRow, []db.ItemRow) {
		saving.Lock()
		defer saving.Unlock()
		turn, err := store.Turn(started.ID)
		if err != nil {
			log.Fatalf("eval: %v", err)
		}
		items, err := store.TurnItems(started.ID)
		if err != nil {
			log.Fatalf("eval: %v", err)
		}
		if *out != "" {
			if err := writeItems(*out, turn, items); err != nil {
				log.Printf("eval: %v", err)
			}
		}
		return turn, items
	}
	go func() {
		for range changed {
			save()
		}
	}()
	select {
	case <-ended:
	case <-ctx.Done():
		save()
		log.Print("eval: stopped before the turn ended")
		ag.Wait()
		os.Exit(1)
	}
	turn, items := save()
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Type == db.ItemAgentMessage {
			fmt.Println(items[i].Text)
			break
		}
	}
	log.Printf("eval: turn %s after %s, %d items", turn.Status, time.Since(startedAt).Round(time.Second), len(items))
	stop()
	ag.Wait()
	if turn.Status != db.TurnCompleted {
		log.Printf("eval: %s", cmp.Or(turn.Error, "the turn did not complete"))
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

// writeItems replaces the file whole, so a reader never sees half of it.
func writeItems(path string, turn db.TurnRow, items []db.ItemRow) error {
	raw, err := json.MarshalIndent(struct {
		Turn  db.TurnRow   `json:"turn"`
		Items []db.ItemRow `json:"items"`
	}{turn, items}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// logItem writes a line for each tool call and message as it completes, so the harness's log shows the run.
func logItem(it *db.ItemRow) {
	if it == nil {
		return
	}
	switch it.Type {
	case db.ItemToolCall:
		log.Printf("tool %s %s: %s", it.Tool, it.Status, clip(it.Arguments, 200))
	case db.ItemAgentMessage:
		log.Printf("message: %s", clip(it.Text, 200))
	case db.ItemError:
		log.Printf("error: %s", clip(cmp.Or(it.Error, it.Text), 400))
	}
}

func answer(ag *orchestrator.Orchestrator, threadID string, it *db.ItemRow) {
	if it == nil {
		return
	}
	req, err := userinput.Decode(json.RawMessage(it.Arguments))
	if err != nil {
		log.Printf("eval: the question could not be read: %v", err)
		return
	}
	answers := userinput.Answers{}
	for _, q := range req.Questions {
		answers[q.ID] = userinput.Answer{Answers: []string{noOneThere}}
	}
	if err := ag.AnswerUserInput(db.DefaultUser, threadID, it.ID, answers); err != nil {
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
