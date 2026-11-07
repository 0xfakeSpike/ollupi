package gateway

import (
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (c *conn) workspaceList(Empty) (WorkspaceListResult, func(), error) {
	rows, err := c.s.db.Workspaces(c.user)
	if err != nil {
		return WorkspaceListResult{}, nil, err
	}
	return WorkspaceListResult{Data: nonNil(rows)}, nil, nil
}

func (c *conn) workspaceAdd(p WorkspaceAddParams) (WorkspaceResult, func(), error) {
	return workspaceResult(c.s.agent.AddWorkspace(c.user, p.Path, p.Name))
}

func (c *conn) workspaceRename(p WorkspaceRenameParams) (WorkspaceResult, func(), error) {
	return workspaceResult(c.s.agent.RenameWorkspace(c.user, p.WorkspaceID, p.Name))
}

// workspaceMove moves the workspace's folder to path, a folder that is there and empty.
func (c *conn) workspaceMove(p WorkspaceMoveParams) (WorkspaceResult, func(), error) {
	if strings.TrimSpace(p.WorkspaceID) == "" {
		return WorkspaceResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "workspaceId is required")
	}
	if strings.TrimSpace(p.Path) == "" {
		return WorkspaceResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "path is required")
	}
	return workspaceResult(c.s.agent.MoveWorkspace(c.user, p.WorkspaceID, p.Path))
}

func (c *conn) workspaceArchive(p WorkspaceArchiveParams) (WorkspaceResult, func(), error) {
	if p.Archived == nil {
		return WorkspaceResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "archived is required")
	}
	return workspaceResult(c.s.agent.ArchiveWorkspace(c.user, p.WorkspaceID, *p.Archived))
}

func (c *conn) workspacePermissions(p WorkspacePermissionsParams) (WorkspaceResult, func(), error) {
	if strings.TrimSpace(p.WorkspaceID) == "" {
		return WorkspaceResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "workspaceId is required")
	}
	return workspaceResult(c.s.agent.UpdateWorkspaceSettings(c.user, p.WorkspaceID, p.PermissionSettings))
}

func workspaceResult(w repo.WorkspaceRow, err error) (WorkspaceResult, func(), error) {
	return WorkspaceResult{Workspace: w}, nil, err
}
