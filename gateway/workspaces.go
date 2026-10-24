package gateway

import (
	"encoding/json"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (c *conn) workspaceRequest(method string, params json.RawMessage) (any, func(), *jsonrpc.Error) {
	manager := c.s.agent
	if method == methodWorkspaceList {
		if err := jsonrpc.DecodeParams(params, &struct{}{}); err != nil {
			return nil, nil, err
		}
		rows, err := manager.ListWorkspaces()
		if err != nil {
			return nil, nil, serverError(err)
		}
		return struct {
			Data []repo.WorkspaceRow `json:"data"`
		}{rows}, nil, nil
	}
	var result repo.WorkspaceRow
	var err error
	switch method {
	case methodWorkspaceAdd:
		var p struct {
			Path string `json:"path"`
			Name string `json:"name,omitempty"`
		}
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		result, err = manager.AddWorkspace(p.Path, p.Name)
	case methodWorkspaceRename:
		var p struct {
			WorkspaceID string `json:"workspaceId"`
			Name        string `json:"name"`
		}
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		result, err = manager.RenameWorkspace(p.WorkspaceID, p.Name)
	case methodWorkspaceArchive:
		var p struct {
			WorkspaceID string `json:"workspaceId"`
			Archived    *bool  `json:"archived"`
		}
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		if p.Archived == nil {
			return nil, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "archived is required")
		}
		result, err = manager.ArchiveWorkspace(p.WorkspaceID, *p.Archived)
	case methodWorkspacePermissions:
		var p struct {
			WorkspaceID string `json:"workspaceId"`
			repo.PermissionSettings
		}
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(p.WorkspaceID) == "" {
			return nil, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "workspaceId is required")
		}
		result, err = manager.UpdateWorkspaceSettings(p.WorkspaceID, p.PermissionSettings)
	}
	if err != nil {
		return nil, nil, serverError(err)
	}
	payload := struct {
		Workspace repo.WorkspaceRow `json:"workspace"`
	}{result}
	return payload, func() { c.s.notify("workspace/changed", payload) }, nil
}
