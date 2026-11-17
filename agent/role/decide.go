package role

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/loader"
	"github.com/google/uuid"
)

func (rt *Runtime) handleCall(ctx context.Context, it db.ItemRow) (*db.ItemRow, error) {
	var approved *db.Approval
	if it.Approval != nil {
		changed, err := rt.changedSinceApproval(ctx, it)
		if err != nil {
			return nil, rt.fail(it, err.Error())
		}
		if !changed {
			return rt.decided(it)
		}
		approved, it.Approval = it.Approval, nil
	}
	if _, offered := rt.tool(it.Tool); !offered {
		reason := fmt.Sprintf("tool %q is not available in this turn; use an exact tool name from the tool list", it.Tool)
		_, deferred := rt.Offer()
		notLoaded := slices.ContainsFunc(deferred, func(d tools.ToolDescriptor) bool { return d.Name == it.Tool })
		if notLoaded {
			reason = fmt.Sprintf("tool %q is not loaded yet; call %s with its name first", it.Tool, loader.Tool)
		}
		return nil, rt.fail(it, reason)
	}
	args, err := callArguments(it)
	if err != nil {
		return nil, rt.fail(it, err.Error())
	}
	if it.PreparedExecution == nil {
		prepared, err := rt.registry.Prepare(ctx, rt.caller(it), it.Tool, args)
		if err != nil {
			return nil, rt.fail(it, err.Error())
		}
		if prepared != nil {
			it.PreparedExecution = prepared
			stored, err := rt.record(it)
			if err != nil {
				return nil, err
			}
			it = stored
		}
	}
	content, hasContent, err := rt.registry.PermissionContent(ctx, rt.caller(it), it.Tool, args)
	if err != nil {
		return nil, rt.fail(it, err.Error())
	}
	annotations := tools.Annotations{}
	if it.Annotations != nil {
		annotations = *it.Annotations
	}
	call := permission.Call{Tool: it.Tool, Annotations: annotations, Content: content, HasContent: hasContent, Prepared: it.PreparedExecution}
	verdict := rt.checker.Decide(call, rt.conversation())
	if verdict.Verdict == permission.Deny {
		return nil, rt.fail(it, verdict.Reason)
	}
	if verdict.Verdict == permission.Ask {
		it.Approval = verdict.Approval(uuid.NewString(), call, time.Now().UTC())
		if approved != nil {
			it.Approval.Reason = strings.TrimSpace(fmt.Sprintf("This call changed after you approved it, which was for: %s. %s", approved.Content, it.Approval.Reason))
		}
		if _, err := rt.record(it); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return &it, nil
}

func (rt *Runtime) changedSinceApproval(ctx context.Context, it db.ItemRow) (bool, error) {
	if it.Approval.Decision == db.DecisionDecline {
		return false, nil
	}
	args, _ := callArguments(it) // a call asks for approval only once its arguments were read
	content, ok, err := rt.registry.PermissionContent(ctx, rt.caller(it), it.Tool, args)
	if err != nil {
		return false, err
	}
	return ok && content.Text != it.Approval.Content, nil
}

func (rt *Runtime) decided(it db.ItemRow) (*db.ItemRow, error) {
	if it.Approval.Decision == db.DecisionDecline {
		_, err := rt.complete(withError(it, "the user declined this call"), db.ItemDeclined)
		return nil, err
	}
	return &it, nil
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
