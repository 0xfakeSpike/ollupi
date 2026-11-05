package agent

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

func (a *Agent) PendingRequests(user, threadID string) ([]Request, error) {
	if err := a.owned(user, threadID); err != nil {
		return nil, err
	}
	st, err := a.cfg.DB.ThreadState(threadID)
	if err != nil {
		return nil, fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	out := requestsOf(st)
	for _, call := range st.AgentCalls() {
		child, err := a.cfg.DB.ThreadState(call.Await.ThreadID)
		if err != nil {
			return nil, fmt.Errorf("agent: thread %s: %w", call.Await.ThreadID, err)
		}
		out = append(out, requestsOf(child)...)
	}
	return byRequestTime(out), nil
}

func (a *Agent) AllPendingRequests(user string) ([]Request, error) {
	ids, err := a.cfg.DB.ThreadsWaitingOnUser(user)
	if err != nil {
		return nil, fmt.Errorf("agent: threads waiting on the user: %w", err)
	}
	var out []Request
	for _, id := range ids {
		st, err := a.cfg.DB.ThreadState(id)
		switch {
		case errors.Is(err, repo.ErrNotFound):
		case err != nil:
			return nil, fmt.Errorf("agent: thread %s: %w", id, err)
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
