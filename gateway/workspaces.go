package gateway

import (
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (c *conn) workspaceList(Empty) (WorkspaceListResult, func(), error) {
	rows, err := c.s.db.Workspaces()
	if err != nil {
		return WorkspaceListResult{}, nil, err
	}
	return WorkspaceListResult{Data: nonNil(rows)}, nil, nil
}

func (c *conn) workspaceAdd(p WorkspaceAddParams) (WorkspaceResult, func(), error) {
	return workspaceResult(c.s.agent.AddWorkspace(p.Path, p.Name))
}

func (c *conn) workspaceRename(p WorkspaceRenameParams) (WorkspaceResult, func(), error) {
	return workspaceResult(c.s.agent.RenameWorkspace(p.WorkspaceID, p.Name))
}

func (c *conn) workspaceArchive(p WorkspaceArchiveParams) (WorkspaceResult, func(), error) {
	if p.Archived == nil {
		return WorkspaceResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "archived is required")
	}
	return workspaceResult(c.s.agent.ArchiveWorkspace(p.WorkspaceID, *p.Archived))
}

func (c *conn) workspacePermissions(p WorkspacePermissionsParams) (WorkspaceResult, func(), error) {
	if strings.TrimSpace(p.WorkspaceID) == "" {
		return WorkspaceResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "workspaceId is required")
	}
	return workspaceResult(c.s.agent.UpdateWorkspaceSettings(p.WorkspaceID, p.PermissionSettings))
}

func workspaceResult(w repo.WorkspaceRow, err error) (WorkspaceResult, func(), error) {
	return WorkspaceResult{Workspace: w}, nil, err
}
