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
	if p.Redaction == nil && p.Memory == nil {
		return SettingsRow{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "redaction or memory is required")
	}
	settings, err := c.s.service.UpdateSettings(ctx, c.user, p.Redaction, p.Memory)
	return settingsRow(settings), nil, err
}

func (c *conn) settingsPermissions(ctx context.Context, p SettingsPermissionsParams) (SettingsRow, func(), error) {
	settings, err := c.s.service.UpdateUserPermissions(c.user, p.PermissionSettings)
	return settingsRow(settings), nil, err
}
