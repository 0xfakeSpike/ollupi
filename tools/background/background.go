// Package background gives an agent the tools it keeps its background calls with: waiting for them, and cancelling
// one. Starting and running a call in the background is the orchestrator's; this package reads what it needs of the
// calls from the database itself.
package background

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
)

const (
	// ProviderName is the provider of the tools.
	ProviderName = "background"
	// ToolWait waits for background calls of the conversation to end.
	ToolWait = "wait_for_background"
	// ToolCancel stops a background call of the conversation.
	ToolCancel = "cancel_background"
)

// Wait is what a call of ToolWait waits on: the background calls it names.
type Wait struct {
	Background []string `json:"background" jsonschema:"The ids of the background calls to wait for, as their starts returned them."`
}

func (Wait) Constrain(s *jsonschema.Schema) {
	s.Properties["background"].MinItems = jsonschema.Ptr(1)
	s.Properties["background"].Items.MinLength = jsonschema.Ptr(1)
}

type cancelArgs struct {
	Background string `json:"background" jsonschema:"The id of the background call, as its start returned it."`
}

func (cancelArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["background"].MinLength = jsonschema.Ptr(1)
}

// Tools provides the tools. Cancel ends a running call as cancelled and stops it, reporting false for one that had
// already ended; it is the orchestrator's, which runs the calls.
type Tools struct {
	db     *db.DB
	cancel func(id, why string) (bool, error)
}

func New(store *db.DB, cancel func(id, why string) (bool, error)) *Tools {
	return &Tools{db: store, cancel: cancel}
}

func (*Tools) Name() (string, string) {
	return ProviderName, "Keeps the background calls of this conversation."
}

func (*Tools) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{
		Name: ToolWait,
		Description: "Wait until background calls of this conversation have ended, when you need their results before going on. " +
			"The turn waits without holding anything; it goes on once every call named has ended, with their results.",
		InputSchema: tools.InputSchema[Wait](),
		Annotations: &tools.Annotations{Title: "Wait for background calls", ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
	}, {
		Name:        ToolCancel,
		Description: "Stop a background call of this conversation that is still running. Its result does not come back.",
		InputSchema: tools.InputSchema[cancelArgs](),
		Annotations: &tools.Annotations{Title: "Cancel background call", DestructiveHint: tools.Hint(false), OpenWorldHint: tools.Hint(false)},
	}}, nil
}

func (t *Tools) Invoke(_ context.Context, caller tools.Caller, tool string, arguments map[string]any) (tools.Result, error) {
	if tool == ToolWait {
		args, err := tools.DecodeArguments[Wait](arguments)
		if err != nil {
			return tools.Result{}, err
		}
		for _, id := range args.Background {
			if _, err := t.callOf(caller, id); err != nil {
				return tools.Result{}, err
			}
		}
		return tools.Awaiting(tools.AwaitBackground, args)
	}
	args, err := tools.DecodeArguments[cancelArgs](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	call, err := t.callOf(caller, args.Background)
	if err != nil {
		return tools.Result{}, err
	}
	cancelled, err := t.cancel(call.ID, "the agent cancelled the call")
	if err != nil {
		return tools.Result{}, err
	}
	if !cancelled {
		ended, _ := t.db.BackgroundCall(call.ID)
		return tools.Result{}, fmt.Errorf("background call %s already ended: %s", call.ID, ended.Status)
	}
	return tools.Result{Output: map[string]any{"background": call.ID, "status": string(db.BackgroundCancelled)}}, nil
}

// callOf is the caller's conversation's background call of that id.
func (t *Tools) callOf(caller tools.Caller, id string) (db.BackgroundCallRow, error) {
	call, err := t.db.BackgroundCall(id)
	if errors.Is(err, db.ErrNotFound) || err == nil && call.ThreadID != caller.ThreadID {
		return db.BackgroundCallRow{}, fmt.Errorf("this conversation has no background call %q", id)
	}
	return call, err
}

// Fulfilled is a call that waits for background calls, completed with how each ended, once they all have; their
// results come back to the turn as backgroundResult items. It reports false while one still runs.
func Fulfilled(store *db.DB, call db.ItemRow) (db.ItemRow, bool, error) {
	var req Wait
	if err := json.Unmarshal(call.Await.Request, &req); err != nil {
		return db.ItemRow{}, false, fmt.Errorf("background: wait %s: %w", call.ID, err)
	}
	ended := map[string]string{}
	for _, id := range req.Background {
		bc, err := store.BackgroundCall(id)
		if err != nil {
			return db.ItemRow{}, false, fmt.Errorf("background: call %s: %w", id, err)
		}
		if bc.Status == db.BackgroundRunning {
			return db.ItemRow{}, false, nil
		}
		ended[id] = string(bc.Status)
	}
	result, _, err := db.ResultOf(tools.Result{Output: map[string]any{"ended": ended}})
	if err != nil {
		return db.ItemRow{}, false, err
	}
	now := time.Now().UTC()
	call.Status, call.Result, call.CompletedAt = db.ItemCompleted, result, &now
	return call, true, nil
}

// WaitInPlace is the call that keeps a subagent from reporting while background calls of its own still run: a wait
// for them as a call of a response of its own, so that the subagent goes on with their results and reports with
// them. It reports false when none runs.
func WaitInPlace(store *db.DB, threadID, turnID string) (db.ItemRow, bool, error) {
	running, err := store.RunningBackgroundCalls(threadID)
	if err != nil || len(running) == 0 {
		return db.ItemRow{}, false, err
	}
	request, err := json.Marshal(Wait{Background: running})
	if err != nil {
		return db.ItemRow{}, false, err
	}
	return db.ItemRow{
		ID: uuid.NewString(), TurnID: turnID, Type: db.ItemToolCall, Status: db.ItemInProgress,
		CallID: "wait_" + uuid.NewString(), ResponseID: uuid.NewString(), Tool: ToolWait, Arguments: string(request),
		Await: &tools.Await{By: tools.AwaitBackground, Request: request}, StartedAt: time.Now().UTC(),
	}, true, nil
}
