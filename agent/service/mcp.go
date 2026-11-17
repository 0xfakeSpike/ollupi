package service

import (
	"errors"
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools/mcp"
)

var ErrInvalidMCPServer = errors.New("invalid MCP server")

type MCPServer struct {
	db.MCPServerRow
	// Status is nil for a server that has not been started yet.
	Status *mcp.ServerStatus
}

func (sv *Service) MCPServers(user string) ([]MCPServer, error) {
	rows, err := sv.cfg.DB.MCPServers(user)
	if err != nil {
		return nil, fmt.Errorf("service: the MCP servers of the user: %w", err)
	}
	running, err := sv.mcp.For(user)
	if err != nil {
		return nil, err
	}
	statuses := map[string]mcp.ServerStatus{}
	for _, s := range running.Statuses() {
		statuses[s.Name] = s
	}
	out := make([]MCPServer, 0, len(rows))
	for _, r := range rows {
		s := MCPServer{MCPServerRow: r}
		if status, ok := statuses[r.Name]; ok {
			s.Status = &status
		}
		out = append(out, s)
	}
	return out, nil
}

// The user's servers start again with it, so a turn that starts after sees its tools.
func (sv *Service) SetMCPServer(row db.MCPServerRow) (db.MCPServerRow, error) {
	if err := server(row).Check(); err != nil {
		return db.MCPServerRow{}, fmt.Errorf("service: %w: %v", ErrInvalidMCPServer, err)
	}
	if sv.res.Tools().HasProvider(row.Name) {
		return db.MCPServerRow{}, fmt.Errorf("service: %w: %q names tools this agent already has", ErrInvalidMCPServer, row.Name)
	}
	if err := sv.cfg.DB.SetMCPServer(row); err != nil {
		return db.MCPServerRow{}, fmt.Errorf("service: MCP server %s: %w", row.Name, err)
	}
	sv.mcp.Reset(row.UserID)
	sv.ev.Publish(event.Event{Kind: event.MCPServerChanged, User: row.UserID, MCPServer: &row})
	return row, nil
}

func (sv *Service) DeleteMCPServer(user, name string) error {
	if err := sv.cfg.DB.DeleteMCPServer(user, name); err != nil {
		return fmt.Errorf("service: MCP server %s: %w", name, err)
	}
	sv.mcp.Reset(user)
	sv.ev.Publish(event.Event{Kind: event.MCPServerDeleted, User: user, MCPServer: &db.MCPServerRow{UserID: user, Name: name}})
	return nil
}

func (sv *Service) mcpServers(user string) ([]mcp.Server, error) {
	rows, err := sv.cfg.DB.MCPServers(user)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.Server, 0, len(rows))
	for _, r := range rows {
		out = append(out, server(r))
	}
	return out, nil
}

func server(r db.MCPServerRow) mcp.Server {
	return mcp.Server{Name: r.Name, Command: r.Command, Args: r.Args, Env: r.Env, URL: r.URL, Headers: r.Headers}
}
