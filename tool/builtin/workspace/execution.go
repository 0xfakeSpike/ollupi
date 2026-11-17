package workspace

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

// Commands are traced through the global TracerProvider, as the agent's runs are.

func (r *Provider) prepareCommand(ctx context.Context, caller tool.Caller, args map[string]any) (*Prepared, error) {
	d, err := r.dirs(ctx, caller)
	if err != nil {
		return nil, err
	}
	program, err := programOf(args)
	if err != nil {
		return nil, err
	}
	argv := parseExecArgsSlice(args["args"])
	cwd, err := d.workDir(ctx, args)
	if err != nil {
		return nil, err
	}
	content := contentOf(program, argv)
	for _, blocked := range r.blacklist {
		if blacklisted(content, blocked) {
			return nil, fmt.Errorf("workspace: command is forbidden by the blacklist entry %q: %s", blocked, strings.Join(append([]string{program}, argv...), " "))
		}
	}
	timeout := r.timeoutSec
	if raw, ok := args["timeout_sec"]; ok {
		timeout, err = parseTimeoutSec(raw, r.maxTimeoutSec)
		if err != nil {
			return nil, err
		}
	}
	env, err := commandEnvironment(args["env"])
	if err != nil {
		return nil, err
	}
	escalated, justification, err := escalation(d.box, args)
	if err != nil {
		return nil, err
	}
	program, err = d.box.Executable(ctx, program, cwd)
	if err != nil {
		return nil, err
	}
	return &Prepared{
		Cwd: cwd, SettingsDigest: r.settingsDigest(d), ArgumentsDigest: digest(args),
		Program: program, Args: append([]string(nil), argv...), Env: env, TimeoutSec: timeout,
		Sandboxed: d.box.Enforced() && !escalated, Escalated: escalated, Justification: justification,
	}, nil
}

func escalation(box sandbox.Box, args map[string]any) (bool, string, error) {
	// The schema offers sandbox_permissions, as one of its two values, only where commands can escalate.
	if args["sandbox_permissions"] != sandboxRequireEscalated {
		return false, "", nil
	}
	justification, _ := args["justification"].(string)
	justification = strings.TrimSpace(justification)
	if !box.Enforced() {
		return false, "", fmt.Errorf("workspace: commands already run without a sandbox here; omit sandbox_permissions")
	}
	if !box.CanEscalate() {
		return false, "", fmt.Errorf("workspace: commands cannot leave this sandbox; omit sandbox_permissions")
	}
	if justification == "" {
		return false, "", fmt.Errorf("workspace: sandbox_permissions %q needs a justification for the user", sandboxRequireEscalated)
	}
	return true, justification, nil
}

func commandEnvironment(raw any) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	values, _ := raw.(map[string]any)
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		value, _ := v.(string)
		if sandbox.ValidateEnvNames([]string{k}) != nil || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("workspace: invalid environment variable %q; names are letters, digits and _, not starting with a digit", k)
		}
		out[k] = value
	}
	return out, nil
}

type CommandOutput struct {
	Stdout         string   `json:"stdout"`
	Stderr         string   `json:"stderr"`
	ExitCode       int      `json:"exit_code"`
	WorkDir        string   `json:"work_dir"`
	Signal         string   `json:"signal,omitempty"`
	Truncated      bool     `json:"truncated,omitempty"`
	SandboxDenials []string `json:"sandbox_denials,omitempty"`
	Sandbox        string   `json:"sandbox,omitempty"`
}

func (r *Provider) runCommand(ctx context.Context, caller tool.Caller, args map[string]any, p *Prepared) (kit.Result, error) {
	d, err := r.dirs(ctx, caller)
	if err != nil {
		return kit.Result{}, err
	}
	if err := r.validatePrepared(ctx, d, p, args); err != nil {
		return kit.Result{}, err
	}
	if err := d.sameExecutable(ctx, p.Program, p); err != nil {
		return kit.Result{}, err
	}
	res, err := d.box.Run(ctx, sandbox.Command{
		Program: p.Program, Args: p.Args, Cwd: p.Cwd, Env: p.Env,
		Timeout: time.Duration(p.TimeoutSec) * time.Second,

		Roots:       d.roots,
		Unsandboxed: p.Escalated,
	})
	if err != nil {
		return kit.Result{}, err
	}
	out := CommandOutput{Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode, WorkDir: p.Cwd,
		Signal: res.Signal, Truncated: res.Truncated, SandboxDenials: res.Denials}
	if p.Escalated {
		out.Sandbox = "escalated: ran on the host without the sandbox"
	}
	return kit.Result{Output: out}, nil
}

