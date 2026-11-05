package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
)

// isolated is the baseline the pool is measured against: one process for each conversation, each waiting on its
// user. It reports the resident memory of each such process; containers would add their own on top.
func isolated(args []string) {
	fs := flag.NewFlagSet("isolated", flag.ExitOnError)
	k := fs.Int("k", 20, "processes, one conversation each")
	fs.Parse(args)
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	var cmds []*exec.Cmd
	var dirs []string
	defer func() {
		for _, c := range cmds {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
		for _, d := range dirs {
			os.RemoveAll(d)
		}
	}()
	for range *k {
		dir, err := os.MkdirTemp("", "ollupi-bench-")
		if err != nil {
			log.Fatal(err)
		}
		dirs = append(dirs, dir)
		cmd := exec.Command(exe, "lone", "-dir", dir)
		cmd.Stderr = os.Stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			log.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			log.Fatal(err)
		}
		cmds = append(cmds, cmd)
		if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || !strings.HasPrefix(line, "ready") {
			log.Fatalf("a conversation's process did not start: %q %v", line, err)
		}
	}
	time.Sleep(3 * time.Second)
	var rss []float64
	for _, c := range cmds {
		out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(c.Process.Pid)).Output()
		if err != nil {
			log.Fatal(err)
		}
		kb, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
		rss = append(rss, kb/1024)
	}
	slices.Sort(rss)
	var sum float64
	for _, r := range rss {
		sum += r
	}
	fmt.Printf("k=%d rss_mb_mean=%.1f rss_mb_min=%.1f rss_mb_median=%.1f rss_mb_max=%.1f\n",
		*k, sum/float64(len(rss)), rss[0], rss[len(rss)/2], rss[len(rss)-1])
}

// lone is one conversation's process for isolated: it opens its own database, asks its user a question, and waits.
func lone(args []string) {
	fs := flag.NewFlagSet("lone", flag.ExitOnError)
	dir := fs.String("dir", "", "home of the database")
	fs.Parse(args)
	db, err := repo.Open(*dir)
	if err != nil {
		log.Fatal(err)
	}
	tr := &tracker{startedAt: map[string]time.Time{}, endedAt: map[string]time.Time{}}
	ag := agent.New(agent.Config{
		Name: "bench", Models: []llm.Model{{Name: "fake", Connection: "fake"}},
		Connections: map[string]llm.Client{"fake": &model{}},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		DB:          db, OnEvent: tr.on,
	})
	if err := ag.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	th, err := ag.StartThread(repo.DefaultUser, "", "bench", permission.Settings{})
	if err != nil {
		log.Fatal(err)
	}
	tr.start(ag, th.ID, "ASK")
	waitFor("the question", func() bool { return tr.asked.Load() == 1 })
	runtime.GC()
	fmt.Println("ready")
	time.Sleep(24 * time.Hour)
}
