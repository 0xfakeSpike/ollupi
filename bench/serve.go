package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/0xfakeSpike/ollupi/gateway"
	"github.com/0xfakeSpike/ollupi/host"
	"github.com/0xfakeSpike/ollupi/tool/builtin/workspace"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dir := fs.String("dir", "", "the shard's home, kept across restarts")
	url := fs.String("url", "", "the simulated provider")
	calls := fs.Int("calls", 64, "model calls the connection allows in flight")
	slots := fs.Int("slots", 32, "agent slots")
	taskSlots := fs.Int("task-slots", 32, "slots of the background queue")
	userTaskSlots := fs.Int("user-task-slots", 4, "the most background slots one user may hold")
	toolLatency := fs.Duration("tool", time.Second, "latency of bench__work")
	toolLog := fs.String("tool-log", "", "append each bench__work call to this file as it starts")
	addr := fs.String("addr", "127.0.0.1:0", "address to listen on")
	fs.Parse(args)

	tool := work{latency: *toolLatency}
	if *toolLog != "" {
		f, err := os.OpenFile(*toolLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		tool.log = f
	}
	folder := filepath.Join(*dir, "work")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		log.Fatal(err)
	}
	runner, err := workspace.New(workspace.Options{Config: workspace.Config{WorkspaceDirs: []string{folder}, SandboxMode: "full-access"},
		Agent: workspace.AgentFiles{Home: *dir}})
	if err != nil {
		log.Fatal(err)
	}
	defer runner.Close()
	http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost = *calls
	e := newEngine(host.Config{
		Home: *dir, Users: fakeUsers(fakeConnection(*url, *calls)),
		Tools: []kit.Provider{tool, runner},
		Slots: *slots, TaskSlots: *taskSlots, UserTaskSlots: *userTaskSlots,
		Gateway: &gateway.Options{Authenticate: benchUser},
	})
	defer e.stop()
	e.run()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		if err := e.Gateway.Serve(e.ctx, ln); err != nil {
			log.Fatal(err)
		}
	}()
	fmt.Printf("ws://%s\n", ln.Addr())
	answerStats()
}
