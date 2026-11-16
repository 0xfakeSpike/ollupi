package service

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
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
