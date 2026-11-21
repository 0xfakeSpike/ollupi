package sandbox

import (
	"bufio"
	"slices"
	"strings"
	"testing"
)

func TestTheProfileLeavesGitAlone(t *testing.T) {
	profile, _, err := seatbeltProfile(policy{Mode: ModeWorkspaceWrite, WritableRoots: []string{"/p"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(profile, ".git") {
		t.Errorf("the profile still closes .git:\n%s", profile)
	}
	if !strings.Contains(profile, "file-write") {
		t.Error("the profile lost its write rules")
	}
}

func TestTheProfileKeepsSettingsFilesClosed(t *testing.T) {
	profile, _, err := seatbeltProfile(policy{Mode: ModeWorkspaceWrite, WritableRoots: []string{"/p"},
		ProtectedNames: []string{".envrc"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profile, "[eE][nN][vV][rR][cC]") {
		t.Errorf("the profile stopped protecting .envrc:\n%s", profile)
	}
}

func TestDenialsEveryProcessHitsAreNotReported(t *testing.T) {
	for _, line := range []string{
		"Sandbox: sleep(81234) deny(1) file-write-data /dev/dtracehelper",
		"Sandbox: bash(81235) deny(1) file-write-data /dev/tty OSBX0123456789abcdef_ollupi",
	} {
		if detail, ok := denialDetail(line); ok || detail != "" {
			t.Errorf("%q is reported as %q", line, detail)
		}
	}
	line := "Sandbox: go(81236) deny(1) file-write-data /Users/me/Library/Caches/go-build/ab OSBX0123456789abcdef_ollupi"
	if detail, ok := denialDetail(line); !ok || detail != "go: file-write-data /Users/me/Library/Caches/go-build/ab" {
		t.Errorf("a real denial is reported as %q", detail)
	}
}

func TestADenialBeforeANoisyOneIsStillReported(t *testing.T) {
	l := newDenialLog()
	id, tag := l.newTag()
	log := "Sandbox: go(1) deny(1) file-write-data /Users/me/Library/Caches/go-build/ab\n" +
		"Sandbox: go(1) deny(1) file-write-data /dev/dtracehelper " + tag + "\n"
	l.consume(bufio.NewScanner(strings.NewReader(log)))
	if got := l.collect(id, false); !slices.Equal(got, []string{"go: file-write-data /Users/me/Library/Caches/go-build/ab"}) {
		t.Errorf("reported %q", got)
	}
}
