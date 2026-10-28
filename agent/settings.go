package agent

import "github.com/0xfakeSpike/ollupi/repo"

func (a *Agent) Settings() (repo.SettingsRow, error) {
	return a.cfg.DB.Settings()
}

func (a *Agent) SetRedaction(on bool) (repo.SettingsRow, error) {
	return a.cfg.DB.SetRedaction(on)
}
