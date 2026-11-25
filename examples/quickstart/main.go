package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
	"github.com/0xfakeSpike/ollupi/tool/builtin/workspace"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	_ "golang.org/x/crypto/x509roots/fallback"
)

func main() {
	home := flag.String("home", ".ollupi-quickstart", "where to keep the SQLite database")
	flag.Parse()

	model := required("OLLUPI_MODEL")
	key := required("OLLUPI_API_KEY")
	baseURL := os.Getenv("OLLUPI_BASE_URL")
	// The deployment serves one model, the one every turn runs on.
	provider := account.ModelProvider{ID: "api", API: account.ModelAPI(cmp.Or(os.Getenv("OLLUPI_API"), "openai")), BaseURL: baseURL, APIKey: key,
		Models: []account.ProviderModel{{Name: model, Calls: 4, Limits: llm.Limits{
			Context: intEnv("OLLUPI_CONTEXT", 128_000),
			Output:  intEnv("OLLUPI_OUTPUT", 8_192),
		}}}}
	if err := provider.Check(); err != nil {
		log.Fatalf("quickstart: %v", err)
	}

	homeDir, err := filepath.Abs(*home)
	if err != nil {
		log.Fatalf("quickstart: resolve home: %v", err)
	}
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		log.Fatalf("quickstart: create home: %v", err)
	}
	workDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("quickstart: current directory: %v", err)
	}
	files, err := workspace.New(workspace.Options{
		Config: workspace.Config{
			WorkspaceDirs: []string{workDir},
			SandboxMode:   cmp.Or(os.Getenv("OLLUPI_SANDBOX"), string(sandbox.ModeWorkspaceWrite)),
		},
		Agent: workspace.AgentFiles{Home: homeDir},
	})
	if err != nil {
		log.Fatalf("quickstart: workspace tools: %v", err)
	}
	defer files.Close()

	const user = "demo"
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h, err := host.New(ctx, host.Config{
		Home: homeDir, Name: "ollupi-quickstart",
		Tools:       []kit.Provider{files},
		Permissions: policy.Settings{ApprovalPolicy: string(policy.OnRequest)},
		Boxes:       func(string) (sandbox.Box, error) { return sandbox.Here() },
	})
	if err != nil {
		log.Fatalf("quickstart: host: %v", err)
	}
	defer h.Close()
	// The catalog is the deployment's, kept in the store: set once, it
	// stays for the next start.
	if err := h.App.SetCatalog(ctx, []account.ModelProvider{provider}); err != nil {
		log.Fatalf("quickstart: models: %v", err)
	}

	if err := h.App.EnsureUser(ctx, user); err != nil {
		log.Fatalf("quickstart: user: %v", err)
	}
	thread, err := h.App.StartThread(ctx, user, "", "", "quickstart")
	if err != nil {
		log.Fatalf("quickstart: thread: %v", err)
	}

	ended := make(chan conversation.Ending, 1)
	sub := h.Store.Feed().Subscribe(user, func(e store.Event) {
		if e.Change == nil {
			return
		}
		for _, item := range e.Change.Items {
			switch {
			case item.WaitsForApproval():
				approval := item.Approval.Approval
				go func() {
					if err := h.App.Decide(context.Background(), user, e.Thread, approval, conversation.Decline); err != nil {
						log.Printf("quickstart: decline approval: %v", err)
					}
				}()
			case item.WaitsForInput():
				itemID := item.ID
				go func() {
					if err := h.App.Answer(context.Background(), user, e.Thread, itemID, nil); err != nil {
						log.Printf("quickstart: skip question: %v", err)
					}
				}()
			}
		}
		if e.Thread != thread.ID {
			return
		}
		for _, turn := range e.Change.Turns {
			if turn.Ended != nil {
				select {
				case ended <- *turn.Ended:
				default:
				}
			}
		}
	})
	defer sub.Close()

	running := make(chan error, 1)
	go func() { running <- h.Run(ctx) }()

	prompt := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if prompt == "" {
		prompt = "In two sentences, explain what this Go repository does. Do not modify any files."
	}
	turn, _, err := h.App.Send(ctx, user, thread.ID, app.Input{Text: prompt})
	if err != nil {
		log.Fatalf("quickstart: send: %v", err)
	}

	select {
	case ending := <-ended:
		if ending.Status != conversation.Completed {
			log.Fatalf("quickstart: turn ended as %s: %s", ending.Status, ending.Reason)
		}
	case err := <-running:
		log.Fatalf("quickstart: host stopped: %v", err)
	case <-time.After(10 * time.Minute):
		log.Fatal("quickstart: the turn did not finish within 10 minutes")
	}

	var view conversation.View
	if err := h.Store.Read(ctx, func(r store.Reader) (err error) {
		view, err = r.View(thread.ID)
		return err
	}); err != nil {
		log.Fatalf("quickstart: read thread: %v", err)
	}
	for _, item := range view.ItemsOf(turn.ID) {
		if item.Kind == conversation.ItemAgentMessage {
			fmt.Println(item.Text)
		}
	}

	stop()
	if err := <-running; err != nil {
		log.Fatalf("quickstart: host stopped: %v", err)
	}
}

func required(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		log.Fatalf("quickstart: set %s", name)
	}
	return value
}

func intEnv(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		log.Fatalf("quickstart: %s must be a positive integer", name)
	}
	return n
}
