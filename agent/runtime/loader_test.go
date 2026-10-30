package runtime

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/tools"
)

func TestLoadToolsTakesOnlyDeferredTools(t *testing.T) {
	ctx := context.WithValue(context.Background(), deferredKey{}, []tools.ToolDescriptor{{Name: "github__issue"}, {Name: "github__pr"}})

	r, err := Loader{}.Invoke(ctx, tools.ToolLoadTools, map[string]any{"names": []any{"github__pr", "github__pr"}})
	out, _ := r.Output.(map[string]any)
	if loaded, _ := out["loaded"].([]string); err != nil || !slices.Equal(loaded, []string{"github__pr"}) {
		t.Errorf("result %+v, %v, want github__pr loaded once", r, err)
	}

	if _, err := (Loader{}).Invoke(ctx, tools.ToolLoadTools, map[string]any{"names": []any{"shell"}}); err == nil || !strings.Contains(err.Error(), "not a deferred tool") {
		t.Errorf("loading a tool that is not deferred: %v", err)
	}
}
