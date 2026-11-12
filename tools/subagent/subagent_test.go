package subagent

import (
	"context"
	"testing"

	"github.com/0xfakeSpike/ollupi/tools"
)

func TestASubagentNeedsATaskToDo(t *testing.T) {
	res, err := New().Invoke(context.Background(), Tool, map[string]any{"description": "Read the docs", "prompt": "Summarize docs/"})
	if err != nil || res.Await == nil || res.Await.By != tools.AwaitAgent {
		t.Fatalf("a task: %+v %v", res, err)
	}
	call, err := Decode(res.Await.Request)
	if err != nil || call.Prompt != "Summarize docs/" {
		t.Errorf("the call it waits on: %+v %v", call, err)
	}
	if _, err := New().Invoke(context.Background(), Tool, map[string]any{"description": "x", "prompt": "  "}); err == nil {
		t.Error("a blank prompt launched a subagent")
	}
}
