package workspace

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
)

type Config struct {
	WorkspaceDirs        []string `json:"workspace_dirs"`
	CommandTimeoutSec    int      `json:"command_timeout_sec"`
	MaxCommandTimeoutSec int      `json:"max_command_timeout_sec,omitempty"`
	CommandBlacklist     []string `json:"command_blacklist"`
	SandboxMode          string   `json:"sandbox_mode,omitempty"`
	Network              bool     `json:"network,omitempty"`
	AllowedDomains       []string `json:"allowed_domains,omitempty"`
	DeniedDomains        []string `json:"denied_domains,omitempty"`
	DenyRead             []string `json:"deny_read,omitempty"`
	ReadOnly             []string `json:"read_only,omitempty"`
	ExtraWritable        []string `json:"extra_writable,omitempty"`
	UnixSockets          []string `json:"unix_sockets,omitempty"`
	EnvPassthrough       []string `json:"env_passthrough,omitempty"`
	MaxOutputBytes       int      `json:"max_output_bytes,omitempty"`
}

func (c *Config) normalize(base string) error {
	if len(c.WorkspaceDirs) == 0 {
		if base == "" {
			return fmt.Errorf("workspace_dirs: at least one command directory is required")
		}
		// Without workspace dirs commands run in a scratch folder, never in the agent home, which holds the database.
		scratch := filepath.Join(base, scratchDir)
		if err := os.MkdirAll(scratch, 0o755); err != nil {
			return fmt.Errorf("workspace_dirs: %w", err)
		}
		c.WorkspaceDirs = []string{scratch}
	} else {
		resolved := make([]string, 0, len(c.WorkspaceDirs))
		for _, entry := range c.WorkspaceDirs {
			if entry == "" {
				return fmt.Errorf("workspace_dirs contains empty path")
			}
			if !filepath.IsAbs(entry) && base != "" {
				entry = filepath.Join(base, entry)
			}
			resolved = append(resolved, filepath.Clean(entry))
		}
		c.WorkspaceDirs = resolved
	}
	if c.CommandTimeoutSec <= 0 {
		c.CommandTimeoutSec = 30
	}
	if c.MaxCommandTimeoutSec < 0 {
		return fmt.Errorf("max_command_timeout_sec must be >= 0")
	}
	if c.MaxCommandTimeoutSec == 0 {
		c.MaxCommandTimeoutSec = max(600, c.CommandTimeoutSec)
	}
	if c.MaxCommandTimeoutSec < c.CommandTimeoutSec {
		return fmt.Errorf("max_command_timeout_sec must be >= command_timeout_sec")
	}
	// The sandbox checks the rest: mode, domains, output limit and environment names.
	return nil
}

type AgentFiles struct {
	Home      string
	SkillsDir string
}

var agentProtectedNames = []string{
	"AGENTS.md", ".agents", ".codex", ".claude", "CLAUDE.md", ".cursor", ".mcp.json",
}

const (
	dataDir    = "data"
	scratchDir = "scratch"
)

func sandboxConfig(o Options, c Config) sandbox.Config {
	denyRead := append([]string(nil), o.DenyRead...)
	var readOnly []string
	if home := o.Agent.Home; home != "" {
		denyRead = append(denyRead, filepath.Join(home, dataDir))
	}
	if o.Agent.SkillsDir != "" {
		readOnly = append(readOnly, o.Agent.SkillsDir)
	}
	return sandbox.Config{
		Mode:           c.SandboxMode,
		Network:        c.Network,
		Domains:        sandbox.DomainRules{Allowed: c.AllowedDomains, Denied: c.DeniedDomains},
		DenyRead:       append(denyRead, c.DenyRead...),
		ReadOnly:       append(readOnly, c.ReadOnly...),
		ProtectedNames: agentProtectedNames,
		EnvMarker:      o.EnvMarker,
		ExtraWritable:  c.ExtraWritable,
		UnixSockets:    c.UnixSockets,
		EnvPassthrough: c.EnvPassthrough,
		MaxOutputBytes: c.MaxOutputBytes,
	}
}
