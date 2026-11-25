package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
)

func TestAWorkspaceHasOneTerminalThatOutlivesWhoWatchesIt(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	f := setup(t)
	w := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "u", "", t.TempDir(), "term"))
	id := must[string](t)(f.a.OpenTerminal(f.ctx, "u", w.ID, 80, 24))
	if again := must[string](t)(f.a.OpenTerminal(f.ctx, "u", w.ID, 100, 30)); again != id {
		t.Errorf("a second open started another terminal: %s, %s", id, again)
	}
	if _, err := f.a.AttachTerminal("v", id); err == nil {
		t.Error("another user attached to the terminal")
	}
	out := must[TerminalOutput](t)(f.a.AttachTerminal("u", id))
	if err := f.a.WriteTerminal("u", id, []byte("echo made-$((40+2))\n")); err != nil {
		t.Fatal(err)
	}
	var seen bytes.Buffer
	until := func(want string) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for !strings.Contains(seen.String(), want) {
			select {
			case chunk, ok := <-out.Next:
				if !ok {
					t.Fatalf("the output ended before %q: %q", want, seen.String())
				}
				seen.Write(chunk)
			case <-deadline:
				t.Fatalf("no %q in %q", want, seen.String())
			}
		}
	}
	until("made-42")
	out.Detach()

	later := must[TerminalOutput](t)(f.a.AttachTerminal("u", id))
	if !strings.Contains(string(later.Before), "made-42") {
		t.Errorf("attaching again does not show what was printed: %q", later.Before)
	}
	must[account.Workspace](t)(f.a.ArchiveWorkspace(f.ctx, "u", w.ID, true))
	select {
	case <-later.Exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the terminal of an archived workspace still runs")
	}
	if _, err := f.a.AttachTerminal("u", id); err == nil {
		t.Error("an ended terminal is still attached to")
	}
	if _, err := f.a.OpenTerminal(f.ctx, "u", w.ID, 80, 24); err == nil {
		t.Error("a terminal opened in an archived workspace")
	}
}
