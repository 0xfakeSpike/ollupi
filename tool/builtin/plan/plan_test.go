package plan

import (
	"context"
	"testing"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

func TestAPlanHasAtMostOneStepInProgress(t *testing.T) {
	r, err := kit.New(context.Background(), nil, Provider{})
	if err != nil {
		t.Fatal(err)
	}
	update := func(steps ...map[string]any) tool.Result {
		plan := make([]any, len(steps))
		for i, s := range steps {
			plan[i] = s
		}
		return r.Invoke(context.Background(), tool.Call{Tool: agent.PlanTool, Arguments: map[string]any{"plan": plan}})
	}
	step := func(text string, status Status) map[string]any {
		return map[string]any{"step": text, "status": string(status)}
	}
	if res := update(step("read", Completed), step("write", InProgress), step("test", Pending)); res.Error != "" {
		t.Fatalf("a plan in order: %v", res.Error)
	}
	for name, res := range map[string]tool.Result{
		"no steps":          update(),
		"two in progress":   update(step("a", InProgress), step("b", InProgress)),
		"a blank step":      update(step(" ", Pending)),
		"an unknown status": update(step("a", "done")),
	} {
		if res.Error == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}
