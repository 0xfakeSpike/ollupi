package contextmanager

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
)

func user(text string) repo.ItemRow {
	return repo.ItemRow{Type: repo.ItemUserMessage, Status: repo.ItemCompleted, Text: text}
}

func answer(response, text string) repo.ItemRow {
	return repo.ItemRow{Type: repo.ItemAgentMessage, Status: repo.ItemCompleted, ResponseID: response, Text: text}
}

func memory(text string, entries ...repo.MemoryReference) repo.ItemRow {
	return repo.ItemRow{Type: repo.ItemMemoryContext, Status: repo.ItemCompleted, Text: text, Memory: entries}
}

func contents(messages []llm.ChatMessage) []string {
	var out []string
	for _, m := range messages {
		out = append(out, m.Content)
	}
	return out
}

func build(t *testing.T, items ...repo.ItemRow) Prompt {
	t.Helper()
	p, err := New("m", llm.Limits{Context: 100000, Output: 10000}).Build(Input{Items: items, System: "system"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSnapshotLeadsThePrompt(t *testing.T) {
	snapshot := memory("SNAPSHOT",
		repo.MemoryReference{MemoryNote: repo.MemoryNote{ID: "g", Text: "global"}, Scope: repo.MemoryGlobal},
		repo.MemoryReference{MemoryNote: repo.MemoryNote{ID: "w", Text: "workspace"}, Scope: repo.MemoryWorkspace, WorkspaceID: "ws"})
	p := build(t, user("q1"), snapshot, answer("r1", "a1"), user("q2"), answer("r2", "a2"), user("q3"))
	if got, want := contents(p.Messages), []string{"SNAPSHOT", "q1", "a1", "q2", "a2", "q3"}; !slices.Equal(got, want) {
		t.Errorf("messages %q, want %q", got, want)
	}
	s := p.Stats
	if s.MemoryTokens != llm.EstimateTokens("SNAPSHOT") || s.GlobalMemory == nil || !slices.Equal(s.GlobalMemory.IDs, []string{"g"}) ||
		s.LocalMemory == nil || !slices.Equal(s.LocalMemory.IDs, []string{"w"}) || s.GlobalMemory.Tokens == 0 {
		t.Errorf("stats %+v, global %+v, local %+v", s, s.GlobalMemory, s.LocalMemory)
	}

	if s := build(t, user("q1"), answer("r1", "a1")).Stats; s.MemoryTokens != 0 || s.GlobalMemory != nil {
		t.Errorf("no snapshot, but stats %+v", s)
	}
}

func TestUserMessagesCarryTheirSendTime(t *testing.T) {
	at := time.Date(2026, 10, 29, 2, 15, 0, 0, time.UTC)
	sent := user("q1")
	sent.StartedAt = at
	got := contents(build(t, sent, answer("r1", "a1")).Messages)
	if want := "[sent " + at.In(time.Local).Format(sentLayout) + "]\nq1"; len(got) != 2 || got[0] != want {
		t.Errorf("messages %q, want %q first", got, want)
	}
}

func TestCompactionRefreshesTheSnapshot(t *testing.T) {
	compaction := repo.ItemRow{Type: repo.ItemContextCompaction, Status: repo.ItemCompleted, ResponseID: "c", Text: "SUMMARY"}
	items := []repo.ItemRow{user("q1"), memory("OLD"), answer("r1", "a1"), user("q2"), compaction, memory("NEW"), answer("r2", "a2")}
	got := contents(build(t, items...).Messages)
	if len(got) != 3 || got[0] != "NEW" || !strings.Contains(got[1], "SUMMARY") || got[2] != "a2" || strings.Contains(strings.Join(got, "\n"), "OLD") {
		t.Errorf("messages %q", got)
	}

	p, err := New("m", llm.Limits{Context: 100000, Output: 10000}).CompactionPrompt(Input{Items: items, System: "system"})
	if err != nil {
		t.Fatal(err)
	}
	if text := strings.Join(contents(p.Messages), "\n"); strings.Contains(text, "NEW") || strings.Contains(text, "OLD") {
		t.Errorf("the summary request carries memory:\n%s", text)
	}
}
