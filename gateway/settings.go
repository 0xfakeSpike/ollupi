package gateway

import (
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (c *conn) settingsRead(Empty) (repo.SettingsRow, func(), error) {
	settings, err := c.s.agent.Settings()
	return settings, nil, err
}

func (c *conn) settingsUpdate(p SettingsUpdateParams) (repo.SettingsRow, func(), error) {
	if p.Redaction == nil {
		return repo.SettingsRow{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "redaction is required")
	}
	settings, err := c.s.agent.SetRedaction(*p.Redaction)
	if err != nil {
		return repo.SettingsRow{}, nil, err
	}
	return settings, func() { c.s.notify(notifySettingsChanged, settings) }, nil
}
