package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
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
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "The task for Claude Code, with the files to look at and what done looks like",
				},
				"work_dir": map[string]any{
					"type":        "string",
					"description": "Optional folder Claude Code works in, relative to the project root; must stay within this turn's allowed directories",
				},
				"timeout_sec": map[string]any{
					"type":        "integer",
					"description": "Optional timeout in seconds",
				},
			},
			"required":             []string{"prompt"},
			"additionalProperties": false,
		},
		Annotations: tool.Annotations{Title: "Claude Code", DestructiveHint: kit.Hint(true), OpenWorldHint: kit.Hint(true)},
		Content:     claudePermissionContent,
		Prepare:     r.locals()[ToolClaude].plan,
	}
}

func claudeArgs(prompt string) []string {
	return append([]string{"-p", prompt}, claudeFlags...)
}

func claudePrompt(args map[string]any) (string, error) {
	prompt, _ := args["prompt"].(string)
	if prompt = strings.TrimSpace(prompt); prompt == "" {
		return "", fmt.Errorf("workspace: argument prompt must be a non-empty string")
	}
	return prompt, nil
}

func claudePermissionContent(_ context.Context, c tool.Call) (policy.Content, error) {
	arguments := c.Arguments
	prompt, err := claudePrompt(arguments)
	if err != nil {
		return policy.Content{}, err
	}
	words := append([]string{ToolClaude}, claudeArgs(prompt)...)
	return policy.Content{Text: strings.Join(words, " "), Match: line(words)}, nil
}

func (r *Provider) prepareClaude(ctx context.Context, caller tool.Caller, args map[string]any) (*Prepared, error) {
	d, err := r.dirs(ctx, caller)
	if err != nil {
		return nil, err
	}
	prompt, err := claudePrompt(args)
	if err != nil {
		return nil, err
	}
	cwd, err := d.workDir(ctx, args)
	if err != nil {
		return nil, err
	}
	timeout := r.maxTimeoutSec
	if raw, ok := args["timeout_sec"]; ok {
		if timeout, err = parseTimeoutSec(raw, r.maxTimeoutSec); err != nil {
			return nil, err
		}
	}
	program, err := d.box.Executable(ctx, ToolClaude, cwd)
	if err != nil {
		return nil, fmt.Errorf("workspace: Claude Code is not installed where this conversation runs: %w", err)
	}
	escalated := d.box.Enforced() && d.box.CanEscalate()
	p := &Prepared{
		Cwd: cwd, SettingsDigest: r.settingsDigest(d), ArgumentsDigest: digest(args),
		Program: program, Args: claudeArgs(prompt), TimeoutSec: timeout,
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

func (r *Provider) runClaude(ctx context.Context, caller tool.Caller, args map[string]any, p *Prepared) (kit.Result, error) {
	d, err := r.dirs(ctx, caller)
	if err != nil {
		return kit.Result{}, err
	}
	if err := r.validatePrepared(ctx, d, p, args); err != nil {
		return kit.Result{}, err
	}
	if err := d.sameExecutable(ctx, ToolClaude, p); err != nil {
		return kit.Result{}, err
	}
	res, err := d.box.Run(ctx, sandbox.Command{
		Program: p.Program, Args: p.Args, Cwd: p.Cwd,
		Timeout: time.Duration(p.TimeoutSec) * time.Second,

		Roots:       d.roots,
		Unsandboxed: p.Escalated,
	})
	if err != nil {
		return kit.Result{}, err
	}
	var out ClaudeOutput
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil || (out.Result == "" && out.Subtype == "") {
		return kit.Result{Output: CommandOutput{Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode, WorkDir: p.Cwd,
			Signal: res.Signal, Truncated: res.Truncated, SandboxDenials: res.Denials}}, nil
	}
	out.WorkDir = p.Cwd
	return kit.Result{Output: out}, nil
}
