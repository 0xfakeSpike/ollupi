package tools

import "testing"

func TestOnlyABackgroundToolTakesRunInBackground(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}, "additionalProperties": false}
	slow, quick := ToolDescriptor{Name: "slow", InputSchema: schema, Background: true}, ToolDescriptor{Name: "quick", InputSchema: schema}
	if _, ok := slow.ModelSchema()["properties"].(map[string]any)[ArgBackground]; !ok {
		t.Errorf("the background tool's schema: %v", slow.ModelSchema())
	}
	if _, ok := schema["properties"].(map[string]any)[ArgBackground]; ok {
		t.Error("the tool's own schema was changed")
	}
	if _, ok := quick.ModelSchema()["properties"].(map[string]any)[ArgBackground]; ok {
		t.Error("a foreground tool's schema takes run_in_background")
	}
	args := map[string]any{"cmd": "x", ArgBackground: true}
	if background, err := slow.TakeBackground(args); err != nil || !background || len(args) != 1 {
		t.Errorf("a background call: %v %v %v", args, background, err)
	}
	own := map[string]any{ArgBackground: true}
	if background, err := quick.TakeBackground(own); err != nil || background || own[ArgBackground] != true {
		t.Errorf("another tool's own argument of that name: %v %v %v", own, background, err)
	}
	if _, err := slow.TakeBackground(map[string]any{ArgBackground: "yes"}); err == nil {
		t.Error("a run_in_background that is no boolean was taken")
	}
}
