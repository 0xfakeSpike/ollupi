package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
)

type job struct {
	it   repo.ItemRow
	args map[string]any
}

func (rt *Runtime) planCalls(ctx context.Context) ([]callAction, error) {
	var plan []callAction
	halt := ""
	for _, it := range rt.Items {
		if !it.IsCall() || it.Status != repo.ItemPending {
			continue
		}
		if halt != "" {
			plan = append(plan, callAction{kind: actionSkip, item: it, reason: "skipped: the turn stopped before this call was executed"})
			continue
		}
		h, waits, ready, err := rt.handleCall(ctx, it)
		if err != nil {
			return nil, err
		}
		switch {
		case waits:
			plan = append(plan, callAction{kind: actionWait})
			return plan, nil
		case ready != nil:
			kind := actionRun
			if rt.readOnly(ready.it.Tool) {
				kind = actionRunReadOnly
			}
			plan = append(plan, callAction{kind: kind, item: ready.it, job: ready})
		case h != "":
			plan = append(plan, callAction{kind: actionHalt, reason: h})
			halt = h
		}
	}
	return plan, nil
}

func (rt *Runtime) readOnly(tool string) bool {
	d, ok := rt.tool(tool)
	return ok && d.Annotations.ReadOnlyHint && !d.Serial
}

func (rt *Runtime) handleCall(ctx context.Context, it repo.ItemRow) (string, bool, *job, error) {
	var approved *repo.Approval
	if it.Approval != nil {
		changed, err := rt.changedSinceApproval(ctx, it)
		if err != nil {
			h, err := rt.refuse(it, err.Error())
			return h, false, nil, err
		}
		if !changed {
			return rt.decided(it)
		}
		approved, it.Approval = it.Approval, nil
	}
	if _, offered := rt.tool(it.Tool); !offered {
		reason := fmt.Sprintf("tool %q is not available in this turn; use an exact tool name from the tool list", it.Tool)
		if slices.ContainsFunc(rt.deferred, func(d tools.ToolDescriptor) bool { return d.Name == it.Tool }) {
			reason = fmt.Sprintf("tool %q is not loaded yet; call %s with its name first", it.Tool, tools.ToolLoadTools)
		}
		h, err := rt.refuse(it, reason)
		return h, false, nil, err
	}
	args, err := callArguments(it)
	if err != nil {
		h, err := rt.refuse(it, err.Error())
		return h, false, nil, err
	}
	descriptor, _ := rt.tool(it.Tool)
	if err := tools.ValidateArguments(args, descriptor.InputSchema); err != nil {
		h, err := rt.refuse(it, fmt.Sprintf("invalid arguments for %s: %v", it.Tool, err))
		return h, false, nil, err
	}
	if rt.toolCalls() >= maxToolCalls {
		if err := rt.fail(it, "skipped: the tool call limit for this turn was reached"); err != nil {
			return "", false, nil, err
		}
		return fmt.Sprintf("reached the tool call limit (%d)", maxToolCalls), false, nil, nil
	}
	if it.PreparedExecution == nil {
		prepared, err := rt.Registry.Prepare(ctx, it.Tool, args)
		if err != nil {
			h, err := rt.refuse(it, err.Error())
			return h, false, nil, err
		}
		if prepared != nil {
			it.PreparedExecution = prepared
			stored, err := rt.record(it)
			if err != nil {
				return "", false, nil, err
			}
			it = stored
		}
	}
	content, hasContent, err := rt.Registry.PermissionContent(ctx, it.Tool, args)
	if err != nil {
		h, err := rt.refuse(it, err.Error())
		return h, false, nil, err
	}
	annotations := tools.Annotations{}
	if it.Annotations != nil {
		annotations = *it.Annotations
	}
	call := permission.Call{Tool: it.Tool, Annotations: annotations, Content: content, HasContent: hasContent, Prepared: it.PreparedExecution}
	decision := rt.Permissions.Decide(call, slices.Concat(rt.History, rt.Items))
	if decision.Verdict == permission.Deny {
		h, err := rt.refuse(it, decision.Reason)
		return h, false, nil, err
	}
	if decision.Verdict == permission.Ask {
		it.Approval = decision.Approval(uuid.NewString(), call, time.Now().UTC())
		if approved != nil {
			it.Approval.Reason = strings.TrimSpace(fmt.Sprintf("This call changed after you approved it, which was for: %s. %s", approved.Content, it.Approval.Reason))
		}
		if _, err := rt.record(it); err != nil {
			return "", false, nil, err
		}
		return "", true, nil, nil
	}
	return "", false, &job{it: it, args: args}, nil
}

func (rt *Runtime) changedSinceApproval(ctx context.Context, it repo.ItemRow) (bool, error) {
	if it.Approval.Decision == repo.DecisionDecline {
		return false, nil
	}
	args, err := callArguments(it)
	if err != nil {
		return false, nil
	}
	content, ok, err := rt.Registry.PermissionContent(ctx, it.Tool, args)
	if err != nil {
		return false, err
	}
	return ok && content.Text != it.Approval.Content, nil
}

func (rt *Runtime) decided(it repo.ItemRow) (string, bool, *job, error) {
	switch it.Approval.Decision {
	case repo.DecisionAccept, repo.DecisionAcceptForSession, repo.DecisionAcceptAlways:
		args, err := callArguments(it)
		if err != nil {
			h, err := rt.refuse(it, fmt.Sprintf("the approved call has invalid arguments: %v", err))
			return h, false, nil, err
		}
		return "", false, &job{it: it, args: args}, nil
	case repo.DecisionDecline:
		if _, err := rt.complete(withError(it, "the user declined this call"), repo.ItemDeclined); err != nil {
			return "", false, nil, err
		}
		return fmt.Sprintf("the user declined %s", it.Tool), false, nil, nil
	default:
		return "", false, nil, fmt.Errorf("runtime: call %s has no approval decision", it.ID)
	}
}

func callArguments(it repo.ItemRow) (map[string]any, error) {
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
	for _, it := range rt.Items {
		if !it.IsCall() {
			continue
		}
		if it.Status == repo.ItemPending && it.Approval == nil {
			continue
		}
		n++
	}
	return n
}

func (rt *Runtime) consecutiveFailures() int {
	n := 0
	items := rt.Items
	for i := len(items) - 1; i >= 0; i-- {
		if !items[i].IsCall() || items[i].Status == repo.ItemPending || items[i].Status == repo.ItemInProgress {
			continue
		}
		if items[i].Status != repo.ItemFailed && items[i].Status != repo.ItemDeclined {
			break
		}
		n++
	}
	return n
}
