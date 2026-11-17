package gateway

import (
	"context"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (c *conn) modelList(ctx context.Context, _ Empty) (ModelListResult, func(), error) {
	models, fallback := c.s.service.ListModels()
	return ModelListResult{Data: models, Default: fallback}, nil, nil
}

func (c *conn) toolList(ctx context.Context, _ Empty) (ToolListResult, func(), error) {
	list, err := c.s.service.ListTools(c.user)
	if err != nil {
		return ToolListResult{}, nil, err
	}
	return ToolListResult{Data: nonNil(list)}, nil, nil
}

func (c *conn) mcpServerList(ctx context.Context, _ Empty) (MCPServerListResult, func(), error) {
	servers, err := c.s.service.MCPServers(c.user)
	if err != nil {
		return MCPServerListResult{}, nil, err
	}
	out := make([]MCPServer, 0, len(servers))
	for _, s := range servers {
		out = append(out, MCPServer{MCPServerConfig: mcpServerConfig(s.MCPServerRow), Status: s.Status})
	}
	return MCPServerListResult{Data: out}, nil, nil
}

func (c *conn) mcpServerSet(ctx context.Context, p MCPServerConfig) (MCPServerConfig, func(), error) {
	row, err := c.s.service.SetMCPServer(db.MCPServerRow{
		UserID: c.user, Name: p.Name, Command: p.Command, Args: p.Args, Env: p.Env, URL: p.URL, Headers: p.Headers,
	})
	return mcpServerConfig(row), nil, err
}

func (c *conn) mcpServerDelete(ctx context.Context, p MCPServerDeleteParams) (Empty, func(), error) {
	return Empty{}, nil, c.s.service.DeleteMCPServer(c.user, p.Name)
}
