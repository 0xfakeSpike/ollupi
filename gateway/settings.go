package gateway

import (
	"context"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
)

func (c *conn) settingsRead(ctx context.Context, _ Empty) (SettingsRow, func(), error) {
	settings, err := c.s.service.Settings(ctx, c.user)
	return settingsRow(settings), nil, err
}

func (c *conn) settingsUpdate(ctx context.Context, p SettingsUpdateParams) (SettingsRow, func(), error) {
	if p.Redaction == nil {
		return SettingsRow{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "redaction is required")
	}
	settings, err := c.s.service.SetRedaction(ctx, c.user, *p.Redaction)
	return settingsRow(settings), nil, err
}
