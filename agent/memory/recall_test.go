package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

func testManager(t *testing.T, use bool, rows ...repo.MemoryRow) *Manager {
	t.Helper()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, r := range rows {
		if r.Status == "" {
			r.Status = repo.MemoryActive
		}
		r.CreatedAt = base
		r.UpdatedAt = base.Add(time.Duration(i) * time.Minute)
		if err := db.SaveMemory(r); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New(db, nil, Options{Use: use})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func ids(entries []repo.MemoryReference) []string {
	out := []string{}
	for _, e := range entries {
		out = append(out, e.ID)
	}
	return out
}

// A snapshot has the global memories and the workspace's, each section
// ranked by kind and then recency, and nothing inactive or from elsewhere.
func TestSnapshotSections(t *testing.T) {
	m := testManager(t, true,
		repo.MemoryRow{ID: "g-fact", Topic: "a", Kind: "fact", Text: "The sky is blue."},
		repo.MemoryRow{ID: "g-pref", Topic: "b", Kind: "preference", Text: "Answers in Chinese."},
		repo.MemoryRow{ID: "w-fact-old", WorkspaceID: "w", Topic: "c", Kind: "fact", Text: "Uses Go."},
		repo.MemoryRow{ID: "w-decision", WorkspaceID: "w", Topic: "d", Kind: "decision", Text: "Prefix MCP tools."},
		repo.MemoryRow{ID: "w-fact-new", WorkspaceID: "w", Topic: "e", Kind: "fact", Text: "Uses gorm."},
		repo.MemoryRow{ID: "w-retired", WorkspaceID: "w", Topic: "f", Kind: "preference", Text: "Old.", Status: repo.MemoryRetired},
		repo.MemoryRow{ID: "other", WorkspaceID: "x", Topic: "g", Kind: "preference", Text: "Elsewhere."},
	)
	s, err := m.Snapshot("w", 4000)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(s.Entries), []string{"g-pref", "g-fact", "w-decision", "w-fact-new", "w-fact-old"}; !slices.Equal(got, want) {
		t.Errorf("entries %v, want %v", got, want)
	}
	global, workspace := strings.Index(s.Text, "Global memories"), strings.Index(s.Text, "Memories of this conversation's workspace")
	if global < 0 || workspace < global || strings.Contains(s.Text, `"workspaceId"`) || strings.Contains(s.Text, "did not fit") {
		t.Errorf("text:\n%s", s.Text)
	}

	s, err = m.Snapshot("", 4000)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(s.Entries); !slices.Equal(got, []string{"g-pref", "g-fact"}) || strings.Contains(s.Text, "this conversation's workspace") {
		t.Errorf("a thread without a workspace got %v:\n%s", got, s.Text)
	}
}

// Each section gets half the budget, and what one leaves goes to the other.
func TestSnapshotBudget(t *testing.T) {
	text := strings.Repeat("word ", 40)
	var rows []repo.MemoryRow
	for i := range 10 {
		rows = append(rows, repo.MemoryRow{ID: fmt.Sprintf("g%d", i), Topic: fmt.Sprintf("g%d", i), Kind: "fact", Text: text})
	}
	rows = append(rows, repo.MemoryRow{ID: "w0", WorkspaceID: "w", Topic: "w0", Kind: "fact", Text: text})
	m := testManager(t, true, rows...)
	const budget = 600
	s, err := m.Snapshot("w", budget)
	if err != nil {
		t.Fatal(err)
	}
	global := slices.DeleteFunc(ids(s.Entries), func(id string) bool { return id == "w0" })
	if !slices.Contains(ids(s.Entries), "w0") || len(global) < 3 || len(global) == 10 {
		t.Errorf("entries %v: want w0 and more than half the budget of global memories", ids(s.Entries))
	}
	if tokens := llm.EstimateTokens(s.Text); tokens > budget {
		t.Errorf("snapshot takes %d tokens, over %d", tokens, budget)
	}
	if !strings.Contains(s.Text, fmt.Sprintf("%d more memories did not fit here", 10-len(global))) {
		t.Errorf("text does not count what was left out:\n%s", s.Text)
	}
}

func TestSnapshotEmptyAndUnused(t *testing.T) {
	s, err := testManager(t, true).Snapshot("w", 4000)
	if err != nil || s == nil || s.Text != "" || s.Entries == nil || len(s.Entries) != 0 {
		t.Errorf("no memories: %+v, %v; want an empty snapshot", s, err)
	}
	m := testManager(t, false, repo.MemoryRow{ID: "g", Topic: "a", Kind: "fact", Text: "x"})
	if s, err := m.Snapshot("w", 4000); s != nil || err != nil {
		t.Errorf("memory not used: %+v, %v; want no snapshot", s, err)
	}
	if specs, _ := m.ToolList(context.Background()); len(specs) != 0 {
		t.Errorf("memory not used, but %s is offered", ToolSearch)
	}
}

func TestSearch(t *testing.T) {
	m := testManager(t, true,
		repo.MemoryRow{ID: "g", Topic: "colors", Kind: "fact", Text: "Likes blue."},
		repo.MemoryRow{ID: "w", WorkspaceID: "w", Topic: "palette", Kind: "preference", Text: "Blue buttons."},
		repo.MemoryRow{ID: "other", WorkspaceID: "x", Topic: "colors", Kind: "fact", Text: "Blue too."},
		repo.MemoryRow{ID: "gone", WorkspaceID: "w", Topic: "old", Kind: "fact", Text: "Blue once.", Status: repo.MemoryRetired},
		repo.MemoryRow{ID: "unrelated", WorkspaceID: "w", Topic: "db", Kind: "fact", Text: "Uses sqlite."},
	)
	ctx := tools.WithCaller(context.Background(), tools.Caller{WorkspaceID: "w"})
	res, err := m.Invoke(ctx, ToolSearch, map[string]any{"query": "blue"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Output.(SearchResult).Memories); !slices.Equal(got, []string{"w", "g"}) {
		t.Errorf("found %v, want the workspace's preference, then the global fact", got)
	}
	res, err = m.Invoke(ctx, ToolSearch, map[string]any{"query": "nothing matches"})
	if err != nil || res.Output.(SearchResult).Memories == nil || len(res.Output.(SearchResult).Memories) != 0 {
		t.Errorf("no match: %+v, %v", res, err)
	}
	if _, err := m.Invoke(ctx, ToolSearch, map[string]any{"query": "?"}); err == nil {
		t.Error("a query without words was accepted")
	}
}
