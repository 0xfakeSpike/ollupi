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

func (a *orchestrator) pendingRequests(threadID string) []Request {
	view, err := a.readThread(threadID)
	if err != nil {
		return nil
	}
	var out []Request
	for _, it := range view.PendingRequests() {
		out = append(out, Request{Thread: view.Thread, Item: it})
	}
	for _, call := range view.AgentCalls() {
		if child, err := a.readThread(call.Await.ThreadID); err == nil {
			for _, it := range child.PendingRequests() {
				out = append(out, Request{Thread: child.Thread, Item: it})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return requestedAt(out[i].Item).Before(requestedAt(out[j].Item)) })
	return out
}

func (a *orchestrator) allPendingRequests() []Request {
	ids, err := a.DB.ActiveThreadIDs()
	if err != nil {
		return nil
	}
	var out []Request
	for _, id := range ids {
		v, err := a.readThread(id)
		if err != nil {
			continue
		}
		for _, it := range v.PendingRequests() {
			out = append(out, Request{Thread: v.Thread, Item: it})
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
