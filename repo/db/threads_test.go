package db

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestThreadPagesFollowListOrder(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	// Two threads share an update time and a creation time, so only the id orders them.
	for i, updated := range []time.Duration{3, 2, 2, 2, 1} {
		created := at
		if i == 3 {
			created = at.Add(-time.Hour)
		}
		row := ThreadRow{UserID: DefaultUser, ID: fmt.Sprintf("t%d", i), CreatedAt: created, UpdatedAt: at.Add(updated * time.Minute)}
		if err := db.CreateThread(row); err != nil {
			t.Fatal(err)
		}
	}
	var listed []string
	var after *ThreadCursor
	for {
		page, _, err := db.ThreadRows(DefaultUser, ThreadFilter{After: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			listed = append(listed, r.ID)
		}
		last := page[len(page)-1]
		after = &ThreadCursor{UpdatedAt: last.UpdatedAt, CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if got, want := fmt.Sprint(listed), "[t0 t2 t1 t3 t4]"; got != want {
		t.Errorf("pages listed %s, want %s", got, want)
	}
}

// A thread named by id is its user's alone: to anyone else each of these finds nothing and changes nothing.
func TestAThreadByIDIsItsUsersAlone(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, user := range []string{"a", "b"} {
		if err := db.EnsureUser(user); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().UTC()
	if err := db.CreateThread(ThreadRow{UserID: "a", ID: "t", Title: "mine", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	for name, try := range map[string]func() error{
		"page":    func() error { _, _, err := db.ThreadPage("b", "t", 0, 10); return err },
		"rename":  func() error { _, err := db.SetThreadTitle("b", "t", "theirs"); return err },
		"read":    func() error { _, err := db.MarkThreadRead("b", "t", at); return err },
		"archive": func() error { _, err := db.SetThreadArchived("b", "t", &at); return err },
		"settings": func() error {
			_, err := db.UpdateThreadSettings("b", "t", func(PermissionSettings) PermissionSettings { return PermissionSettings{} })
			return err
		},
		"attachment": func() error { _, err := db.Attachment("b", "t", "file"); return err },
		"delete":     func() error { return db.DeleteThread("b", "t") },
	} {
		if err := try(); !errors.Is(err, ErrNotFound) {
			t.Errorf("b tried to %s: %v", name, err)
		}
	}
	row, err := db.ThreadOf("a", "t")
	if err != nil || row.Title != "mine" || row.ArchivedAt != nil || row.ReadAt != nil {
		t.Errorf("a's thread after b's tries: %+v %v", row, err)
	}
}
