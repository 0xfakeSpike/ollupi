package repo

import (
	"testing"
	"time"
)

// root_path changes only through SetWorkspaceRoot, and a folder another workspace of the user has stays its own.
func TestAWorkspaceRootMovesAndStaysUnique(t *testing.T) {
	db := sharedShard(t)

	// A turn that waits on the user is an open call in the workspace: the folder cannot move from under it.
	if busy, err := db.WorkspaceBusy("a", "w-a"); err != nil || !busy {
		t.Errorf("workspace of a user: busy %v, %v; want true", busy, err)
	}
	if busy, err := db.WorkspaceBusy("b", "w-a"); err != nil || busy {
		t.Errorf("another user's workspace: busy %v, %v; want false", busy, err)
	}
	if busy, err := db.WorkspaceBusy("a", "w-a2"); err != nil || busy {
		t.Errorf("a workspace that is not there: busy %v, %v; want false", busy, err)
	}

	before, err := db.WorkspaceOf("a", "w-a")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	moved, err := db.SetWorkspaceRoot("a", "w-a", "/srv/moved", at)
	if err != nil {
		t.Fatal(err)
	}
	if moved.RootPath != "/srv/moved" || !moved.UpdatedAt.Equal(at) {
		t.Errorf("moved to %q at %v, want /srv/moved at %v", moved.RootPath, moved.UpdatedAt, at)
	}
	if moved.ID != before.ID || moved.Name != before.Name || !moved.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("%+v kept none of the workspace's own columns: %+v", moved, before)
	}
	if saved, err := db.WorkspaceOf("a", "w-a"); err != nil || saved.RootPath != "/srv/moved" {
		t.Errorf("the database names %q (%v), want /srv/moved", saved.RootPath, err)
	}

	// A second workspace of the user cannot take a folder that another one has.
	other := WorkspaceRow{ID: "w-a2", UserID: "a", Name: "other", RootPath: "/srv/other", CreatedAt: at, UpdatedAt: at}
	if err := db.saveWorkspace(other); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetWorkspaceRoot("a", "w-a", "/srv/other", at); err == nil {
		t.Error("two workspaces of one user came to share a folder")
	}
	if saved, err := db.WorkspaceOf("a", "w-a"); err != nil || saved.RootPath != "/srv/moved" {
		t.Errorf("a refused move left the workspace at %q (%v), want /srv/moved", saved.RootPath, err)
	}

	// Another user's workspace is not the caller's to move.
	if _, err := db.SetWorkspaceRoot("b", "w-a", "/srv/theirs", at); err == nil {
		t.Error("another user moved the workspace")
	}
}
