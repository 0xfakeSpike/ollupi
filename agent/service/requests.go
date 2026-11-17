package service

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
)

type Request struct {
	Thread db.ThreadRow
	Item   db.ItemRow
}

func (sv *Service) PendingRequests(user, threadID string) ([]Request, error) {
	if err := sv.owned(user, threadID); err != nil {
		return nil, err
	}
	st, err := sv.cfg.DB.ThreadState(threadID)
	if err != nil {
		return nil, fmt.Errorf("service: thread %s: %w", threadID, err)
	}
	out := requestsOf(st)
	turn, active := st.ActiveTurn()
	if !active {
		return out, nil
	}
	running, err := sv.cfg.DB.RunningSubagents(turn.ID)
	if err != nil {
		return nil, fmt.Errorf("service: the subagents of thread %s: %w", threadID, err)
	}
	for _, id := range running {
		child, err := sv.cfg.DB.ThreadState(id)
		if err != nil {
			return nil, fmt.Errorf("service: thread %s: %w", id, err)
		}
		out = append(out, requestsOf(child)...)
	}
	return byRequestTime(out), nil
}

func (sv *Service) AllPendingRequests(user string) ([]Request, error) {
	ids, err := sv.cfg.DB.ThreadsWaitingOnUser(user)
	if err != nil {
		return nil, fmt.Errorf("service: threads waiting on the user: %w", err)
	}
	var out []Request
	for _, id := range ids {
		st, err := sv.cfg.DB.ThreadState(id)
		switch {
		case errors.Is(err, db.ErrNotFound):
		case err != nil:
			return nil, fmt.Errorf("service: thread %s: %w", id, err)
		default:
			out = append(out, requestsOf(st)...)
		}
	}
	return byRequestTime(out), nil
}

func requestsOf(st db.ThreadState) []Request {
	var out []Request
	for _, it := range st.PendingRequests() {
		out = append(out, Request{Thread: st.Thread, Item: it})
	}
	return out
}

func byRequestTime(requests []Request) []Request {
	sort.SliceStable(requests, func(i, j int) bool { return requestedAt(requests[i].Item).Before(requestedAt(requests[j].Item)) })
	return requests
}

func requestedAt(it db.ItemRow) time.Time {
	if it.Approval != nil {
		return it.Approval.RequestedAt
	}
	return it.StartedAt
}

func (sv *Service) ResolveApproval(user, threadID, approvalID string, decision db.Decision, decidedBy string) error {
	if err := sv.owned(user, threadID); err != nil {
		return err
	}
	stored, err := sv.respond(threadID, func(it db.ItemRow) bool { return it.WaitsForApproval() && it.Approval.ID == approvalID }, func(it db.ItemRow) (db.ItemRow, error) {
		if available := it.Approval.AvailableDecisions(); !slices.Contains(available, decision) {
			return db.ItemRow{}, fmt.Errorf("service: %w: %q is not one of %q", ErrInvalidDecision, decision, available)
		}
		now := time.Now().UTC()
		it.Approval.Decision, it.Approval.DecidedBy, it.Approval.DecidedAt = decision, decidedBy, &now
		return it, nil
	})
	if err != nil {
		return err
	}
	defer sv.o.Wake(threadID)
	// The rule is kept once the approval is: an approval refused, because the thread no longer waited on it, widens nothing.
	if decision == db.DecisionAcceptAlways {
		if err := sv.allowAlways(threadID, stored.Approval.Suggested); err != nil {
			return fmt.Errorf("service: the call was approved, but its rule was not kept: %w", err)
		}
	}
	return nil
}

func (sv *Service) AnswerUserInput(user, threadID, itemID string, answers userinput.Answers) error {
	if err := sv.owned(user, threadID); err != nil {
		return err
	}
	return sv.answer(threadID, itemID, func(it db.ItemRow) (db.ItemRow, error) {
		req, err := userinput.Decode(it.Await.Request)
		if err != nil {
			return db.ItemRow{}, err
		}
		if err := req.Check(answers, maxAnswerRunes); err != nil {
			return db.ItemRow{}, fmt.Errorf("service: %w: %v", ErrInvalidAnswer, err)
		}
		result, _, _ := db.ResultOf(tools.Result{Output: answers}) // strings alone: it cannot fail
		now := time.Now().UTC()
		it.Result, it.Status, it.CompletedAt = result, db.ItemCompleted, &now
		return it, nil
	})
}

func (sv *Service) SkipUserInput(user, threadID, itemID string) error {
	if err := sv.owned(user, threadID); err != nil {
		return err
	}
	return sv.answer(threadID, itemID, func(it db.ItemRow) (db.ItemRow, error) {
		now := time.Now().UTC()
		it.Status, it.Error, it.CompletedAt = db.ItemDeclined, "the user skipped these questions; go on without their answers", &now
		return it, nil
	})
}

// The thread's state is read before the answer is made, so the database refuses it with db.ErrNoSuchRequest
// if the thread no longer waits on that request.
func (sv *Service) respond(threadID string, match func(db.ItemRow) bool, apply func(db.ItemRow) (db.ItemRow, error)) (db.ItemRow, error) {
	st, err := sv.cfg.DB.ThreadState(threadID)
	if err != nil {
		return db.ItemRow{}, fmt.Errorf("service: thread %s: %w", threadID, err)
	}
	pending := st.PendingRequests()
	i := slices.IndexFunc(pending, match)
	if i < 0 {
		return db.ItemRow{}, db.ErrNoSuchRequest
	}
	activeTurn, _ := st.ActiveTurn()
	answered, err := apply(pending[i])
	if err != nil {
		return db.ItemRow{}, err
	}
	stored, err := sv.cfg.DB.AnswerRequest(threadID, answered)
	if err != nil {
		return db.ItemRow{}, err
	}
	sv.ev.Item(st.Thread, activeTurn, stored, false)
	sv.ev.Status(threadID)
	return stored, nil
}

func (sv *Service) answer(threadID, itemID string, apply func(db.ItemRow) (db.ItemRow, error)) error {
	if _, err := sv.respond(threadID, func(it db.ItemRow) bool { return it.ID == itemID && !it.WaitsForApproval() }, apply); err != nil {
		return err
	}
	sv.o.Wake(threadID)
	return nil
}
