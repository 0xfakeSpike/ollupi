package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/0xfakeSpike/ollupi/tools/loader"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
)

type job struct {
	it   db.ItemRow
	args map[string]any
}

// A decision is what becomes of a pending call: it runs as job; or the turn waits for the user to approve it; or it
// stops the turn for halt, as one the user declined does; or, with none of these, it is done, as a refused call is.
type decision struct {
	job  *job
	wait bool
	halt string
}

func (rt *Runtime) readOnly(tool string) bool {
	d, ok := rt.Role.Tool(tool)
	return ok && d.Annotations.ReadOnlyHint && !d.Serial
}

// handleCall decides what becomes of a pending call, the calls waiting to run with it counting toward the turn's limit.
func (rt *Runtime) handleCall(ctx context.Context, it db.ItemRow, waiting int) (decision, error) {
	var approved *db.Approval
	if it.Approval != nil {
		changed, err := rt.changedSinceApproval(ctx, it)
		if err != nil {
			return rt.refuse(it, err.Error())
		}
		if !changed {
			return rt.decided(it)
		}
		approved, it.Approval = it.Approval, nil
	}
	if _, offered := rt.Role.Tool(it.Tool); !offered {
		reason := fmt.Sprintf("tool %q is not available in this turn; use an exact tool name from the tool list", it.Tool)
		if _, deferred := rt.Role.Offer(); slices.ContainsFunc(deferred, func(d tools.ToolDescriptor) bool { return d.Name == it.Tool }) {
			reason = fmt.Sprintf("tool %q is not loaded yet; call %s with its name first", it.Tool, loader.Tool)
		}
		return rt.refuse(it, reason)
	}
	args, err := callArguments(it)
	if err != nil {
		return rt.refuse(it, err.Error())
	}
	if rt.toolCalls()+waiting >= maxToolCalls {
		if err := rt.fail(it, "skipped: the tool call limit for this turn was reached"); err != nil {
			return decision{}, err
		}
		return decision{halt: fmt.Sprintf("reached the tool call limit (%d)", maxToolCalls)}, nil
	}
	if it.PreparedExecution == nil {
		prepared, err := rt.Registry.Prepare(ctx, rt.caller(it), it.Tool, args)
		if err != nil {
			return rt.refuse(it, err.Error())
		}
		if prepared != nil {
			it.PreparedExecution = prepared
			stored, err := rt.record(it)
			if err != nil {
				return decision{}, err
			}
			it = stored
		}
	}
	content, hasContent, err := rt.Registry.PermissionContent(ctx, rt.caller(it), it.Tool, args)
	if err != nil {
		return rt.refuse(it, err.Error())
	}
	annotations := tools.Annotations{}
	if it.Annotations != nil {
		annotations = *it.Annotations
	}
	call := permission.Call{Tool: it.Tool, Annotations: annotations, Content: content, HasContent: hasContent, Prepared: it.PreparedExecution}
	verdict := rt.Role.Decide(call)
	if verdict.Verdict == permission.Deny {
		return rt.refuse(it, verdict.Reason)
	}
	if verdict.Verdict == permission.Ask {
		it.Approval = verdict.Approval(uuid.NewString(), call, time.Now().UTC())
		if approved != nil {
			it.Approval.Reason = strings.TrimSpace(fmt.Sprintf("This call changed after you approved it, which was for: %s. %s", approved.Content, it.Approval.Reason))
		}
		if _, err := rt.record(it); err != nil {
			return decision{}, err
		}
		slog.Info("approval requested", "thread", rt.Role.Thread().ID, "turn", rt.Role.Turn().ID, "item", it.ID, "tool", it.Tool)
		return decision{wait: true}, nil
	}
	return decision{job: &job{it: it, args: args}}, nil
}

func (rt *Runtime) changedSinceApproval(ctx context.Context, it db.ItemRow) (bool, error) {
	if it.Approval.Decision == db.DecisionDecline {
		return false, nil
	}
	args, err := callArguments(it)
	if err != nil {
		return false, nil
	}
	content, ok, err := rt.Registry.PermissionContent(ctx, rt.caller(it), it.Tool, args)
	if err != nil {
		return false, err
	}
	return ok && content.Text != it.Approval.Content, nil
}

// decided is what becomes of a call the user decided on, unchanged since.
func (rt *Runtime) decided(it db.ItemRow) (decision, error) {
	switch it.Approval.Decision {
	case db.DecisionAccept, db.DecisionAcceptForSession, db.DecisionAcceptAlways:
		args, err := callArguments(it)
		if err != nil {
			return rt.refuse(it, fmt.Sprintf("the approved call has invalid arguments: %v", err))
		}
		return decision{job: &job{it: it, args: args}}, nil
	case db.DecisionDecline:
		if _, err := rt.complete(withError(it, "the user declined this call"), db.ItemDeclined); err != nil {
			return decision{}, err
		}
		return decision{halt: fmt.Sprintf("the user declined %s", it.Tool)}, nil
	default:
		return decision{}, fmt.Errorf("runtime: call %s has no approval decision", it.ID)
	}
}

func callArguments(it db.ItemRow) (map[string]any, error) {
	args := map[string]any{}
	if strings.TrimSpace(it.Arguments) == "" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(it.Arguments), &args); err != nil {
		return nil, fmt.Errorf("arguments are not a valid JSON object: %v", err)
	}
	if args == nil {
		return nil, fmt.Errorf("arguments must be a JSON object, not null")
	}
	return args, nil
}

func (rt *Runtime) toolCalls() int {
	n := 0
	for _, it := range rt.Role.Items() {
		if !it.IsCall() {
			continue
		}
		if it.Status == db.ItemPending && it.Approval == nil {
			continue
		}
		n++
	}
	return n
}

func (rt *Runtime) consecutiveFailures() int {
	n := 0
	items := rt.Role.Items()
	for i := len(items) - 1; i >= 0; i-- {
		if !items[i].IsCall() || items[i].Status == db.ItemPending || items[i].Status == db.ItemInProgress {
			continue
		}
		if items[i].Status != db.ItemFailed && items[i].Status != db.ItemDeclined {
			break
		}
		n++
	}
	return n
}
