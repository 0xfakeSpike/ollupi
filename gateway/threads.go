package gateway

import (
	"encoding/base64"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (c *conn) threadStart(p ThreadStartParams) (ThreadResult, func(), error) {
	t, err := c.s.agent.StartThread(p.WorkspaceID, p.Title, p.PermissionSettings)
	return ThreadResult{Thread: t}, nil, err
}

func threadPage(resume bool) func(*conn, ThreadPageParams) (ThreadPageResult, func(), error) {
	return func(c *conn, p ThreadPageParams) (ThreadPageResult, func(), error) {
		if p.Limit == 0 {
			p.Limit = defaultItems
		}
		if p.Limit < 1 || p.Limit > maxItems || p.BeforeSeq < 0 {
			return ThreadPageResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d and beforeSeq not negative", maxItems)
		}
		c.s.deliver.Lock()
		view, more, err := c.s.db.ThreadPage(p.ThreadID, p.BeforeSeq, p.Limit)
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
				for _, r := range c.s.agent.PendingRequests(p.ThreadID) {
					c.request(r.Thread.ID, r.Item)
				}
			}
		}
		return ThreadPageResult{ThreadView: view, HasMore: more}, after, nil
	}
}

func (c *conn) threadPermissions(p ThreadPermissionsParams) (ThreadResult, func(), error) {
	t, err := c.s.agent.UpdateThreadSettings(p.ThreadID, p.PermissionSettings)
	return ThreadResult{Thread: t}, nil, err
}

func (c *conn) threadRename(p ThreadRenameParams) (ThreadResult, func(), error) {
	t, err := c.s.agent.RenameThread(p.ThreadID, p.Title)
	return ThreadResult{Thread: t}, nil, err
}

func (c *conn) threadArchive(p ThreadArchiveParams) (ThreadResult, func(), error) {
	if p.Archived == nil {
		return ThreadResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "archived is required")
	}
	t, err := c.s.agent.ArchiveThread(p.ThreadID, *p.Archived)
	return ThreadResult{Thread: t}, nil, err
}

func (c *conn) threadDelete(p ThreadDeleteParams) (Empty, func(), error) {
	return Empty{}, nil, c.s.agent.DeleteThread(p.ThreadID)
}

func (c *conn) threadList(p repo.ThreadFilter) (ThreadListResult, func(), error) {
	if p.Limit == 0 {
		p.Limit = defaultLimit
	}
	if p.Limit < 1 || p.Limit > maxLimit {
		return ThreadListResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d", maxLimit)
	}
	threads, more, err := c.s.agent.ListThreads(p)
	if err != nil {
		return ThreadListResult{}, nil, err
	}
	return ThreadListResult{Data: nonNil(threads), HasMore: more}, nil, nil
}

func (c *conn) turnStart(p TurnStartParams) (TurnResult, func(), error) {
	in, err := agentInput(p.Input)
	if err != nil {
		return TurnResult{}, nil, err
	}
	in.Connection = p.Connection
	turn, err := c.s.agent.StartTurn(p.ThreadID, in)
	if err != nil {
		return TurnResult{}, nil, err
	}
	return TurnResult{Turn: turn}, nil, nil
}

func (c *conn) turnSteer(p TurnSteerParams) (TurnSteerResult, func(), error) {
	if p.ExpectedTurnID == "" {
		return TurnSteerResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "expectedTurnId is required")
	}
	in, err := agentInput(p.Input)
	if err != nil {
		return TurnSteerResult{}, nil, err
	}
	turn, err := c.s.agent.SteerTurn(p.ThreadID, p.ExpectedTurnID, in)
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

func (c *conn) attachmentRead(p AttachmentReadParams) (AttachmentReadResult, func(), error) {
	att, data, err := c.s.agent.ReadAttachment(p.ThreadID, p.AttachmentID)
	return AttachmentReadResult{Attachment: att, Data: base64.StdEncoding.EncodeToString(data)}, nil, err
}
