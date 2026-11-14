package orchestrator

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
)

type Request struct {
	Thread repo.ThreadRow
	Item   repo.ItemRow
}

func (o *Orchestrator) PendingRequests(user, threadID string) ([]Request, error) {
	if err := o.owned(user, threadID); err != nil {
		return nil, err
	}
	st, err := o.cfg.DB.ThreadState(threadID)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: thread %s: %w", threadID, err)
	}
	out := requestsOf(st)
	for _, call := range st.AgentCalls() {
		child, err := o.cfg.DB.ThreadState(call.Await.Handle)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: thread %s: %w", call.Await.Handle, err)
		}
		out = append(out, requestsOf(child)...)
	}
	return byRequestTime(out), nil
}

func (o *Orchestrator) AllPendingRequests(user string) ([]Request, error) {
	ids, err := o.cfg.DB.ThreadsWaitingOnUser(user)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: threads waiting on the user: %w", err)
	}
	var out []Request
	for _, id := range ids {
		st, err := o.cfg.DB.ThreadState(id)
		switch {
		case errors.Is(err, repo.ErrNotFound):
		case err != nil:
			return nil, fmt.Errorf("orchestrator: thread %s: %w", id, err)
		default:
			out = append(out, requestsOf(st)...)
		}
	}
	return byRequestTime(out), nil
}

func requestsOf(st repo.ThreadState) []Request {
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

func requestedAt(it repo.ItemRow) time.Time {
	if it.Approval != nil {
		return it.Approval.RequestedAt
	}
	return it.StartedAt
}
