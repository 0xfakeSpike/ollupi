package role

import (
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

type step int

const (
	stepRespond step = iota
	stepCalls
	stepConclude
	stepWait
	stepDone
)

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
