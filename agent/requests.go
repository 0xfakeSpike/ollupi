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
	var out []Request
	for _, it := range st.PendingRequests() {
		out = append(out, Request{Thread: st.Thread, Item: it})
	}
	for _, call := range st.AgentCalls() {
		if child, err := a.cfg.DB.ThreadState(call.Await.ThreadID); err == nil {
			for _, it := range child.PendingRequests() {
				out = append(out, Request{Thread: child.Thread, Item: it})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return requestedAt(out[i].Item).Before(requestedAt(out[j].Item)) })
	return out
}

func (a *Agent) AllPendingRequests() []Request {
	ids, err := a.cfg.DB.ActiveThreadIDs()
	if err != nil {
		return nil
	}
	var out []Request
	for _, id := range ids {
		st, err := a.cfg.DB.ThreadState(id)
		if err != nil {
			continue
		}
		for _, it := range st.PendingRequests() {
			out = append(out, Request{Thread: st.Thread, Item: it})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return requestedAt(out[i].Item).Before(requestedAt(out[j].Item)) })
	return out
}

func requestedAt(it repo.ItemRow) time.Time {
	if it.Approval != nil {
		return it.Approval.RequestedAt
	}
	return it.StartedAt
}
