package ask

import (
	"context"
	"fmt"

	"github.com/0xfakeSpike/ollupi/ask"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

type Provider struct{}

func (Provider) Name() (string, string) { return "user_input", "Asks the user questions." }

func (Provider) Tools(context.Context) ([]kit.Spec, error) {
	return []kit.Spec{{
		Name: ask.Tool,
		Description: "Asks the user one to four short questions and waits for the answers. " +
			"Use it whenever you need the user's answer, instead of writing questions in your reply. " +
			"Not for permission to make a tool call: the runtime asks for approvals itself. " +
			"Make it the only call of its response: other calls in the same response run before the user answers.",
		InputSchema: kit.InputSchema[ask.Request](),
		Annotations: tool.Annotations{Title: "Question", ReadOnlyHint: true, OpenWorldHint: kit.Hint(false)},
	}}, nil
}

func (Provider) Invoke(_ context.Context, c tool.Call) (kit.Result, error) {
	r, _ := kit.DecodeArguments[ask.Request](c.Arguments)
	if err := r.Validate(); err != nil {
		return kit.Result{}, fmt.Errorf("invalid questions: %w", err)
	}
	return kit.Result{Ask: r}, nil
}
