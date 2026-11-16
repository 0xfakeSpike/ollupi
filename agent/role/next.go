package role

import (
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

// A step is what a run of the turn does next, as its items say.
type step int

const (
	// stepRespond asks the model for its next response.
	stepRespond step = iota
	// stepCalls decides and runs the pending calls.
	stepCalls
	// stepConclude asks the model for its final answer, tool use having stopped.
	stepConclude
	// stepWait leaves the turn waiting: on the user, or on what a call awaits.
	stepWait
	// stepDone ends the run: the model answered all the turn's input.
	stepDone
)

// next is what the run does next, as the turn's items are now, with the reason a stepConclude gives the model.
func (rt *Runtime) next() (step, string) {
	waiting := slices.ContainsFunc(rt.items, func(it db.ItemRow) bool { return it.WaitsForApproval() || it.Awaits() })
	pending := slices.ContainsFunc(rt.items, func(it db.ItemRow) bool { return it.IsCall() && it.Status == db.ItemPending })
	switch {
	case waiting:
		return stepWait, ""
	case pending:
		return stepCalls, ""
	case rt.answered():
		return stepDone, ""
	}
	if reason := rt.stop(0); reason != "" {
		return stepConclude, reason
	}
	if rt.responses() >= rt.role.maxModelCalls {
		return stepConclude, fmt.Sprintf("reached the model call limit (%d)", rt.role.maxModelCalls)
	}
	return stepRespond, ""
}

// stop is why tool use has stopped in the turn, or "" while calls may run, the batched calls about to run counting
// toward its limit. It holds, since the model last answered or the user last wrote, once the user declined a call or
// too many calls in a row failed; and in the whole turn once its calls reached their limit.
func (rt *Runtime) stop(batched int) string {
	from := rt.fresh()
	declined := slices.IndexFunc(rt.items[from:], func(it db.ItemRow) bool { return it.IsCall() && it.Status == db.ItemDeclined })
	if declined >= 0 {
		return fmt.Sprintf("the user declined %s", rt.items[from+declined].Tool)
	}
	calls := batched
	for _, it := range rt.items {
		if it.IsCall() && it.Status != db.ItemPending {
			calls++
		}
	}
	if calls >= rt.role.maxToolCalls {
		return fmt.Sprintf("reached the tool call limit (%d)", rt.role.maxToolCalls)
	}
	failed, last := 0, ""
	for i := len(rt.items) - 1; i >= from; i-- {
		it := rt.items[i]
		if !it.IsCall() || it.Status == db.ItemPending || it.Status == db.ItemInProgress {
			continue
		}
		if it.Status != db.ItemFailed {
			break
		}
		if failed == 0 {
			last = it.Error
		}
		failed++
	}
	if failed >= rt.role.maxConsecutiveFailures {
		return fmt.Sprintf("%d consecutive invalid or failed tool calls; last: %s", failed, last)
	}
	return ""
}

// answered says whether the model's latest response is an answer, a message that called no tool, to all the input of
// the turn: none came after what its prompt was built from. A response that failed answered nothing.
func (rt *Runtime) answered() bool {
	latest := rt.latestResponse()
	answer, through := false, 0
	for _, it := range rt.items {
		if latest == "" || it.ResponseID != latest {
			continue
		}
		if it.IsCall() {
			return false
		}
		answer = answer || it.Type == db.ItemAgentMessage && it.Status == db.ItemCompleted
		if it.ContextStats != nil {
			through = it.ContextStats.ThroughSeq
		}
	}
	return answer && !slices.ContainsFunc(rt.items, func(it db.ItemRow) bool {
		return (it.Type == db.ItemUserMessage || it.Type == db.ItemAgentReport) && it.Seq > through
	})
}

// fresh is where the turn's items begin that a stop counts: after the model's latest answer, a completed message of a
// response that called no tool, or the user's latest message.
func (rt *Runtime) fresh() int {
	called := map[string]bool{}
	for _, it := range rt.items {
		if it.IsCall() {
			called[it.ResponseID] = true
		}
	}
	for i := len(rt.items) - 1; i >= 0; i-- {
		if it := rt.items[i]; it.Type == db.ItemUserMessage || it.Type == db.ItemAgentMessage && it.Status == db.ItemCompleted && !called[it.ResponseID] {
			return i + 1
		}
	}
	return 0
}

// latestResponse is the ID of the model's latest response in the turn, if any; a compaction is none.
func (rt *Runtime) latestResponse() string {
	for i := len(rt.items) - 1; i >= 0; i-- {
		if it := rt.items[i]; it.ResponseID != "" && it.Type != db.ItemContextCompaction {
			return it.ResponseID
		}
	}
	return ""
}

func (rt *Runtime) responses() int {
	ids := map[string]bool{}
	for _, it := range rt.items {
		if it.ResponseID != "" && it.Type != db.ItemContextCompaction {
			ids[it.ResponseID] = true
		}
	}
	return len(ids)
}
