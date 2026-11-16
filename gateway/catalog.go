package gateway

import (
	"context"

	"github.com/0xfakeSpike/ollupi/tools/mcp"
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
	var servers []mcp.ServerStatus
	if c.s.mcp != nil {
		mine, err := c.s.mcp.For(c.user)
		if err != nil {
			return MCPServerListResult{}, nil, err
		}
		servers = mine.Statuses()
	}
	return MCPServerListResult{Data: nonNil(servers)}, nil, nil
}
