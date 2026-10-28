package gateway

import (
	"strings"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (c *conn) threadStart(p ThreadStartParams) (ThreadResult, func(), error) {
	t, err := c.s.agent.StartThread(p.WorkspaceID, p.PermissionSettings)
	if err != nil {
		return ThreadResult{}, nil, err
	}
	result := ThreadResult{Thread: t}
	return result, func() { c.rpc.Notify(notifyThreadStarted, result) }, nil
}

func threadPage(resume bool) func(*conn, ThreadPageParams) (ThreadPageResult, func(), error) {
	return func(c *conn, p ThreadPageParams) (ThreadPageResult, func(), error) {
		if p.Limit == 0 {
			p.Limit = defaultItems
		}
		if p.Limit < 1 || p.Limit > maxItems || p.BeforeSeq < 0 {
			return ThreadPageResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d and beforeSeq not negative", maxItems)
		}
		ag := c.s.agent
		c.s.deliver.Lock()
		view, more, err := ag.ReadThreadPage(p.ThreadID, p.BeforeSeq, p.Limit)
		if err != nil {
			c.s.deliver.Unlock()
			return ThreadPageResult{}, nil, err
		}
		size := 0
		for i := len(view.Items) - 1; i >= 0; i-- {
			size += itemBytes(view.Items[i])
			if size > pageBytes && i < len(view.Items)-1 {
				view.Items, more = view.Items[i+1:], true
				break
			}
		}
		view.Turns, view.Items = nonNil(view.Turns), nonNil(view.Items)
		after := func() {
			c.s.deliver.Unlock()
			if resume {
				for _, r := range ag.PendingRequests(p.ThreadID) {
					c.request(r.Thread.ID, r.Item)
				}
			}
		}
		return ThreadPageResult{ThreadView: view, HasMore: more}, after, nil
	}
}

func (c *conn) threadPermissions(p ThreadPermissionsParams) (ThreadResult, func(), error) {
	t, err := c.s.agent.UpdateThreadSettings(p.ThreadID, p.PermissionSettings)
	if err != nil {
		return ThreadResult{}, nil, err
	}
	return ThreadResult{Thread: t}, nil, nil
}

func (c *conn) threadList(p repo.ThreadFilter) (ThreadListResult, func(), error) {
	if p.Limit == 0 {
		p.Limit = defaultLimit
	}
	if p.Limit < 1 || p.Limit > maxLimit {
		return ThreadListResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d", maxLimit)
	}
	threads, err := c.s.agent.ListThreads(p)
	if err != nil {
		return ThreadListResult{}, nil, err
	}
	return ThreadListResult{Data: nonNil(threads)}, nil, nil
}

func (c *conn) turnStart(p TurnStartParams) (TurnResult, func(), error) {
	text, err := inputText(p.Input)
	if err != nil {
		return TurnResult{}, nil, err
	}
	turn, err := c.s.agent.StartTurn(p.ThreadID, agent.Input{Text: text, Connection: p.Connection})
	if err != nil {
		return TurnResult{}, nil, err
	}
	return TurnResult{Turn: turn}, nil, nil
}

func (c *conn) turnSteer(p TurnSteerParams) (TurnSteerResult, func(), error) {
	if p.ExpectedTurnID == "" {
		return TurnSteerResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "expectedTurnId is required")
	}
	text, err := inputText(p.Input)
	if err != nil {
		return TurnSteerResult{}, nil, err
	}
	turn, err := c.s.agent.SteerTurn(p.ThreadID, p.ExpectedTurnID, text)
	if err != nil {
		return TurnSteerResult{}, nil, err
	}
	return TurnSteerResult{TurnID: turn.ID}, nil, nil
}

func (c *conn) turnInterrupt(p TurnInterruptParams) (Empty, func(), error) {
	if strings.TrimSpace(p.TurnID) == "" {
		return Empty{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "turnId is required")
	}
	return Empty{}, nil, c.s.agent.InterruptTurn(p.ThreadID, p.TurnID)
}
