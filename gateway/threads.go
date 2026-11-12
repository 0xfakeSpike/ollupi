package gateway

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (c *conn) threadStart(ctx context.Context, p ThreadStartParams) (ThreadResult, func(), error) {
	t, err := c.s.agent.StartThread(c.user, p.WorkspaceID, p.EnvironmentID, p.Title, p.PermissionSettings)
	return ThreadResult{Thread: threadRow(t)}, nil, err
}

func threadPage(resume bool) func(*conn, context.Context, ThreadPageParams) (ThreadPageResult, func(), error) {
	return func(c *conn, ctx context.Context, p ThreadPageParams) (ThreadPageResult, func(), error) {
		if p.Limit == 0 {
			p.Limit = defaultItems
		}
		if p.Limit < 1 || p.Limit > maxItems || p.BeforeSeq < 0 {
			return ThreadPageResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d and beforeSeq not negative", maxItems)
		}
		db := c.s.db.WithContext(ctx)
		if _, err := db.ThreadOf(c.user, p.ThreadID); err != nil {
			return ThreadPageResult{}, nil, err
		}
		c.deliver.Lock()
		view, more, err := db.ThreadPage(c.user, p.ThreadID, p.BeforeSeq, p.Limit)
		if err != nil {
			c.deliver.Unlock()
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
		after := func() {
			c.deliver.Unlock()
			if resume {
				c.requests(c.s.agent.PendingRequests(c.user, p.ThreadID))
			}
		}
		return ThreadPageResult{ThreadView: threadView(view), HasMore: more}, after, nil
	}
}

func (c *conn) threadPermissions(ctx context.Context, p ThreadPermissionsParams) (ThreadResult, func(), error) {
	t, err := c.s.agent.UpdateThreadSettings(c.user, p.ThreadID, p.PermissionSettings)
	return ThreadResult{Thread: threadRow(t)}, nil, err
}

func (c *conn) threadRename(ctx context.Context, p ThreadRenameParams) (ThreadResult, func(), error) {
	t, err := c.s.agent.RenameThread(c.user, p.ThreadID, p.Title)
	return ThreadResult{Thread: threadRow(t)}, nil, err
}

func (c *conn) threadMarkRead(ctx context.Context, p ThreadMarkReadParams) (ThreadResult, func(), error) {
	t, err := c.s.agent.MarkThreadRead(c.user, p.ThreadID)
	return ThreadResult{Thread: threadRow(t)}, nil, err
}

func (c *conn) threadArchive(ctx context.Context, p ThreadArchiveParams) (ThreadResult, func(), error) {
	if p.Archived == nil {
		return ThreadResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "archived is required")
	}
	t, err := c.s.agent.ArchiveThread(c.user, p.ThreadID, *p.Archived)
	return ThreadResult{Thread: threadRow(t)}, nil, err
}

func (c *conn) threadDelete(ctx context.Context, p ThreadDeleteParams) (Empty, func(), error) {
	return Empty{}, nil, c.s.agent.DeleteThread(c.user, p.ThreadID)
}

func (c *conn) threadList(ctx context.Context, p repo.ThreadFilter) (ThreadListResult, func(), error) {
	if p.Limit == 0 {
		p.Limit = defaultLimit
	}
	if p.Limit < 1 || p.Limit > maxLimit {
		return ThreadListResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d", maxLimit)
	}
	threads, more, err := c.s.agent.ListThreads(c.user, p)
	if err != nil {
		return ThreadListResult{}, nil, err
	}
	return ThreadListResult{Data: each(threads, threadSummary), HasMore: more}, nil, nil
}

func (c *conn) turnStart(ctx context.Context, p TurnStartParams) (TurnResult, func(), error) {
	in, err := agentInput(p.Input)
	if err != nil {
		return TurnResult{}, nil, err
	}
	in.Connection = p.Connection
	turn, err := c.s.agent.StartTurn(ctx, c.user, p.ThreadID, in)
	if err != nil {
		return TurnResult{}, nil, err
	}
	return TurnResult{Turn: turnRow(turn)}, nil, nil
}

func (c *conn) turnSteer(ctx context.Context, p TurnSteerParams) (TurnSteerResult, func(), error) {
	if p.ExpectedTurnID == "" {
		return TurnSteerResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "expectedTurnId is required")
	}
	in, err := agentInput(p.Input)
	if err != nil {
		return TurnSteerResult{}, nil, err
	}
	turn, err := c.s.agent.SteerTurn(ctx, c.user, p.ThreadID, p.ExpectedTurnID, in)
	if err != nil {
		return TurnSteerResult{}, nil, err
	}
	return TurnSteerResult{TurnID: turn.ID}, nil, nil
}

func (c *conn) turnInterrupt(ctx context.Context, p TurnInterruptParams) (Empty, func(), error) {
	if strings.TrimSpace(p.TurnID) == "" {
		return Empty{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "turnId is required")
	}
	return Empty{}, nil, c.s.agent.InterruptTurn(c.user, p.ThreadID, p.TurnID)
}

func (c *conn) attachmentRead(ctx context.Context, p AttachmentReadParams) (AttachmentReadResult, func(), error) {
	att, data, err := c.s.agent.ReadAttachment(ctx, c.user, p.ThreadID, p.AttachmentID)
	return AttachmentReadResult{Attachment: att, Data: base64.StdEncoding.EncodeToString(data)}, nil, err
}
