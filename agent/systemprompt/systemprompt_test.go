package systemprompt

import (
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/tools"
)

func TestDeferredToolsAreListedByProvider(t *testing.T) {
	deferred := []tools.ToolDescriptor{{Provider: "github", Name: "github__issue"}, {Provider: "github", Name: "github__pr"}, {Provider: "slack", Name: "slack__post"}}
	text := Build(Input{Agent: "AGENT", Root: "/p", Deferred: deferred})
	list := "# DEFERRED TOOLS\n"
	i, j := strings.Index(text, list), strings.Index(text, "# PROJECT")
	if i < 0 || j < i || !strings.Contains(text[i:j], "\n- github: github__issue, github__pr\n- slack: slack__post\n") {
		t.Errorf("prompt:\n%s", text)
	}
	if strings.Contains(Build(Input{Agent: "AGENT"}), list) {
		t.Error("a prompt without deferred tools has a DEFERRED TOOLS section")
	}
}
