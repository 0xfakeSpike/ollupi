package gateway

import (
	"context"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

// The first is where a conversation that names none runs.
func (c *conn) environmentList(ctx context.Context, _ Empty) (EnvironmentListResult, func(), error) {
	rows, err := c.s.service.Environments(c.user)
	if err != nil {
		return EnvironmentListResult{}, nil, err
	}
	return EnvironmentListResult{Data: each(rows, environmentRow)}, nil, nil
}

func (c *conn) environmentAdd(ctx context.Context, p EnvironmentAddParams) (EnvironmentResult, func(), error) {
	return environmentResult(c.s.service.AddEnvironment(c.user, p.Name, p.Kind))
}

func (c *conn) environmentRename(ctx context.Context, p EnvironmentRenameParams) (EnvironmentResult, func(), error) {
	if strings.TrimSpace(p.EnvironmentID) == "" {
		return EnvironmentResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "environmentId is required")
	}
	return environmentResult(c.s.service.RenameEnvironment(c.user, p.EnvironmentID, p.Name))
}

func (c *conn) environmentDelete(ctx context.Context, p EnvironmentDeleteParams) (EnvironmentResult, func(), error) {
	if strings.TrimSpace(p.EnvironmentID) == "" {
		return EnvironmentResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "environmentId is required")
	}
	return environmentResult(c.s.service.DeleteEnvironment(c.user, p.EnvironmentID))
}

func environmentResult(env db.EnvironmentRow, err error) (EnvironmentResult, func(), error) {
	return EnvironmentResult{Environment: environmentRow(env)}, nil, err
}
