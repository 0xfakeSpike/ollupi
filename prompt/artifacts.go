package prompt

import (
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

const SkillArtifact = "agent_skill"

const maxArtifactRunes = 20000

func (w Window) CheckArtifacts(artifacts []conversation.Artifact) error {
	if len(artifacts) == 0 {
		return nil
	}
	for _, artifact := range artifacts {
		if artifact.Name == "" || strings.TrimSpace(artifact.Content) == "" {
			return fmt.Errorf("context artifact requires a name and content")
		}
		if artifact.Kind != SkillArtifact {
			return fmt.Errorf("context artifact %s/%s has unsupported kind", artifact.Kind, artifact.Name)
		}
		if len([]rune(artifact.Content)) > maxArtifactRunes {
			return fmt.Errorf("context artifact %s/%s exceeds %d runes", artifact.Kind, artifact.Name, maxArtifactRunes)
		}
	}
	if need, budget := llm.EstimateTokens(renderArtifacts(artifacts)), w.artifactBudget(); need > budget {
		return fmt.Errorf("the tool's context artifacts need about %d tokens; model %s leaves %d for them", need, w.model, budget)
	}
	return nil
}

func activeArtifacts(items []conversation.Item) []conversation.Artifact {
	var out []conversation.Artifact
	for _, it := range items {
		if !completedCall(it) {
			continue
		}
		for _, artifact := range it.Result.Artifacts {
			out = slices.DeleteFunc(out, func(a conversation.Artifact) bool { return a.Kind == artifact.Kind && a.Name == artifact.Name })
			out = append(out, artifact)
		}
	}
	return out
}

func renderArtifact(a conversation.Artifact) string {
	return fmt.Sprintf("--- %s: %s ---\n%s", a.Kind, a.Name, a.Content)
}

func renderArtifacts(artifacts []conversation.Artifact) string {
	parts := make([]string, 0, len(artifacts))
	for _, a := range artifacts {
		parts = append(parts, renderArtifact(a))
	}
	return strings.Join(parts, "\n\n")
}

func recentArtifacts(active []conversation.Artifact, budget int) (kept, dropped []conversation.Artifact) {
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
