package contextmanager

import (
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

const maxArtifactRunes = 20000

// CheckArtifacts accepts the context artifacts of a tool result: named skills that fit the model's artifact budget.
func (m *Manager) CheckArtifacts(artifacts []tools.ContextArtifact) error {
	if len(artifacts) == 0 {
		return nil
	}
	for _, artifact := range artifacts {
		if artifact.Name == "" || strings.TrimSpace(artifact.Content) == "" {
			return fmt.Errorf("context artifact requires a name and content")
		}
		if artifact.Kind != tools.ArtifactAgentSkill {
			return fmt.Errorf("context artifact %s/%s has unsupported kind", artifact.Kind, artifact.Name)
		}
		if len([]rune(artifact.Content)) > maxArtifactRunes {
			return fmt.Errorf("context artifact %s/%s exceeds %d runes", artifact.Kind, artifact.Name, maxArtifactRunes)
		}
	}
	if need, budget := llm.EstimateTokens(renderArtifacts(artifacts)), m.artifactBudget(); need > budget {
		return fmt.Errorf("the tool's context artifacts need about %d tokens; model %s leaves %d for them", need, m.model, budget)
	}
	return nil
}

func activeArtifacts(items []db.ItemRow) []tools.ContextArtifact {
	var out []tools.ContextArtifact
	for _, it := range items {
		if !it.IsCall() || it.Status != db.ItemCompleted {
			continue
		}
		for _, artifact := range it.Result.Artifacts {
			out = slices.DeleteFunc(out, func(a tools.ContextArtifact) bool { return a.Kind == artifact.Kind && a.Name == artifact.Name })
			out = append(out, artifact)
		}
	}
	return out
}

func renderArtifact(a tools.ContextArtifact) string {
	return fmt.Sprintf("--- %s: %s ---\n%s", a.Kind, a.Name, a.Content)
}

func renderArtifacts(artifacts []tools.ContextArtifact) string {
	parts := make([]string, 0, len(artifacts))
	for _, a := range artifacts {
		parts = append(parts, renderArtifact(a))
	}
	return strings.Join(parts, "\n\n")
}

func recentArtifacts(active []tools.ContextArtifact, budget int) (kept, dropped []tools.ContextArtifact) {
	used := 0
	fits := make([]bool, len(active))
	for i := len(active) - 1; i >= 0; i-- {
		if cost := llm.EstimateTokens(renderArtifact(active[i])); used+cost <= budget {
			used += cost
			fits[i] = true
		}
	}
	for i, artifact := range active {
		if fits[i] {
			kept = append(kept, artifact)
		} else {
			dropped = append(dropped, artifact)
		}
	}
	return kept, dropped
}
