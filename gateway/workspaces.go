package gateway

import (
	"context"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (c *conn) workspaceList(ctx context.Context, _ Empty) (WorkspaceListResult, func(), error) {
	rows, err := c.s.db.WithContext(ctx).Workspaces(c.user)
	if err != nil {
		return WorkspaceListResult{}, nil, err
	}
	return WorkspaceListResult{Data: each(rows, workspaceRow)}, nil, nil
}

func (c *conn) workspaceAdd(ctx context.Context, p WorkspaceAddParams) (WorkspaceResult, func(), error) {
	return workspaceResult(c.s.orchestrator.AddWorkspace(ctx, c.user, p.Path, p.Name))
}

func (c *conn) workspaceRename(ctx context.Context, p WorkspaceRenameParams) (WorkspaceResult, func(), error) {
	return workspaceResult(c.s.orchestrator.RenameWorkspace(c.user, p.WorkspaceID, p.Name))
}

// workspaceMove moves the workspace's folder to path, a folder that is there and empty.
func (c *conn) workspaceMove(ctx context.Context, p WorkspaceMoveParams) (WorkspaceResult, func(), error) {
	if strings.TrimSpace(p.WorkspaceID) == "" {
		return WorkspaceResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "workspaceId is required")
	}
	if strings.TrimSpace(p.Path) == "" {
		return WorkspaceResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "path is required")
	}
	return workspaceResult(c.s.orchestrator.MoveWorkspace(ctx, c.user, p.WorkspaceID, p.Path))
}

func (c *conn) workspaceArchive(ctx context.Context, p WorkspaceArchiveParams) (WorkspaceResult, func(), error) {
	if p.Archived == nil {
		return WorkspaceResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "archived is required")
	}
	return workspaceResult(c.s.orchestrator.ArchiveWorkspace(c.user, p.WorkspaceID, *p.Archived))
}

func (c *conn) workspacePermissions(ctx context.Context, p WorkspacePermissionsParams) (WorkspaceResult, func(), error) {
	if strings.TrimSpace(p.WorkspaceID) == "" {
		return WorkspaceResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "workspaceId is required")
	}
	return workspaceResult(c.s.orchestrator.UpdateWorkspaceSettings(c.user, p.WorkspaceID, p.PermissionSettings))
}

func workspaceResult(w repo.WorkspaceRow, err error) (WorkspaceResult, func(), error) {
	return WorkspaceResult{Workspace: workspaceRow(w)}, nil, err
}
