package agent_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo"
)

// Every user starts with the environment on this machine, which a conversation that names none runs in.
func TestEveryUserRunsOnThisMachineUntilTheyAddAnother(t *testing.T) {
	ag, _ := environmentAgent(t)

	envs, err := ag.Environments(repo.DefaultUser)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 || envs[0].Kind != repo.EnvironmentLocal || envs[0].Name != repo.LocalEnvironmentName {
		t.Fatalf("the environments are %+v, want this machine alone", envs)
	}
	local := envs[0]

	workspace := addWorkspace(t, ag, t.TempDir())

	here, err := ag.StartThread(repo.DefaultUser, workspace.ID, "", "here", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if here.EnvironmentID != repo.Ref(local.ID) {
		t.Errorf("a conversation that names no environment runs in %q, want %q", here.EnvironmentID, local.ID)
	}

	cloud, err := ag.AddEnvironment(repo.DefaultUser, "cloud", repo.EnvironmentMachine)
	if err != nil {
		t.Fatal(err)
	}
	there, err := ag.StartThread(repo.DefaultUser, workspace.ID, cloud.ID, "there", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if there.EnvironmentID != repo.Ref(cloud.ID) {
		t.Errorf("the conversation runs in %q, want %q", there.EnvironmentID, cloud.ID)
	}

	// The same name twice is one environment, not two.
	again, err := ag.AddEnvironment(repo.DefaultUser, "CLOUD", repo.EnvironmentMachine)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != cloud.ID {
		t.Errorf("adding a name twice made %s, want the environment %s", again.ID, cloud.ID)
	}
}

// An environment is the user's to name and to remove, except this machine and the ones a conversation runs in.
func TestThisMachineStaysAndSoDoesTheEnvironmentsInUse(t *testing.T) {
	ag, _ := environmentAgent(t)
	envs, err := ag.Environments(repo.DefaultUser)
	if err != nil {
		t.Fatal(err)
	}
	local := envs[0]

	renamed, err := ag.RenameEnvironment(repo.DefaultUser, local.ID, "my mac")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "my mac" {
		t.Errorf("the environment is called %q after renaming it", renamed.Name)
	}
	if _, err := ag.RenameEnvironment(repo.DefaultUser, local.ID, "my mac "); err != nil {
		t.Errorf("renaming an environment to its own name failed: %v", err)
	}

	cloud, err := ag.AddEnvironment(repo.DefaultUser, "cloud", repo.EnvironmentMachine)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.RenameEnvironment(repo.DefaultUser, cloud.ID, "my mac"); !errors.Is(err, agent.ErrInvalidName) {
		t.Errorf("two environments got one name: %v", err)
	}
	if _, err := ag.DeleteEnvironment(repo.DefaultUser, local.ID); !errors.Is(err, agent.ErrInvalidName) {
		t.Errorf("this machine was removed: %v", err)
	}

	workspace := addWorkspace(t, ag, t.TempDir())
	if _, err := ag.StartThread(repo.DefaultUser, workspace.ID, cloud.ID, "there", permission.Settings{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.DeleteEnvironment(repo.DefaultUser, cloud.ID); !errors.Is(err, agent.ErrEnvironmentInUse) {
		t.Errorf("an environment a conversation runs in was removed: %v", err)
	}

	other, err := ag.AddEnvironment(repo.DefaultUser, "other", repo.EnvironmentMachine)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.DeleteEnvironment(repo.DefaultUser, other.ID); err != nil {
		t.Errorf("an environment no conversation runs in stays: %v", err)
	}
	if _, err := ag.DeleteEnvironment(repo.DefaultUser, other.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("an environment was removed twice: %v", err)
	}
}

// A conversation runs in an environment of its own user, and adding one is a place to run, not this machine.
func TestAConversationOnlyRunsInItsOwnUsersEnvironments(t *testing.T) {
	ag, db := environmentAgent(t)
	if err := db.EnsureUser("other"); err != nil {
		t.Fatal(err)
	}
	theirs, err := ag.Environments("other")
	if err != nil {
		t.Fatal(err)
	}
	if len(theirs) != 1 {
		t.Fatalf("the other user has %d environments, want their own machine", len(theirs))
	}

	if _, err := ag.StartThread(repo.DefaultUser, "", theirs[0].ID, "x", permission.Settings{}); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("a conversation was started in another user's environment: %v", err)
	}
	if _, err := ag.StartThread(repo.DefaultUser, "", "nope", "x", permission.Settings{}); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("a conversation was started in an environment that does not exist: %v", err)
	}
	if _, err := ag.AddEnvironment(repo.DefaultUser, "local again", repo.EnvironmentLocal); !errors.Is(err, agent.ErrInvalidName) {
		t.Errorf("a second environment on this machine was added: %v", err)
	}
	if _, err := ag.AddEnvironment(repo.DefaultUser, "  ", repo.EnvironmentMachine); !errors.Is(err, agent.ErrInvalidName) {
		t.Errorf("an environment with no name was added: %v", err)
	}
}

func environmentAgent(t *testing.T) (*agent.Agent, *repo.DB) {
	t.Helper()
	return started(context.Background(), t, agent.Config{}, "http://127.0.0.1:1")
}

func addWorkspace(t *testing.T, ag *agent.Agent, dir string) repo.WorkspaceRow {
	ctx := context.Background()
	t.Helper()
	root, err := sandbox.CanonicalDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := ag.AddWorkspace(ctx, repo.DefaultUser, root, "")
	if err != nil {
		t.Fatal(err)
	}
	return w
}
