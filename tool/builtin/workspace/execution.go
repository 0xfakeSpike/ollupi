package workspace

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
)

type commandArgs struct {
	Program            string            `json:"program" jsonschema:"argv0: executable name or path (e.g. opencli, npm, git, sh, bash)"`
	Args               []string          `json:"args,omitempty" jsonschema:"Argv after program. For sh/bash/zsh/dash: use [\"-c\", \"whole shell command\"] for one-liners; otherwise the first arg is a script path and the rest are its arguments (sh myscript.sh a b). For other programs use normal argv."`
	WorkDir            string            `json:"work_dir,omitempty" jsonschema:"Optional command directory, relative to the current project/default command directory; must stay within this turn's allowed directories"`
	TimeoutSec         int               `json:"timeout_sec,omitempty" jsonschema:"Optional timeout in seconds"`
	Env                map[string]string `json:"env,omitempty" jsonschema:"Optional environment variables"`
	SandboxPermissions string            `json:"sandbox_permissions,omitempty" jsonschema:"use_default (the default) runs in the sandbox; require_escalated runs this command outside the sandbox, and its justification is shown to the user"`
	Justification      string            `json:"justification,omitempty" jsonschema:"Required with require_escalated: one sentence telling the user what the command needs that the sandbox does not allow, such as writing /usr/local or reaching a host"`
}

func (commandArgs) Constrain(s *jsonschema.Schema) {
	constrainRun(s)
	s.Properties["sandbox_permissions"].Enum = []any{sandboxUseDefault, sandboxRequireEscalated}
}

func constrainRun(s *jsonschema.Schema) {
	s.Properties["work_dir"].MinLength = jsonschema.Ptr(1)
	s.Properties["timeout_sec"].Minimum = jsonschema.Ptr(1.0)
}

func (r *Provider) inputSchema() *jsonschema.Schema {
	s := kit.InputSchema[commandArgs]()
	s.Properties["timeout_sec"].Maximum = jsonschema.Ptr(float64(r.maxTimeoutSec))
	if !r.box.CanEscalate() {
		delete(s.Properties, "sandbox_permissions")
		delete(s.Properties, "justification")
	}
	return s
}

func commandOf(args map[string]any) (commandArgs, error) {
	a, err := kit.DecodeArguments[commandArgs](args)
	if err != nil {
		return a, err
	}
	if a.Program = strings.TrimSpace(a.Program); a.Program == "" {
		return a, errProgram
	}
	for k, v := range a.Env {
		if sandbox.ValidateEnvNames([]string{k}) != nil || strings.ContainsRune(v, 0) {
			return a, fmt.Errorf("workspace: invalid environment variable %q; names are letters, digits and _, not starting with a digit", k)
		}
	}
	return a, nil
}

func (r *Provider) prepareCommand(ctx context.Context, call tool.Call) (*Prepared, error) {
	d, err := r.dirs(ctx, call.Caller)
	if err != nil {
		return nil, err
	}
	a, err := commandOf(call.Arguments)
	if err != nil {
		return nil, err
	}
	cwd, err := d.resolve(ctx, a.WorkDir)
	if err != nil {
		return nil, err
	}
	content := contentOf(a.Program, a.Args)
	for _, blocked := range r.blacklist {
		if blacklisted(content, blocked) {
			return nil, fmt.Errorf("workspace: command is forbidden by the blacklist entry %q: %s", blocked, strings.Join(append([]string{a.Program}, a.Args...), " "))
		}
	}
	escalated, err := escalation(d.box, a)
	if err != nil {
		return nil, err
	}
	program, err := d.box.Executable(ctx, a.Program, cwd)
	if err != nil {
		return nil, err
	}
	p := &Prepared{
		Cwd: cwd, SettingsDigest: r.settingsDigest(d), ArgumentsDigest: digest(call.Arguments),
		Program: program, Args: a.Args, Env: a.Env, TimeoutSec: cmp.Or(a.TimeoutSec, r.timeoutSec),
		Sandboxed: d.box.Enforced() && !escalated, Escalated: escalated,
	}
	if escalated {
		p.Justification = strings.TrimSpace(a.Justification)
	}
	return p, nil
}

func escalation(box sandbox.Box, a commandArgs) (bool, error) {
	if a.SandboxPermissions != sandboxRequireEscalated {
		return false, nil
	}
	if !box.Enforced() {
		return false, fmt.Errorf("workspace: commands already run without a sandbox here; omit sandbox_permissions")
	}
	if !box.CanEscalate() {
		return false, fmt.Errorf("workspace: commands cannot leave this sandbox; omit sandbox_permissions")
	}
	if strings.TrimSpace(a.Justification) == "" {
		return false, fmt.Errorf("workspace: sandbox_permissions %q needs a justification for the user", sandboxRequireEscalated)
	}
	return true, nil
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

func commandOutput(res sandbox.Result, cwd string) CommandOutput {
	return CommandOutput{Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode, WorkDir: cwd,
		Signal: res.Signal, Truncated: res.Truncated, SandboxDenials: res.Denials}
}

func (r *Provider) execute(ctx context.Context, call tool.Call, p *Prepared, program string) (sandbox.Result, error) {
	d, err := r.dirs(ctx, call.Caller)
	if err != nil {
		return sandbox.Result{}, err
	}
	if err := r.validatePrepared(ctx, d, p, call.Arguments); err != nil {
		return sandbox.Result{}, err
	}
	current, err := d.box.Executable(ctx, program, p.Cwd)
	if err != nil {
		return sandbox.Result{}, err
	}
	if current != p.Program {
		return sandbox.Result{}, fmt.Errorf("workspace: approved executable now resolves to a different path")
	}
	return d.box.Run(ctx, sandbox.Command{
		Program: p.Program, Args: p.Args, Cwd: p.Cwd, Env: p.Env,
		Timeout: time.Duration(p.TimeoutSec) * time.Second,

		Roots:       d.roots,
		Unsandboxed: p.Escalated,
	})
}

func (r *Provider) runCommand(ctx context.Context, call tool.Call, p *Prepared) (kit.Result, error) {
	a, err := commandOf(call.Arguments)
	if err != nil {
		return kit.Result{}, err
	}
	res, err := r.execute(ctx, call, p, a.Program)
	if err != nil {
		return kit.Result{}, err
	}
	out := commandOutput(res, p.Cwd)
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

func (r *Provider) commandTool() kit.Spec {
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
		Annotations: tool.Annotations{DestructiveHint: new(true), OpenWorldHint: new(true)},
		Background:  tool.BackgroundOptional,

		Content: permissionContent,
		Prepare: r.locals()[ToolRunCommand].plan,
	}
}
