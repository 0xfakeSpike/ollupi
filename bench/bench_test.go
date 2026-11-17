package main

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && experiments[os.Args[1]] != nil {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

var fast = []string{"-latency", "20ms", "-jitter", "10ms", "-tool", "10ms"}

func bench(t *testing.T, args ...string) []map[string]string {
	t.Helper()
	if testing.Short() {
		t.Skip("the bench's smoke tests take a while")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("bench %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	var lines []map[string]string
	for s := bufio.NewScanner(&stdout); s.Scan(); {
		line := map[string]string{"": s.Text()}
		for _, field := range strings.Fields(s.Text()) {
			if k, v, ok := strings.Cut(field, "="); ok {
				line[k] = v
			}
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		t.Fatalf("bench %s printed nothing\n%s", strings.Join(args, " "), stderr.String())
	}
	return lines
}

func want(t *testing.T, line map[string]string, pairs ...string) {
	t.Helper()
	for i := 0; i < len(pairs); i += 2 {
		if got := line[pairs[i]]; got != pairs[i+1] {
			t.Errorf("%s=%q, want %q, in: %s", pairs[i], got, pairs[i+1], line[""])
		}
	}
}

func line(t *testing.T, lines []map[string]string, key, value string) map[string]string {
	t.Helper()
	for _, l := range lines {
		if l[key] == value {
			return l
		}
	}
	t.Fatalf("no line says %s=%s", key, value)
	return nil
}

func TestParked(t *testing.T) {
	out := bench(t, "parked", "-n", "200", "-active", "4", "-rounds", "2", "-latency", "20ms")
	want(t, out[0], "n", "200", "turns", "8")
}

func TestIsolated(t *testing.T) {
	out := bench(t, "isolated", "-k", "2")
	if out[0]["rss_mb_min"] == "" || out[0]["rss_mb_min"] == "0.0" {
		t.Errorf("no process measured: %s", out[0][""])
	}
}

func TestSteady(t *testing.T) {
	for name, extra := range map[string][]string{
		"plain":     nil,
		"faults":    {"-fail", "0.2", "-others", "2", "-calls", "6"},
		"lognormal": {"-sigma", "0.6"},
	} {
		t.Run(name, func(t *testing.T) {
			out := bench(t, append(append([]string{"steady", "-c", "8", "-turns", "2"}, fast...), extra...)...)
			want(t, out[0], "turns", "16", "failed_turns", "0")
		})
	}
}

func TestSteadyCommands(t *testing.T) {
	modes := []string{"full-access"}
	if runtime.GOOS == "darwin" {
		modes = append(modes, "workspace-write")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			out := bench(t, append([]string{"steady", "-c", "4", "-turns", "1", "-command", mode}, fast...)...)
			want(t, out[0], "command", mode, "turns", "4", "failed_turns", "0")
		})
	}
}

func TestBurst(t *testing.T) {
	timeline := filepath.Join(t.TempDir(), "burst.csv")
	out := bench(t, append([]string{"burst", "-n", "40", "-slots", "8", "-calls", "4", "-probe", "1s", "-timeline", timeline}, fast...)...)
	want(t, out[0], "n", "40", "refused_429", "0", "failed_turns", "0")
	if body, err := os.ReadFile(timeline); err != nil || !strings.HasPrefix(string(body), "seconds,") {
		t.Errorf("the timeline: %.80q, %v", body, err)
	}
}

func TestResume(t *testing.T) {
	out := bench(t, "resume", "-history", "5,20", "-tries", "3", "-waiting", "50", "-running", "5")
	line(t, out, "history_turns", "5")
	line(t, out, "history_turns", "20")
	want(t, line(t, out, "waiting", "50"), "running", "5")
}

func TestCrash(t *testing.T) {
	for name, extra := range map[string][]string{"restart": nil, "standby": {"-lease", "600ms"}} {
		t.Run(name, func(t *testing.T) {
			out := bench(t, append([]string{"crash", "-n", "20", "-kills", "2", "-min", "300ms", "-max", "600ms",
				"-latency", "50ms", "-tool", "50ms"}, extra...)...)
			want(t, out[0], "open_turns", "0", "open_items", "0", "calls_run_twice", "0", "completed_calls_never_run", "0")
		})
	}
}

func TestMix(t *testing.T) {
	out := bench(t, append([]string{"mix", "-c", "8", "-peak", "20", "-peak-at", "1s", "-d", "3s", "-slots", "16", "-calls", "8"}, fast...)...)
	want(t, out[0], "failed_turns", "0")
	for _, kind := range []string{"user", "sub", "cron"} {
		if l := line(t, out, "kind", kind); l["n"] == "0" {
			t.Errorf("no %s turn ran: %s", kind, l[""])
		}
	}
}

func TestFanout(t *testing.T) {
	out := bench(t, append([]string{"fanout", "-parents", "4", "-n", "4", "-slots", "4", "-calls", "8"}, fast...)...)
	want(t, out[0], "subagents", "16", "turns_ended", "20", "failed_turns", "0")
}

func TestSoak(t *testing.T) {
	samples := filepath.Join(t.TempDir(), "soak.csv")
	bench(t, append([]string{"soak", "-c", "8", "-d", "3s", "-every", "1s", "-csv", samples}, fast...)...)
	body, err := os.ReadFile(samples)
	if err != nil {
		t.Fatal(err)
	}
	if rows := strings.Count(string(body), "\n"); rows < 3 {
		t.Errorf("%d rows of samples:\n%s", rows, body)
	}
}

func TestUsers(t *testing.T) {
	for name, extra := range map[string][]string{
		"shared":  nil,
		"slots":   {"-user-slots", "4"},
		"refused": {"-user-slots", "8", "-heavy-calls", "8", "-heavy-limit", "4"},
	} {
		t.Run(name, func(t *testing.T) {
			out := bench(t, append(append([]string{"users", "-users", "3", "-c", "2", "-heavy", "16", "-calls", "2",
				"-heavy-calls", "4", "-slots", "16", "-d", "3s"}, fast...), extra...)...)
			want(t, out[0], "others_failed_turns", "0", "others_refused_429", "0")
		})
	}
}

func TestConns(t *testing.T) {
	for name, extra := range map[string][]string{"shard": nil, "router": {"-router"}} {
		t.Run(name, func(t *testing.T) {
			out := bench(t, append([]string{"conns", "-n", "10,20", "-idle", "1s", "-wait", "1m", "-storm"}, extra...)...)
			want(t, line(t, out, "connected", "20"), "n", "20")
			want(t, line(t, out, "back", "20"), "all_back", "true")
		})
	}
}
