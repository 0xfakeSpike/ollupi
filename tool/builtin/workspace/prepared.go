package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func (r *Provider) settingsDigest(d dirs) string {
	return digest(struct {
		Sandbox    any
		TimeoutSec int
		Blacklist  []string
		Roots      []string
	}{d.box.Fingerprint(), r.timeoutSec, r.blacklist, d.roots})
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digest(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("workspace: digest non-JSON value: %v", err))
	}
	return sha(string(body))
}

func (r *Provider) validatePrepared(ctx context.Context, d dirs, p *Prepared, args map[string]any) error {
	if p.SettingsDigest != r.settingsDigest(d) {
		return fmt.Errorf("workspace: the command settings or the project directory changed after this call was prepared, so it was not run; call run_command again")
	}
	if p.ArgumentsDigest != digest(args) {
		return fmt.Errorf("workspace: prepared command arguments changed")
	}
	cwd, err := d.resolve(ctx, p.Cwd)
	if err != nil {
		return err
	}
	if cwd != p.Cwd {
		return fmt.Errorf("workspace: prepared command directory changed")
	}
	return nil
}
