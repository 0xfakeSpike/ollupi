package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

// ToolCancelBackground stops a background call of the conversation.
const ToolCancelBackground = "cancel_background"

// ToolWaitForBackground waits for background calls of the conversation to end.
const ToolWaitForBackground = "wait_for_background"

type waitForBackground struct {
	Background []string `json:"background" jsonschema:"The ids of the background calls to wait for, as their starts returned them."`
}

func (waitForBackground) Constrain(s *jsonschema.Schema) {
	s.Properties["background"].MinItems = jsonschema.Ptr(1)
	s.Properties["background"].Items.MinLength = jsonschema.Ptr(1)
}

// backgroundTools are the tools a conversation keeps its background calls with.
type backgroundTools struct{ a *Agent }

func (backgroundTools) Name() (string, string) {
	return "background", "Keeps the background calls of this conversation."
}

type cancelBackground struct {
	Background string `json:"background" jsonschema:"The id of the background call, as its start returned it."`
}

func (cancelBackground) Constrain(s *jsonschema.Schema) {
	s.Properties["background"].MinLength = jsonschema.Ptr(1)
}

func (backgroundTools) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{
		Name: ToolWaitForBackground,
		Description: "Wait until background calls of this conversation have ended, when you need their results before going on. " +
			"The turn waits without holding anything; it goes on once every call named has ended, with their results.",
		InputSchema: tools.InputSchema[waitForBackground](),
		Annotations: &tools.Annotations{Title: "Wait for background calls", ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
	}, {
		Name:        ToolCancelBackground,
		Description: "Stop a background call of this conversation that is still running. Its result does not come back.",
		InputSchema: tools.InputSchema[cancelBackground](),
		Annotations: &tools.Annotations{Title: "Cancel background call", DestructiveHint: tools.Hint(false), OpenWorldHint: tools.Hint(false)},
	}}, nil
}

func (b backgroundTools) Invoke(ctx context.Context, tool string, arguments map[string]any) (tools.Result, error) {
	if tool == ToolWaitForBackground {
		return b.wait(ctx, arguments)
	}
	args, err := tools.DecodeArguments[cancelBackground](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	call, err := b.a.cfg.DB.BackgroundCall(args.Background)
	if errors.Is(err, repo.ErrNotFound) || err == nil && call.ThreadID != tools.CallerOf(ctx).ThreadID {
		return tools.Result{}, fmt.Errorf("this conversation has no background call %q", args.Background)
	}
	if err != nil {
		return tools.Result{}, err
	}
	cancelled, err := b.a.cancelBackground(call.ID, "the agent cancelled the call")
	if err != nil {
		return tools.Result{}, err
	}
	if !cancelled {
		ended, _ := b.a.cfg.DB.BackgroundCall(call.ID)
		return tools.Result{}, fmt.Errorf("background call %s already ended: %s", call.ID, ended.Status)
	}
	return tools.Result{Output: map[string]any{"background": call.ID, "status": string(repo.BackgroundCancelled)}}, nil
}

func (b backgroundTools) wait(ctx context.Context, arguments map[string]any) (tools.Result, error) {
	args, err := tools.DecodeArguments[waitForBackground](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	for _, id := range args.Background {
		call, err := b.a.cfg.DB.BackgroundCall(id)
		if errors.Is(err, repo.ErrNotFound) || err == nil && call.ThreadID != tools.CallerOf(ctx).ThreadID {
			return tools.Result{}, fmt.Errorf("this conversation has no background call %q", id)
		}
		if err != nil {
			return tools.Result{}, err
		}
	}
	return tools.Awaiting(tools.AwaitBackground, args)
}
