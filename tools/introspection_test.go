package tools

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestLoadToolsTakesOnlyDeferredTools(t *testing.T) {
	reg, err := New(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithDeferred(context.Background(), []ToolDescriptor{{Name: "github__issue"}, {Name: "github__pr"}})

	r := reg.Invoke(ctx, ToolLoadTools, map[string]any{"names": []any{"github__pr", "github__pr"}})
	out, _ := r.Output.(map[string]any)
	if loaded, _ := out["loaded"].([]string); r.Err != nil || !slices.Equal(loaded, []string{"github__pr"}) {
		t.Errorf("result %+v, want github__pr loaded once", r)
	}

	if r := reg.Invoke(ctx, ToolLoadTools, map[string]any{"names": []any{"shell"}}); r.Err == nil || !strings.Contains(r.Err.Error(), "not a deferred tool") {
		t.Errorf("loading a tool that is not deferred: %+v", r)
	}
}
