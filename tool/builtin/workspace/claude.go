package workspace

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
)

const ToolClaude = "claude"

var claudeFlags = []string{
	"--output-format", "json",
	"--permission-mode", "acceptEdits",
	"--allowedTools", "Bash(go test *)",
	"--permission-prompts", "none",
	"--max-budget-usd", "2",
}

const claudeJustification = "Claude Code needs the network and its own settings in your home folder"

func (r *Provider) claudeTool() kit.Spec {
	return kit.Spec{
		Name: ToolClaude,
		Description: "Hand a self-contained task to Claude Code, which works in the project on its own and returns its final answer. " +
			"It may edit files and run go test, nothing else, and stops after spending $2. " +
			"Write the prompt as a full brief: it does not see this conversation. work_dir defaults to the project root; " +
			"timeout_sec defaults to " + fmt.Sprint(r.maxTimeoutSec) + ", the most allowed",
		InputSchema: r.claudeSchema(),
		Annotations: tool.Annotations{Title: "Claude Code", DestructiveHint: new(true), OpenWorldHint: new(true)},
		Content:     claudePermissionContent,
		Prepare:     r.locals()[ToolClaude].plan,
	}
}

type claudeTask struct {
	Prompt     string `json:"prompt" jsonschema:"The task for Claude Code, with the files to look at and what done looks like"`
	WorkDir    string `json:"work_dir,omitempty" jsonschema:"Optional folder Claude Code works in, relative to the project root; must stay within this turn's allowed directories"`
	TimeoutSec int    `json:"timeout_sec,omitempty" jsonschema:"Optional timeout in seconds"`
}

func (claudeTask) Constrain(s *jsonschema.Schema) {
	s.Properties["prompt"].MinLength = jsonschema.Ptr(1)
	constrainRun(s)
}

func (r *Provider) claudeSchema() *jsonschema.Schema {
	s := kit.InputSchema[claudeTask]()
	s.Properties["timeout_sec"].Maximum = jsonschema.Ptr(float64(r.maxTimeoutSec))
	return s
}

func claudeArgs(prompt string) []string {
	return append([]string{"-p", prompt}, claudeFlags...)
}

func claudeTaskOf(args map[string]any) (claudeTask, error) {
	t, err := kit.DecodeArguments[claudeTask](args)
	if err != nil {
		return t, err
	}
	if t.Prompt = strings.TrimSpace(t.Prompt); t.Prompt == "" {
		return t, fmt.Errorf("workspace: argument prompt must be a non-empty string")
	}
	return t, nil
}

func claudePermissionContent(_ context.Context, c tool.Call) (policy.Content, error) {
	t, err := claudeTaskOf(c.Arguments)
	if err != nil {
		return policy.Content{}, err
	}
	words := append([]string{ToolClaude}, claudeArgs(t.Prompt)...)
	return policy.Content{Text: strings.Join(words, " "), Match: line(words)}, nil
}

func (r *Provider) prepareClaude(ctx context.Context, call tool.Call) (*Prepared, error) {
	d, err := r.dirs(ctx, call.Caller)
	if err != nil {
		return nil, err
	}
	t, err := claudeTaskOf(call.Arguments)
	if err != nil {
		return nil, err
	}
	cwd, err := d.resolve(ctx, t.WorkDir)
	if err != nil {
		return nil, err
	}
	program, err := d.box.Executable(ctx, ToolClaude, cwd)
	if err != nil {
		return nil, fmt.Errorf("workspace: Claude Code is not installed where this conversation runs: %w", err)
	}
	escalated := d.box.Enforced() && d.box.CanEscalate()
	p := &Prepared{
		Cwd: cwd, SettingsDigest: r.settingsDigest(d), ArgumentsDigest: digest(call.Arguments),
		Program: program, Args: claudeArgs(t.Prompt), TimeoutSec: cmp.Or(t.TimeoutSec, r.maxTimeoutSec),
		Sandboxed: d.box.Enforced() && !escalated, Escalated: escalated,
	}
	if escalated {
		p.Justification = claudeJustification
	}
	return p, nil
}

type ClaudeOutput struct {
	Result    string `json:"result"`
	IsError   bool   `json:"is_error,omitempty"`
	Subtype   string `json:"subtype,omitempty"`
	NumTurns  int    `json:"num_turns,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	WorkDir   string `json:"work_dir"`
}

func (r *Provider) runClaude(ctx context.Context, call tool.Call, p *Prepared) (kit.Result, error) {
	res, err := r.execute(ctx, call, p, ToolClaude)
	if err != nil {
		return kit.Result{}, err
	}
	var out ClaudeOutput
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil || (out.Result == "" && out.Subtype == "") {
		return kit.Result{Output: commandOutput(res, p.Cwd)}, nil
	}
	out.WorkDir = p.Cwd
	return kit.Result{Output: out}, nil
}
