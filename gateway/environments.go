package gateway

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

// environmentList lists where the user's conversations can run; the first is where one that names none runs.
func (c *conn) environmentList(Empty) (EnvironmentListResult, func(), error) {
	rows, err := c.s.agent.Environments(c.user)
	if err != nil {
		return EnvironmentListResult{}, nil, err
	}
	return EnvironmentListResult{Data: nonNil(rows)}, nil, nil
}

func (c *conn) environmentAdd(p EnvironmentAddParams) (EnvironmentResult, func(), error) {
	if strings.TrimSpace(p.Name) == "" {
		return EnvironmentResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "name is required")
	}
	return environmentResult(c.s.agent.AddEnvironment(c.user, p.Name, p.Kind))
}

func (c *conn) environmentRename(p EnvironmentRenameParams) (EnvironmentResult, func(), error) {
	if strings.TrimSpace(p.EnvironmentID) == "" {
		return EnvironmentResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "environmentId is required")
	}
	if strings.TrimSpace(p.Name) == "" {
		return EnvironmentResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "name is required")
	}
	return environmentResult(c.s.agent.RenameEnvironment(c.user, p.EnvironmentID, p.Name))
}

func (c *conn) environmentDelete(p EnvironmentDeleteParams) (EnvironmentResult, func(), error) {
	if strings.TrimSpace(p.EnvironmentID) == "" {
		return EnvironmentResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "environmentId is required")
	}
	return environmentResult(c.s.agent.DeleteEnvironment(c.user, p.EnvironmentID))
}

func environmentResult(env repo.EnvironmentRow, err error) (EnvironmentResult, func(), error) {
	return EnvironmentResult{Environment: env}, nil, err
}

// environmentChanged tells the client an environment changed, or, when it can no longer be read, that it is gone.
func (c *conn) environmentChanged(id string) {
	n := EnvironmentChangedNotification{EnvironmentID: id}
	env, err := c.s.db.EnvironmentOf(c.user, id)
	switch {
	case err == nil:
		n.Environment = &env
	case !errors.Is(err, repo.ErrNotFound):
		slog.Error("changed environment not readable", "environment", id, "err", err)
		return
	}
	c.rpc.Notify(notifyEnvironmentChanged, n)
}
