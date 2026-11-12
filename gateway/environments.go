package gateway

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

// environmentList lists where the user's conversations can run; the first is where one that names none runs.
func (c *conn) environmentList(ctx context.Context, _ Empty) (EnvironmentListResult, func(), error) {
	rows, err := c.s.agent.Environments(c.user)
	if err != nil {
		return EnvironmentListResult{}, nil, err
	}
	return EnvironmentListResult{Data: each(rows, environmentRow)}, nil, nil
}

func (c *conn) environmentAdd(ctx context.Context, p EnvironmentAddParams) (EnvironmentResult, func(), error) {
	if strings.TrimSpace(p.Name) == "" {
		return EnvironmentResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "name is required")
	}
	return environmentResult(c.s.agent.AddEnvironment(c.user, p.Name, p.Kind))
}

func (c *conn) environmentRename(ctx context.Context, p EnvironmentRenameParams) (EnvironmentResult, func(), error) {
	if strings.TrimSpace(p.EnvironmentID) == "" {
		return EnvironmentResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "environmentId is required")
	}
	if strings.TrimSpace(p.Name) == "" {
		return EnvironmentResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "name is required")
	}
	return environmentResult(c.s.agent.RenameEnvironment(c.user, p.EnvironmentID, p.Name))
}

func (c *conn) environmentDelete(ctx context.Context, p EnvironmentDeleteParams) (EnvironmentResult, func(), error) {
	if strings.TrimSpace(p.EnvironmentID) == "" {
		return EnvironmentResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "environmentId is required")
	}
	return environmentResult(c.s.agent.DeleteEnvironment(c.user, p.EnvironmentID))
}

func environmentResult(env repo.EnvironmentRow, err error) (EnvironmentResult, func(), error) {
	return EnvironmentResult{Environment: environmentRow(env)}, nil, err
}

// environmentChanged tells the user's connections that an environment changed, or, when it can no longer be read,
// that it is gone. It reads the environment only when a connection is there to tell.
func (s *Server) environmentChanged(user, id string) {
	var n *EnvironmentChangedNotification
	s.each(user, func(c *conn) {
		if n == nil {
			read := EnvironmentChangedNotification{EnvironmentID: id}
			env, err := s.db.EnvironmentOf(user, id)
			switch {
			case err == nil:
				row := environmentRow(env)
				read.Environment = &row
			case !errors.Is(err, repo.ErrNotFound):
				slog.Error("changed environment not readable", "environment", id, "err", err)
				return
			}
			n = &read
		}
		c.rpc.Notify(notifyEnvironmentChanged, *n)
	})
}
