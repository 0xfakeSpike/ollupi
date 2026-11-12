package gateway

import (
	"context"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
)

func (c *conn) settingsRead(ctx context.Context, _ Empty) (SettingsRow, func(), error) {
	settings, err := c.s.db.WithContext(ctx).Settings(c.user)
	return settingsRow(settings), nil, err
}

func (c *conn) settingsUpdate(ctx context.Context, p SettingsUpdateParams) (SettingsRow, func(), error) {
	if p.Redaction == nil {
		return SettingsRow{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "redaction is required")
	}
	settings, err := c.s.db.WithContext(ctx).SetRedaction(c.user, *p.Redaction)
	if err != nil {
		return SettingsRow{}, nil, err
	}
	row := settingsRow(settings)
	return row, func() { c.s.each(c.user, func(o *conn) { o.rpc.Notify(notifySettingsChanged, row) }) }, nil
}
