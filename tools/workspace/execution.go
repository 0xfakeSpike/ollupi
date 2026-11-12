package workspace

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tools"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Commands are traced through the global TracerProvider, as the agent's runs are.
var tracer = otel.Tracer("github.com/0xfakeSpike/ollupi/tools/workspace")

func (r *Provider) prepareCommand(ctx context.Context, args map[string]any) (*tools.Prepared, error) {
	d, err := r.dirs(ctx)
	if err != nil {
		return nil, err
	}
	program, ok := args["program"].(string)
	if !ok || strings.TrimSpace(program) == "" {
		return nil, fmt.Errorf("workspace: argument program must be a non-empty string")
	}
	program = strings.TrimSpace(program)
	argv, err := parseExecArgsSlice(args["args"])
	if err != nil {
		return nil, err
	}
	requested := ""
	if raw, ok := args["work_dir"]; ok {
		requested, ok = raw.(string)
		if !ok || strings.TrimSpace(requested) == "" {
			return nil, fmt.Errorf("workspace: argument work_dir must be a non-empty string")
		}
	}
	cwd, err := d.resolve(ctx, requested)
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
	return &tools.Prepared{
		Cwd: cwd, SettingsDigest: r.settingsDigest(d), ArgumentsDigest: digest(args),
		Program: program, Args: append([]string(nil), argv...), Env: env, TimeoutSec: timeout,
		Sandboxed: d.box.Enforced() && !escalated, Escalated: escalated, Justification: justification,
	}, nil
}

func escalation(box sandbox.Box, args map[string]any) (bool, string, error) {
	mode := sandboxUseDefault
	if raw, ok := args["sandbox_permissions"]; ok {
		s, ok := raw.(string)
		if !ok || (s != sandboxUseDefault && s != sandboxRequireEscalated) {
			return false, "", fmt.Errorf("workspace: argument sandbox_permissions must be %q or %q", sandboxUseDefault, sandboxRequireEscalated)
		}
		mode = s
	}
	justification := ""
	if raw, ok := args["justification"]; ok {
		s, ok := raw.(string)
		if !ok {
			return false, "", fmt.Errorf("workspace: argument justification must be a string")
		}
		justification = strings.TrimSpace(s)
	}
	if mode != sandboxRequireEscalated {
		return false, "", nil
	}
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
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("workspace: argument env must be an object")
	}

	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		value, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("workspace: env value for %q must be string", k)
		}
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

func (r *Provider) runCommand(ctx context.Context, args map[string]any, p *tools.Prepared) (tools.Result, error) {
	d, err := r.dirs(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	if err := r.validatePrepared(ctx, d, p, args); err != nil {
		return tools.Result{}, err
	}
	if p.Program == "" || p.TimeoutSec <= 0 {
		return tools.Result{}, fmt.Errorf("workspace: incomplete prepared command")
	}

	current, err := d.box.Executable(ctx, p.Program, p.Cwd)
	if err != nil {
		return tools.Result{}, err
	}
	if current != p.Program {
		return tools.Result{}, fmt.Errorf("workspace: approved executable now resolves to a different path")
	}
	// Only the program's name goes into the trace: its path and arguments can name the user's files.
	ctx, span := tracer.Start(ctx, "ollupi.sandbox.run", trace.WithAttributes(
		attribute.String("process.executable.name", path.Base(p.Program)), attribute.Bool("ollupi.sandbox.escalated", p.Escalated),
		attribute.Bool("ollupi.sandbox.enforced", d.box.Enforced())))
	defer span.End()
	res, err := d.box.Run(ctx, sandbox.Command{
		Program: p.Program, Args: p.Args, Cwd: p.Cwd, Env: p.Env,
		Timeout: time.Duration(p.TimeoutSec) * time.Second,

		Roots:       d.roots,
		Unsandboxed: p.Escalated,
	})
	if err != nil {
		span.SetStatus(codes.Error, "")
		return tools.Result{}, err
	}
	span.SetAttributes(attribute.Int("process.exit.code", res.ExitCode), attribute.Int("ollupi.sandbox.denials", len(res.Denials)),
		attribute.Bool("ollupi.sandbox.truncated", res.Truncated))
	out := CommandOutput{Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode, WorkDir: p.Cwd,
		Signal: res.Signal, Truncated: res.Truncated, SandboxDenials: res.Denials}
	if p.Escalated {
		out.Sandbox = "escalated: ran on the host without the sandbox"
	}
	return tools.Result{Output: out}, nil
}

func (r *Provider) validatePrepared(ctx context.Context, d dirs, p *tools.Prepared, args map[string]any) error {
	if p == nil {
		return fmt.Errorf("workspace: missing prepared command")
	}
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
