package main

import (
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

	"github.com/0xfakeSpike/ollupi/host"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

func isolated(args []string) {
	fs := flag.NewFlagSet("isolated", flag.ExitOnError)
	k := fs.Int("k", 20, "processes, one conversation each")
	fs.Parse(args)
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
		dir := tempHome()
		dirs = append(dirs, dir)
		cmd, line := spawn("lone", "-dir", dir)
		cmds = append(cmds, cmd)
		if !strings.HasPrefix(line, "ready") {
			log.Fatalf("a conversation's process did not start: %q", line)
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

func lone(args []string) {
	fs := flag.NewFlagSet("lone", flag.ExitOnError)
	dir := fs.String("dir", "", "home of the store")
	fs.Parse(args)
	tr := newTracker()
	e := newEngine(host.Config{
		Home: *dir, Models: fakeModels,
		Connections: map[string]llm.Client{"fake": &model{}},
	})
	e.Store.Feed().SubscribeAll(tr.on)
	e.run()
	tr.start(e, defaultUser, e.newThread(defaultUser), "ASK")
	waitFor("the question", func() bool { return tr.asked.Load() == 1 })
	runtime.GC()
	fmt.Println("ready")
	time.Sleep(24 * time.Hour)
}
