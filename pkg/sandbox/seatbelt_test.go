package sandbox

import (
	"strings"
	"testing"
)

// A workspace's repository is written like any other folder: the profile no longer names paths under .git, so git init
// and git clone are not stopped by the sandbox.
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

// The settings files other programs later run are still closed, even inside a writable root.
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
