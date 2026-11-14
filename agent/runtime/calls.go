package runtime

import (
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

const (
	maxModelCalls          = 200
	maxToolCalls           = 400
	maxConsecutiveFailures = 10

	turnStopped  = "skipped: the turn stopped before this call was executed"
	callsUsedUp  = "skipped: the tool call limit for this turn was reached"
	userDeclined = "the user declined this call"
)

// A Job is a call to run, with its arguments for the tool.
type Job struct {
	Item repo.ItemRow
	Args map[string]any
	// Background is what the call asked with ArgBackground.
	Background bool
	ReadOnly   bool
}

// A callPlan is what the pending calls come to: the items that record what was decided on them, the calls to run in
// their order, and why tool use stops, when it does.
type callPlan struct {
	records []repo.ItemRow
	run     []Job
	halt    string
}

// firstRun is the calls that run first: read-only calls in a row run together, any other call on its own.
func (p callPlan) firstRun() []Job {
	if len(p.run) == 0 || !p.run[0].ReadOnly {
		return p.run[:min(1, len(p.run))]
	}
	n := 1
	for n < len(p.run) && p.run[n].ReadOnly {
		n++
	}
	return p.run[:n]
}

// planCalls decides on the pending calls in their order, from the latest stop of tool use on. A call is refused when
// it is not offered, its arguments are invalid, the permissions deny it or it cannot be prepared; one that needs the
// user's approval asks for it, and the calls after it wait with it. Tool use stops when the user declines a call, the
// turn makes too many calls, or the calls in a row fail too often; the pending calls after the one that stops it are
// skipped. A call that has to be inspected first returns Inspect, before anything is decided.
func (s State) planCalls(of Offer) (callPlan, *Inspect, error) {
	items := slices.Clone(s.Items)
	var p callPlan
	failures := 0
	settle := func(i int, it repo.ItemRow) {
		items[i] = it
		if it.Status == repo.ItemFailed || it.Status == repo.ItemDeclined {
			failures++
		} else {
			failures = 0
		}
		if p.halt != "" {
			return
		}
		switch {
		case it.Status == repo.ItemDeclined && it.Approval != nil && it.Approval.Decision == repo.DecisionDecline:
			p.halt = fmt.Sprintf("the user declined %s", it.Tool)
		case it.Error == callsUsedUp:
			p.halt = fmt.Sprintf("reached the tool call limit (%d)", maxToolCalls)
		case failures >= maxConsecutiveFailures:
			p.halt = fmt.Sprintf("%d consecutive invalid or failed tool calls; last: %s", failures, it.Error)
		}
	}
	record := func(i int, it repo.ItemRow) {
		p.records = append(p.records, it)
		settle(i, it)
	}
	refuse := func(i int, it repo.ItemRow, reason string) {
		record(i, completed(withError(it, reason), repo.ItemFailed))
	}
	for i := lastIndex(items, isStopNotice) + 1; i < len(items); i++ {
		it := items[i]
		switch {
		case !it.IsCall() || it.Status == repo.ItemInProgress:
			continue
		case it.WaitsForApproval():
			return p, nil, nil
		case it.Status != repo.ItemPending:
			settle(i, it)
			continue
		case p.halt != "":
			refuse(i, it, turnStopped)
			continue
		}
		var approved *repo.Approval
		if it.Approval != nil {
			if it.Approval.Decision == repo.DecisionDecline {
				record(i, completed(withError(it, userDeclined), repo.ItemDeclined))
				continue
			}
			args, background, err := of.arguments(it)
			if err != nil {
				refuse(i, it, fmt.Sprintf("the approved call has invalid arguments: %v", err))
				continue
			}
			in, ok := s.Inspections[it.ID]
			if !ok {
				return callPlan{}, &Inspect{Call: it, Args: args}, nil
			}
			if in.Err != nil {
				refuse(i, it, in.Err.Error())
				continue
			}
			if !in.HasContent || in.Content.Text == it.Approval.Content {
				switch it.Approval.Decision {
				case repo.DecisionAccept, repo.DecisionAcceptForSession, repo.DecisionAcceptAlways:
					p.run = append(p.run, Job{Item: it, Args: args, Background: background, ReadOnly: of.readOnly(it.Tool)})
					continue
				default:
					return callPlan{}, nil, fmt.Errorf("runtime: call %s has no approval decision", it.ID)
				}
			}
			approved, it.Approval = it.Approval, nil
		}
		if _, offered := of.Tool(it.Tool); !offered {
			reason := fmt.Sprintf("tool %q is not available in this turn; use an exact tool name from the tool list", it.Tool)
			if slices.ContainsFunc(of.Deferred, func(d tools.ToolDescriptor) bool { return d.Name == it.Tool }) {
				reason = fmt.Sprintf("tool %q is not loaded yet; call %s with its name first", it.Tool, tools.ToolLoadTools)
			}
			refuse(i, it, reason)
			continue
		}
		args, background, err := of.arguments(it)
		if err != nil {
			refuse(i, it, err.Error())
			continue
		}
		descriptor, _ := of.Tool(it.Tool)
		if err := tools.ValidateArguments(args, descriptor.InputSchema); err != nil {
			refuse(i, it, fmt.Sprintf("invalid arguments for %s: %v", it.Tool, err))
			continue
		}
		if toolCalls(items) >= maxToolCalls {
			refuse(i, it, callsUsedUp)
			continue
		}
		in, ok := s.Inspections[it.ID]
		if !ok {
			return callPlan{}, &Inspect{Call: it, Args: args, Prepare: it.PreparedExecution == nil}, nil
		}
		if in.Err != nil {
			refuse(i, it, in.Err.Error())
			continue
		}
		annotations := tools.Annotations{}
		if it.Annotations != nil {
			annotations = *it.Annotations
		}
		call := permission.Call{Tool: it.Tool, Annotations: annotations, Content: in.Content, HasContent: in.HasContent, Prepared: it.PreparedExecution}
		decision := s.Permissions.Decide(call, slices.Concat(s.History, items))
		switch decision.Verdict {
		case permission.Deny:
			refuse(i, it, decision.Reason)
		case permission.Ask:
			it.Approval = decision.Approval(uuid.NewString(), call, time.Now().UTC())
			if approved != nil {
				it.Approval.Reason = strings.TrimSpace(fmt.Sprintf("This call changed after you approved it, which was for: %s. %s", approved.Content, it.Approval.Reason))
			}
			p.records = append(p.records, it)
			return p, nil, nil
		default:
			p.run = append(p.run, Job{Item: it, Args: args, Background: background, ReadOnly: of.readOnly(it.Tool)})
		}
	}
	return p, nil, nil
}

func (o Offer) readOnly(tool string) bool {
	d, ok := o.Tool(tool)
	return ok && d.Annotations.ReadOnlyHint && !d.Serial
}

// arguments are the call's arguments for its tool and whether it asked to run in the background.
func (o Offer) arguments(it repo.ItemRow) (map[string]any, bool, error) {
	args, err := callArguments(it)
	if err != nil {
		return nil, false, err
	}
	d, _ := o.Tool(it.Tool)
	background, err := d.TakeBackground(args)
	if err != nil {
		return nil, false, err
	}
	return args, background, nil
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

// toolCalls counts the turn's calls that were made, or are asked about.
func toolCalls(items []repo.ItemRow) int {
	n := 0
	for _, it := range items {
		if it.IsCall() && (it.Status != repo.ItemPending || it.Approval != nil) {
			n++
		}
	}
	return n
}
