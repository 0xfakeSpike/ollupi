package agent

import (
	"sort"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
)

type Request struct {
	Thread repo.ThreadRow
	Item   repo.ItemRow
}

func (a *Agent) PendingRequests(threadID string) []Request {
	st, err := a.cfg.DB.ThreadState(threadID)
	if err != nil {
		return nil
	}
	out := requestsOf(st)
	for _, call := range st.AgentCalls() {
		if child, err := a.cfg.DB.ThreadState(call.Await.ThreadID); err == nil {
			out = append(out, requestsOf(child)...)
		}
	}
	return byRequestTime(out)
}

func (a *Agent) AllPendingRequests() []Request {
	ids, err := a.cfg.DB.OpenThreads(nil)
	if err != nil {
		return nil
	}
	var out []Request
	for _, id := range ids {
		if st, err := a.cfg.DB.ThreadState(id); err == nil {
			out = append(out, requestsOf(st)...)
		}
	}
	return byRequestTime(out)
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
