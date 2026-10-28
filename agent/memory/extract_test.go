package memory

import (
	"slices"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/repo"
)

func scopeBatch(existing ...repo.MemoryRow) *Batch {
	return &Batch{
		Scope: repo.MemoryWorkspace, WorkspaceID: "w", TurnID: "t",
		Facts:    []Fact{{Source: repo.MemorySource{Table: "items", ID: "i1"}, Data: []byte(`{}`)}},
		Existing: existing,
	}
}

func same(s string) string { return s }

func TestExtractionScopes(t *testing.T) {
	global := repo.MemoryRow{ID: "g", Scope: repo.MemoryGlobal, Topic: "language", Kind: "preference", Status: repo.MemoryActive}
	batch := scopeBatch(global)
	op := func(fields string) string {
		return `{"operations":[{` + fields + `,"sources":[{"table":"items","id":"i1"}]}]}`
	}
	for _, c := range []struct {
		content, err, scope string
	}{
		{op(`"action":"add","scope":"global","topic":"Editor","kind":"preference","text":"Uses vim."`), "", scopeGlobal},
		{op(`"action":"add","scope":"local","topic":"Build","kind":"decision","text":"Builds with make."`), "", scopeLocal},
		{op(`"action":"add","topic":"Build","kind":"fact","text":"Builds with make."`), "", scopeLocal},
		{op(`"action":"add","scope":"workspace","topic":"Build","kind":"fact","text":"Builds with make."`), "", scopeLocal},
		{op(`"action":"add","scope":"global","topic":"Build","kind":"decision","text":"Builds with make."`), "", scopeLocal},
		{op(`"action":"update","id":"g","topic":"language","text":"Answers in Chinese.","kind":"preference"`), "", scopeGlobal},
		{op(`"action":"update","id":"g","topic":"language","scope":"local","text":"Answers in Chinese.","kind":"preference"`), "", scopeGlobal},
		{op(`"action":"update","id":"g","topic":"language","text":"Ships on Fridays.","kind":"procedure"`), "only preferences and facts", ""},
	} {
		ops, err := parseOperations(c.content, batch, same)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: got %v, want an error about %q", c.content, err, c.err)
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.content, err)
		case c.err == "" && ops[0].Scope != c.scope:
			t.Errorf("%s: scope %q, want %q", c.content, ops[0].Scope, c.scope)
		}
	}
}

func TestCommitPlacesAddsByScope(t *testing.T) {
	m := testManager(t, true,
		repo.MemoryRow{ID: "local-build", WorkspaceID: "w", Topic: "build", Kind: "fact", Text: "Builds with go."},
		repo.MemoryRow{ID: "other-build", WorkspaceID: "x", Topic: "editor", Kind: "fact", Text: "Elsewhere."},
	)
	existing, err := m.db.Memories(repo.MemoryWorkspace, "w", true)
	if err != nil {
		t.Fatal(err)
	}
	batch := scopeBatch(existing...)
	ops, err := parseOperations(`{"operations":[
		{"action":"add","scope":"global","topic":"Editor","kind":"preference","text":"Uses vim.","sources":[{"table":"items","id":"i1"}]},
		{"action":"add","scope":"global","topic":"Build","kind":"fact","text":"Knows Go well.","sources":[{"table":"items","id":"i1"}]},
		{"action":"add","scope":"local","topic":"Build","kind":"fact","text":"Builds with make.","sources":[{"table":"items","id":"i1"}]}
	]}`, batch, same)
	if err != nil {
		t.Fatal(err)
	}
	if err := commit(m.db, batch, ops, repo.MemoryReport{}); err != nil {
		t.Fatal(err)
	}
	rows, err := m.db.Memories(repo.MemoryWorkspace, "w", true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, string(r.Scope)+" "+r.WorkspaceID+" "+r.OriginWorkspaceID+" "+r.Topic+": "+r.Text)
	}
	slices.Sort(got)
	want := []string{"global  w build: Knows Go well.", "global  w editor: Uses vim.", "workspace w  build: Builds with make."}
	if !slices.Equal(got, want) {
		t.Errorf("memories\n%q\nwant\n%q", got, want)
	}
	for _, bad := range []repo.MemoryRow{
		{ID: "b1", Scope: repo.MemoryGlobal, WorkspaceID: "w", Topic: "b1", Kind: "fact", Text: "x", Status: repo.MemoryActive},
		{ID: "b2", Scope: repo.MemoryWorkspace, Topic: "b2", Kind: "fact", Text: "x", Status: repo.MemoryActive},
		{ID: "b3", Scope: repo.MemoryWorkspace, WorkspaceID: "w", OriginWorkspaceID: "x", Topic: "b3", Kind: "fact", Text: "x", Status: repo.MemoryActive},
	} {
		if err := m.db.SaveMemory(bad); err == nil {
			t.Errorf("the database took a %s memory with workspace %q", bad.Scope, bad.WorkspaceID)
		}
	}
}
