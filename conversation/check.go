package conversation

import (
	"errors"
	"fmt"
	"slices"
)

var ErrRefused = errors.New("conversation: the entry does not follow from the conversation")

// Check says whether e may be appended to the conversation the view folds: whatever its writer read before, an entry
// that does not follow from the conversation as it is now is refused.
func Check(v View, e Entry) error {
	if e.Body == nil {
		return refuse("an entry without a body")
	}
	if _, ok := e.Body.(TurnQueued); ok {
		if _, exists := v.TurnOf(e.Turn); exists || e.Turn == "" {
			return refuse("turn %q is queued twice", e.Turn)
		}
		return nil
	}
	turn, ok := v.TurnOf(e.Turn)
	if !ok {
		return refuse("no turn %q", e.Turn)
	}
	if turn.Ended != nil {
		return refuse("turn %s has ended", turn.ID)
	}
	active, _ := v.Active()
	_, queuedMessage := e.Body.(UserMessage)
	_, ending := e.Body.(TurnEnded)
	if turn.ID != active.ID && !queuedMessage && !ending {
		return refuse("turn %s waits behind turn %s", turn.ID, active.ID)
	}
	switch b := e.Body.(type) {
	case TurnEnded:
		if open := slices.IndexFunc(v.ItemsOf(turn.ID), Item.open); open >= 0 {
			return refuse("turn %s still has call %s open", turn.ID, v.ItemsOf(turn.ID)[open].ID)
		}
		return nil
	case ModelResponded:
		if e.Item != "" || b.Response == "" {
			return refuse("a response is the turn's own and has an id")
		}
		return nil
	case UserMessage, AgentMessage, Reasoning, ModelFailed, ToolCall, Compacted, Recalled:
		return newItem(v, e)
	case ChildReported:
		if err := newItem(v, e); err != nil {
			return err
		}
		launch, ok := v.ItemOf(b.Launch)
		if !ok || launch.Child != b.Child || launch.Reported {
			return refuse("call %s launched no subagent %s that has not reported", b.Launch, b.Child)
		}
		return nil
	}
	it, ok := v.ItemOf(e.Item)
	if !ok || it.Turn != turn.ID || it.Kind != ItemToolCall {
		return refuse("turn %s has no call %q", turn.ID, e.Item)
	}
	switch b := e.Body.(type) {
	case ApprovalAsked:
		if it.Status != Pending || it.Approval != nil || b.Approval == "" {
			return refuse("call %s cannot ask for approval now", it.ID)
		}
	case Decided:
		if !it.waitsForApproval() || !slices.Contains(it.Approval.Decisions(), b.Decision) {
			return refuse("call %s does not wait for the decision %q", it.ID, b.Decision)
		}
	case ToolStarted:
		if it.Status != Pending || it.waitsForApproval() {
			return refuse("call %s cannot start now", it.ID)
		}
	case InputAsked:
		if it.Status != InProgress || it.Input != nil {
			return refuse("call %s cannot ask the user now", it.ID)
		}
	case Answered:
		if !it.waitsForInput() {
			return refuse("call %s does not wait for an answer", it.ID)
		}
	case ToolResult:
		if !it.open() || it.waitsForInput() {
			return refuse("call %s cannot end now", it.ID)
		}
	case ChildLaunched:
		if it.Status != InProgress || it.Child != "" || b.Child == "" {
			return refuse("call %s cannot launch a subagent now", it.ID)
		}
	}
	return nil
}

func newItem(v View, e Entry) error {
	if e.Item == "" {
		return refuse("a %s needs an item id", KindOf(e.Body))
	}
	if _, exists := v.ItemOf(e.Item); exists {
		return refuse("item %s exists already", e.Item)
	}
	return nil
}

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrRefused}, args...)...)
}
