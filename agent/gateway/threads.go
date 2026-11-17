package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (c *conn) threadStart(ctx context.Context, p ThreadStartParams) (ThreadResult, func(), error) {
	t, err := c.s.service.StartThread(c.user, p.WorkspaceID, p.EnvironmentID, p.Title)
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
		c.rpc.HoldUntilReplied()
		view, more, err := c.s.service.ThreadPage(ctx, c.user, p.ThreadID, p.BeforeSeq, p.Limit)
		if err != nil {
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
		var after func()
		if resume {
			after = func() { c.requests(c.s.service.PendingRequests(c.user, p.ThreadID)) }
		}
		return ThreadPageResult{ThreadView: threadView(view), HasMore: more}, after, nil
	}
}

func (c *conn) threadPermissions(ctx context.Context, p ThreadPermissionsParams) (ThreadResult, func(), error) {
	t, err := c.s.service.UpdateThreadSettings(c.user, p.ThreadID, p.PermissionSettings)
	return ThreadResult{Thread: threadRow(t)}, nil, err
}

func (c *conn) threadRename(ctx context.Context, p ThreadRenameParams) (ThreadResult, func(), error) {
	t, err := c.s.service.RenameThread(c.user, p.ThreadID, p.Title)
	return ThreadResult{Thread: threadRow(t)}, nil, err
}

func (c *conn) threadMarkRead(ctx context.Context, p ThreadMarkReadParams) (ThreadResult, func(), error) {
	t, err := c.s.service.MarkThreadRead(c.user, p.ThreadID)
	return ThreadResult{Thread: threadRow(t)}, nil, err
}

func (c *conn) threadArchive(ctx context.Context, p ThreadArchiveParams) (ThreadResult, func(), error) {
	if p.Archived == nil {
		return ThreadResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "archived is required")
	}
	t, err := c.s.service.ArchiveThread(c.user, p.ThreadID, *p.Archived)
	return ThreadResult{Thread: threadRow(t)}, nil, err
}

func (c *conn) threadDelete(ctx context.Context, p ThreadDeleteParams) (Empty, func(), error) {
	return Empty{}, nil, c.s.service.DeleteThread(c.user, p.ThreadID)
}

func (c *conn) threadList(ctx context.Context, p db.ThreadFilter) (ThreadListResult, func(), error) {
	if p.Limit == 0 {
		p.Limit = defaultLimit
	}
	if p.Limit < 1 || p.Limit > maxLimit {
		return ThreadListResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d", maxLimit)
	}
	threads, more, err := c.s.service.ListThreads(c.user, p)
	if err != nil {
		return ThreadListResult{}, nil, err
	}
	return ThreadListResult{Data: each(threads, threadSummary), HasMore: more}, nil, nil
}

func (c *conn) attachmentRead(ctx context.Context, p AttachmentReadParams) (AttachmentReadResult, func(), error) {
	att, data, err := c.s.service.ReadAttachment(ctx, c.user, p.ThreadID, p.AttachmentID)
	return AttachmentReadResult{Attachment: att, Data: base64.StdEncoding.EncodeToString(data)}, nil, err
}

const pageBytes = 4 << 20

func itemBytes(it db.ItemRow) int {
	raw, err := json.Marshal(it)
	if err != nil {
		return pageBytes
	}
	return len(raw)
}