const ToolRunCommand = "run_command"

const (
	sandboxUseDefault       = "use_default"
	sandboxRequireEscalated = "require_escalated"
)

func (r *Provider) tool() kit.Spec {
	desc := "Run a command: set arguments.program to argv0 (a binary on PATH or an absolute path). work_dir defaults to the project root, or outside a project to " + r.box.Roots()[0] +
		", and must stay inside the project (outside a project: inside " + strings.Join(r.box.Roots(), ", ") + "). " + r.box.Summary() +
		". A non-zero exit_code is returned with the output, not as an error. To read, find or search files use " +
		ToolReadFile + ", " + ToolGlob + " and " + ToolGrep + ", not cat, find or grep"
	if r.box.Enforced() {
		desc += ". Commands run in the sandbox without asking the user"
	}
	if r.box.CanEscalate() {
		desc += ". A result's sandbox_denials lists what the sandbox stopped. " +
			"When a command fails because of the sandbox (a write outside the command directories or to a protected file, " +
			"a host outside the allowed domains, the network, a local socket), " +
			"retry it with sandbox_permissions \"require_escalated\" and a one-sentence justification: " +
			"the command then runs on the host without the sandbox, and the user is asked first unless the approval policy never asks. " +
			"Do not escalate to get around a denied path on purpose, and do not escalate commands that work in the sandbox"
	}
	if r.boxIn != nil {
		desc += ". A conversation runs in the environment it was started in, whose folders need not be this machine's"
	}

	return kit.Spec{
		Name:        ToolRunCommand,
		Description: desc,
		InputSchema: r.inputSchema(),
		Annotations: tool.Annotations{DestructiveHint: kit.Hint(true), OpenWorldHint: kit.Hint(true)},

		Content: permissionContent,
		Prepare: r.locals()[ToolRunCommand].plan,
	}
}

func toolInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"program": map[string]any{
				"type":        "string",
				"description": "argv0: executable name or path (e.g. opencli, npm, git, sh, bash)",
			},
			"args": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "Argv after program. For sh/bash/zsh/dash: use [\"-c\", \"whole shell command\"] for one-liners; " +
					"otherwise the first arg is a script path and the rest are its arguments (sh myscript.sh a b). For other programs use normal argv.",
			},
			"work_dir": map[string]any{
				"type":        "string",
				"description": "Optional command directory, relative to the current project/default command directory; must stay within this turn's allowed directories",
			},
			"timeout_sec": map[string]any{
				"type":        "integer",
				"description": "Optional timeout in seconds",
			},
			"env": map[string]any{
				"type":                 "object",
				"description":          "Optional environment variables",
				"additionalProperties": map[string]any{"type": "string"},
			},
		},
		"required":             []string{"program"},
		"additionalProperties": false,
	}
}

func (r *Provider) inputSchema() map[string]any {
	schema := toolInputSchema()
	if !r.box.CanEscalate() {
		return schema
	}
	props := schema["properties"].(map[string]any)
	props["sandbox_permissions"] = map[string]any{
		"type":        "string",
		"enum":        []string{sandboxUseDefault, sandboxRequireEscalated},
		"description": "use_default (the default) runs in the sandbox; require_escalated runs this command outside the sandbox, and its justification is shown to the user",
	}
	props["justification"] = map[string]any{
		"type":        "string",
		"description": "Required with require_escalated: one sentence telling the user what the command needs that the sandbox does not allow, such as writing /usr/local or reaching a host",
	}
	return schema
}

// The tool's schema makes args an array of strings.
func parseExecArgsSlice(v any) []string {
	if v == nil {
		return nil
	}
	arr, _ := v.([]any)
	out := make([]string, len(arr))
	for i, el := range arr {
		out[i], _ = el.(string)
	}
	return out
}

// The tool's schema makes timeout_sec an integer.
func parseTimeoutSec(v any, limit int) (int, error) {
	sec, _ := v.(float64)
	if sec <= 0 {
		return 0, fmt.Errorf("workspace: argument \"timeout_sec\" must be > 0")
	}
	if sec > float64(limit) {
		return 0, fmt.Errorf("workspace: argument \"timeout_sec\" must be at most %d", limit)
	}
	return int(sec), nil
}
