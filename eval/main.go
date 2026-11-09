// Command eval runs one task through ollupi's agent and exits: one user, one conversation, one turn, no gateway and
// no client. A benchmark harness such as Harbor runs it inside a task's container, where the container is the
// sandbox, so by default commands run with full access; nothing asks for approval. When the model asks the user something,
// eval answers that no one is there and it should decide for itself.
//
// The task comes from the OLLUPI_INSTRUCTION environment variable, a file (-instruction-file) or stdin; the model's
// key from the variable -key-env names. It prints the agent's last message, writes the turn's items as JSON to -out,
// and exits 1 when the turn does not complete.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
	"github.com/0xfakeSpike/ollupi/tools/web"
	"github.com/0xfakeSpike/ollupi/tools/workspace"
)

// version is set at build time, by build.sh, to the commit it was built from.
var version = "dev"

const noOneThere = "No one is available to answer: decide for yourself and carry on with the task."

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	api := flag.String("api", "openai", "the provider's API: openai (chat completions, also for compatible providers), anthropic or gemini")
	baseURL := flag.String("base-url", "", "the provider's base URL (default: the API's own)")
	model := flag.String("model", "", "the model to run, as the provider names it")
	keyEnv := flag.String("key-env", "OLLUPI_API_KEY", "the environment variable holding the provider's key")
	contextWindow := flag.Int("context", 128_000, "the model's context window, in tokens")
	output := flag.Int("output", 8_192, "the most tokens the model writes in one response")
	dir := flag.String("dir", "", "the folder the task is in and commands run in (default: the current one)")
	home := flag.String("home", "", "the folder ollupi keeps its database in (default: a new temporary one)")
	instructionFile := flag.String("instruction-file", "", "read the task from this file (default: $OLLUPI_INSTRUCTION, else stdin)")
	promptFile := flag.String("prompt-file", "", "add this file's text to the agent's prompt")
	mode := flag.String("sandbox", "full-access", "the sandbox commands run in: full-access inside a task's container, "+
		"workspace-write to confine them on macOS")
	out := flag.String("out", "", "write the turn's items to this file as JSON")
	runLimit := flag.Duration("run-limit", 0, "fail the turn after this long (default: the agent's own limit)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *model == "" {
		log.Fatal("eval: -model is required")
	}

	instruction, err := readInstruction(*instructionFile)
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	var prompt string
	if *promptFile != "" {
		raw, err := os.ReadFile(*promptFile)
		if err != nil {
			log.Fatalf("eval: %v", err)
		}
		prompt = string(raw)
	}
	if *dir == "" {
		if *dir, err = os.Getwd(); err != nil {
			log.Fatalf("eval: %v", err)
		}
	}
	if *home == "" {
		if *home, err = os.MkdirTemp("", "ollupi-eval-"); err != nil {
			log.Fatalf("eval: %v", err)
		}
	}

	limits := map[string]llm.Limits{*model: {Context: *contextWindow, Output: *output}}
	key := os.Getenv(*keyEnv)
	var client llm.Client
	switch *api {
	case "openai":
		client = llm.NewOpenAI(*baseURL, key, 4, limits)
	case "anthropic":
		client = llm.NewAnthropic(*baseURL, key, 4, limits)
	case "gemini":
		if client, err = llm.NewGemini(*baseURL, key, 4, limits); err != nil {
			log.Fatalf("eval: %v", err)
		}
	default:
		log.Fatalf("eval: unknown -api %q", *api)
	}

	db, err := repo.Open(*home)
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	defer db.Close()
	runner, err := workspace.New(workspace.Options{Config: workspace.Config{WorkspaceDirs: []string{*dir}, SandboxMode: *mode},
		Agent: workspace.AgentFiles{Home: *home}})
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	defer runner.Close()

	var ag *agent.Agent
	ended := make(chan repo.TurnRow, 1)
	ag = agent.New(agent.Config{
		Name: "ollupi", Prompt: prompt,
		Models:      []llm.Model{{Name: *model, Connection: "model"}},
		Connections: map[string]llm.Client{"model": client},
		Providers:   []tools.Provider{runner, web.New(web.Options{})},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		RunLimit:    *runLimit,
		DB:          db,
		OnEvent: func(e agent.Event) {
			switch e.Kind {
			case agent.EventItemCompleted:
				logItem(e.Item)
			case agent.EventUserInputRequested:
				// Answered outside the event, which the agent's loop is delivering.
				go answer(ag, e.Thread.ID, e.Item)
			case agent.EventTurnCompleted:
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
	th, err := ag.StartThread(repo.DefaultUser, "", "", "eval", permission.Settings{})
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	startedAt := time.Now()
	if _, err := ag.StartTurn(repo.DefaultUser, th.ID, agent.Input{Text: instruction}); err != nil {
		log.Fatalf("eval: %v", err)
	}
	var turn repo.TurnRow
	select {
	case turn = <-ended:
	case <-ctx.Done():
		log.Print("eval: stopped before the turn ended")
		ag.Wait()
		os.Exit(1)
	}
	items, err := db.TurnItems(turn.ID)
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	if *out != "" {
		raw, err := json.MarshalIndent(struct {
			Turn  repo.TurnRow   `json:"turn"`
			Items []repo.ItemRow `json:"items"`
		}{turn, items}, "", "  ")
		if err != nil {
			log.Fatalf("eval: %v", err)
		}
		if err := os.WriteFile(*out, raw, 0o644); err != nil {
			log.Fatalf("eval: %v", err)
		}
	}
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Type == repo.ItemAgentMessage {
			fmt.Println(items[i].Text)
			break
		}
	}
	log.Printf("eval: turn %s after %s, %d items", turn.Status, time.Since(startedAt).Round(time.Second), len(items))
	stop()
	ag.Wait()
	if turn.Status != repo.TurnCompleted {
		log.Printf("eval: %s", cmp.Or(turn.Error, "the turn did not complete"))
		os.Exit(1)
	}
}

func readInstruction(file string) (string, error) {
	var raw []byte
	var err error
	switch {
	case file != "":
		raw, err = os.ReadFile(file)
	case os.Getenv("OLLUPI_INSTRUCTION") != "":
		return os.Getenv("OLLUPI_INSTRUCTION"), nil
	default:
		raw, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", fmt.Errorf("the task is empty")
	}
	return string(raw), nil
}

// logItem writes a line for each tool call and message as it completes, so the harness's log shows the run.
func logItem(it *repo.ItemRow) {
	if it == nil {
		return
	}
	switch it.Type {
	case repo.ItemToolCall:
		log.Printf("tool %s %s: %s", it.Tool, it.Status, clip(it.Arguments, 200))
	case repo.ItemAgentMessage:
		log.Printf("message: %s", clip(it.Text, 200))
	case repo.ItemError:
		log.Printf("error: %s", clip(cmp.Or(it.Error, it.Text), 400))
	}
}

func answer(ag *agent.Agent, threadID string, it *repo.ItemRow) {
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
	if err := ag.AnswerUserInput(repo.DefaultUser, threadID, it.ID, answers); err != nil {
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
