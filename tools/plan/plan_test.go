package plan

import (
	"context"
	"testing"

	"github.com/0xfakeSpike/ollupi/tools"
)

func TestAPlanHasAtMostOneStepInProgress(t *testing.T) {
	r, err := tools.New(context.Background(), nil, New())
	if err != nil {
		t.Fatal(err)
	}
	update := func(steps ...map[string]any) tools.Result {
		plan := make([]any, len(steps))
		for i, s := range steps {
			plan[i] = s
		}
		return r.Invoke(context.Background(), tools.Caller{}, Tool, map[string]any{"plan": plan})
	}
	step := func(text string, status Status) map[string]any {
		return map[string]any{"step": text, "status": string(status)}
	}
	if res := update(step("read", Completed), step("write", InProgress), step("test", Pending)); res.Err != nil {
		t.Fatalf("a plan in order: %v", res.Err)
	}
	for name, res := range map[string]tools.Result{
		"no steps":          update(),
		"two in progress":   update(step("a", InProgress), step("b", InProgress)),
		"a blank step":      update(step(" ", Pending)),
		"an unknown status": update(step("a", "done")),
	} {
		if res.Err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
