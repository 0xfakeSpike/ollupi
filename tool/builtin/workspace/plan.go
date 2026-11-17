package workspace

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

type Prepared struct {
	Cwd             string            `json:"cwd"`
	SettingsDigest  string            `json:"settingsDigest"`
	ArgumentsDigest string            `json:"argumentsDigest"`
	Program         string            `json:"program,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	TimeoutSec      int               `json:"timeoutSec,omitempty"`
	Sandboxed       bool              `json:"sandboxed,omitempty"`
	Escalated       bool              `json:"escalated,omitempty"`
	Justification   string            `json:"justification,omitempty"`
	Files           []FileChange      `json:"files,omitempty"`
}

type FileChange struct {
	Path    string `json:"path"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after"`
	Diff    string `json:"diff"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

type local struct {
	prepare func(ctx context.Context, caller tool.Caller, args map[string]any) (*Prepared, error)
	run     func(ctx context.Context, caller tool.Caller, args map[string]any, p *Prepared) (kit.Result, error)
}

func (l local) plan(ctx context.Context, c tool.Call) (tool.Plan, error) {
	p, err := l.prepare(ctx, c.Caller, c.Arguments)
	if err != nil {
		return tool.Plan{}, err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return tool.Plan{}, fmt.Errorf("workspace: encode the plan: %w", err)
	}
	return tool.Plan{Data: data, Sandboxed: p.Sandboxed, Escalated: p.Escalated}, nil
}

func (l local) invoke(ctx context.Context, c tool.Call) (kit.Result, error) {
	var p *Prepared
	if err := json.Unmarshal(c.Prepared, &p); err != nil {
		return kit.Result{}, fmt.Errorf("workspace: the call's plan: %w", err)
	}
	return l.run(ctx, c.Caller, c.Arguments, p)
}

func (r *Provider) locals() map[string]local {
	file := local{r.prepareFile, r.runFile}
	return map[string]local{
		ToolRunCommand: {r.prepareCommand, r.runCommand},
		ToolClaude:     {r.prepareClaude, r.runClaude},
		ToolEditFile:   file,
		ToolWriteFile:  file,
	}
}
