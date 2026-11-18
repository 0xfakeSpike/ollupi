package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/store"
)

func (c *conn) threadStart(ctx context.Context, p ThreadStartParams) (ThreadResult, error) {
	t, err := c.s.app.StartThread(ctx, c.user, p.WorkspaceID, p.EnvironmentID, p.Title)
	return ThreadResult{Thread: threadRow(t)}, err
}

func threadPage(resume bool) func(*conn, context.Context, ThreadPageParams) (ThreadPageResult, func(), error) {
	return func(c *conn, ctx context.Context, p ThreadPageParams) (ThreadPageResult, func(), error) {
		if err := limit(&p.Limit, defaultItems, maxItems); err != nil {
			return ThreadPageResult{}, nil, err
		}
		if p.BeforeSeq < 0 {
			return ThreadPageResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "beforeSeq must not be negative")
		}
		th, page, more, seq, err := c.s.app.Page(ctx, c.user, p.ThreadID, p.BeforeSeq, p.Limit)
		if err != nil {
			return ThreadPageResult{}, nil, err
		}
		items := each(page.Items, itemRow)
		size := 0
		for i := len(items) - 1; i >= 0; i-- {
			size += itemBytes(items[i])
			if size > pageBytes && i < len(items)-1 {
				items, more = items[i+1:], true
				break
			}
		}
		var active string
		for _, t := range page.Turns {
			if t.Ended == nil {
				active = t.ID
				break
			}
		}
		var after func()
		if resume {
			after = func() { c.requests(c.s.app.Requests(context.Background(), c.user, p.ThreadID)) }
		}
		view := ThreadView{Thread: threadRow(th), Turns: turnRows(th.ID, page.Turns, active), Items: items, Seq: seq}
		return ThreadPageResult{ThreadView: view, HasMore: more}, after, nil
	}
}

const pageBytes = 4 << 20

func itemBytes(it ItemRow) int {
	raw, err := json.Marshal(it)
	if err != nil {
		return pageBytes
	}
	return len(raw)
}

func limit(n *int, def, most int) error {
	if *n == 0 {
		*n = def
	}
	if *n < 1 || *n > most {
		return jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d", most)
	}
	return nil
}

func (c *conn) threadList(ctx context.Context, p ThreadListParams) (ThreadListResult, error) {
	if err := limit(&p.Limit, defaultLimit, maxLimit); err != nil {
		return ThreadListResult{}, err
	}
	f := store.ThreadFilter{Workspace: p.WorkspaceID, Archived: p.Archived, Limit: p.Limit}
	if a := p.After; a != nil {
		f.After = &store.ThreadCursor{Updated: a.UpdatedAt, Created: a.CreatedAt, ID: a.ID}
	}
	threads, more, err := c.s.app.Threads(ctx, c.user, f)
	return ThreadListResult{Data: each(threads, threadRow), HasMore: more}, err
}

func (c *conn) threadPermissions(ctx context.Context, p ThreadPermissionsParams) (ThreadResult, error) {
	t, err := c.s.app.UpdateThreadPermissions(ctx, c.user, p.ThreadID, p.Settings)
	return ThreadResult{Thread: threadRow(t)}, err
}

func (c *conn) threadRename(ctx context.Context, p ThreadRenameParams) (ThreadResult, error) {
	t, err := c.s.app.RenameThread(ctx, c.user, p.ThreadID, p.Title)
	return ThreadResult{Thread: threadRow(t)}, err
}

func (c *conn) threadMarkRead(ctx context.Context, p ThreadMarkReadParams) (ThreadResult, error) {
	t, err := c.s.app.MarkThreadRead(ctx, c.user, p.ThreadID)
	return ThreadResult{Thread: threadRow(t)}, err
}

func (c *conn) threadArchive(ctx context.Context, p ThreadArchiveParams) (ThreadResult, error) {
	if p.Archived == nil {
		return ThreadResult{}, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "archived is required")
	}
	t, err := c.s.app.ArchiveThread(ctx, c.user, p.ThreadID, *p.Archived)
	return ThreadResult{Thread: threadRow(t)}, err
}

func (c *conn) threadDelete(ctx context.Context, p ThreadDeleteParams) (Empty, error) {
	return Empty{}, c.s.app.DeleteThread(ctx, c.user, p.ThreadID)
}

func (c *conn) attachmentRead(ctx context.Context, p AttachmentReadParams) (AttachmentReadResult, error) {
	att, data, err := c.s.app.ReadAttachment(ctx, c.user, p.ThreadID, p.AttachmentID)
	return AttachmentReadResult{Attachment: att, Data: base64.StdEncoding.EncodeToString(data)}, err
}

func (c *conn) turnStart(ctx context.Context, p TurnStartParams) (TurnResult, error) {
	in, err := input(p.Input)
	if err != nil {
		return TurnResult{}, err
	}
	in.Connection = p.Connection
	turn, inProgress, err := c.s.app.Send(ctx, c.user, p.ThreadID, in)
	active := ""
	if inProgress {
		active = turn.ID
	}
	return TurnResult{Turn: turnRow(p.ThreadID, turn, active)}, err
}

func (c *conn) turnSteer(ctx context.Context, p TurnSteerParams) (TurnSteerResult, error) {
	if p.ExpectedTurnID == "" {
		return TurnSteerResult{}, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "expectedTurnId is required")
	}
	in, err := input(p.Input)
	if err != nil {
		return TurnSteerResult{}, err
	}
	turn, err := c.s.app.Steer(ctx, c.user, p.ThreadID, p.ExpectedTurnID, in)
	return TurnSteerResult{TurnID: turn.ID}, err
}

func (c *conn) turnInterrupt(ctx context.Context, p TurnInterruptParams) (Empty, error) {
	if strings.TrimSpace(p.TurnID) == "" {
		return Empty{}, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "turnId is required")
	}
	return Empty{}, c.s.app.Interrupt(ctx, c.user, p.ThreadID, p.TurnID)
}

func input(parts []UserInput) (app.Input, error) {
	var in app.Input
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			texts = append(texts, part.Text)
		case "file":
			in.Attachments = append(in.Attachments, app.AttachmentInput{Path: part.Path, Name: part.Name, MimeType: part.MimeType})
		default:
			return app.Input{}, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "input type %q is not supported", part.Type)
		}
	}
	in.Text = strings.Join(texts, "\n")
	return in, nil
}
