package repo

import (
	"slices"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/tools"
)

func TestRunItemsCarryOnlyWhatOutlastsACompaction(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	if err := db.CreateThread(ThreadRow{ID: "t", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := db.createTurn(TurnRow{ID: "turn", ThreadID: "t", Seq: 1, Status: TurnInProgress, Model: llm.Model{Name: "m", Connection: "c"}, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	call := func(id, tool string, status ItemStatus) ItemRow {
		return ItemRow{ID: id, Type: ItemToolCall, Status: status, Tool: tool, Arguments: "{}", Result: &ToolResult{}}
	}
	skill, session := call("skill", "activate_skill", ItemCompleted), call("allowed", "touch", ItemFailed)
	skill.Result.Artifacts = []tools.ContextArtifact{{Kind: tools.ArtifactAgentSkill, Name: "s", Content: "use it"}}
	skill.Native = []byte(`{"response":"with only calls"}`)
	session.Approval = &Approval{ID: "a", RequestedAt: at, Decision: DecisionAcceptForSession}
	items := []ItemRow{
		{ID: "ask", Type: ItemUserMessage, Status: ItemCompleted, Text: "old ask"},
		{ID: "answer", Type: ItemAgentMessage, Status: ItemCompleted, Text: "old answer", Native: []byte(`{}`)},
		call("plain", "run_command", ItemCompleted),
		skill,
		call("load", tools.ToolLoadTools, ItemCompleted),
		session,
		{ID: "compaction", Type: ItemContextCompaction, Status: ItemCompleted, Text: "summary"},
		{ID: "recent", Type: ItemAgentMessage, Status: ItemCompleted, Text: "new answer", Native: []byte(`{"kept":true}`)},
	}
	for _, it := range items {
		it.TurnID, it.StartedAt = "turn", at
		if err := db.saveItemOf("t", it); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.RunItems("t")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range got {
		ids = append(ids, it.ID)
	}
	if want := []string{"ask", "skill", "load", "allowed", "compaction", "recent"}; !slices.Equal(ids, want) {
		t.Fatalf("run items %v, want %v", ids, want)
	}
	if got[len(got)-1].Native == nil || got[1].Native != nil {
		t.Errorf("native records: recent %q, carried %q; want only items after the compaction to keep theirs", got[len(got)-1].Native, got[1].Native)
	}
}
