package contextmanager

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

func skillCall(id, name, content string) db.ItemRow {
	return db.ItemRow{ID: id, Type: db.ItemToolCall, Status: db.ItemCompleted, ResponseID: "r", CallID: id, Tool: "activate_skill",
		Arguments: `{"name":"` + name + `"}`, Result: &db.ToolResult{Output: json.RawMessage(`"loaded"`),
			Artifacts: []tools.ContextArtifact{{Kind: tools.ArtifactAgentSkill, Name: name, Content: content}}}}
}

// Skills loaded in one answer, each within the artifact budget, do not together fill the window, and a skill loaded
// again is carried once.
func TestTheSkillsATranscriptCarriesFitTheirBudgetTogether(t *testing.T) {
	m := New("m", llm.Limits{Context: 32000, Output: 4000})
	body := strings.Repeat("Follow this step with care. ", 500)
	items := []db.ItemRow{user("set it all up")}
	for i := range 7 {
		items = append(items, skillCall(fmt.Sprint("call", i), fmt.Sprint("skill", i), body))
	}
	items = append(items, skillCall("again", "skill6", body))
	p, err := m.Build(Input{Items: items, System: "system"})
	if err != nil {
		t.Fatalf("the transcript does not fit: %v", err)
	}
	var all strings.Builder
	for _, msg := range p.Messages {
		all.WriteString(msg.Content)
	}
	full := strings.Count(all.String(), "--- "+tools.ArtifactAgentSkill+": ")
	cost := llm.EstimateTokens(renderArtifact(tools.ContextArtifact{Kind: tools.ArtifactAgentSkill, Name: "skill0", Content: body}))
	if full == 0 || full*cost > m.artifactBudget() {
		t.Errorf("the transcript carries %d skills of %d tokens each, want at least one and at most the budget of %d", full, cost, m.artifactBudget())
	}
	if !strings.Contains(all.String(), "skill6: loaded again later") || !strings.Contains(all.String(), "skill0: no longer fits") {
		t.Errorf("the skills left out are not named:\n%.2000s", all.String())
	}
}
