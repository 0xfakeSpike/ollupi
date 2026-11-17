package workspace

import (
	"context"
	"slices"
	"testing"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tool"
)

// claudeMachine answers every command as Claude Code would.
type claudeMachine struct {
	*memoryMachine
	stdout string
}

func (m claudeMachine) Run(ctx context.Context, c sandbox.Command) (sandbox.Result, error) {
	if _, err := m.memoryMachine.Run(ctx, c); err != nil {
		return sandbox.Result{}, err
	}
	return sandbox.Result{Stdout: m.stdout}, nil
}

func TestClaudeRunsWithItsFlagsInTheUsersMachine(t *testing.T) {
	m := claudeMachine{memoryMachine: machineWith(map[string]string{"a.go": "package a\n"}),
		stdout: `{"type":"result","subtype":"success","is_error":false,"num_turns":3,"result":"done","session_id":"s1","total_cost_usd":0.4}`}
	r, err := New(Options{Box: sandbox.Remote(m, []string{machineRoot}, "cloud")})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	args := map[string]any{"prompt": " fix the test "}
	p, err := r.prepareClaude(context.Background(), tool.Caller{}, args)
	if err != nil {
		t.Fatal(err)
	}
	// The cloud sandbox cannot be left, so Claude Code runs confined in it.
	if !p.Sandboxed || p.Escalated || p.Program != ToolClaude || p.Cwd != machineRoot || p.TimeoutSec != r.maxTimeoutSec {
		t.Errorf("prepared %+v", p)
	}
	want := []string{"-p", "fix the test", "--output-format", "json", "--permission-mode", "acceptEdits",
		"--allowedTools", "Bash(go test *)", "--permission-prompts", "none", "--max-budget-usd", "2"}
	if !slices.Equal(p.Args, want) {
		t.Errorf("args %q, want %q", p.Args, want)
	}
	res, err := r.runClaude(context.Background(), tool.Caller{}, args, p)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := res.Output.(ClaudeOutput)
	if !ok || out.Result != "done" || out.NumTurns != 3 || out.SessionID != "s1" || out.WorkDir != machineRoot {
		t.Errorf("output %#v", res.Output)
	}
	if len(m.ran) != 1 || m.ran[0].Program != ToolClaude || m.ran[0].Unsandboxed {
		t.Errorf("ran %+v", m.ran)
	}
}

// Claude Code that fails before it answers comes back as what it printed.
func TestClaudeThatCannotAnswerReturnsItsOutput(t *testing.T) {
	m := claudeMachine{memoryMachine: machineWith(map[string]string{"a.go": "package a\n"}), stdout: "Not logged in"}
	r, err := New(Options{Box: sandbox.Remote(m, []string{machineRoot}, "cloud")})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	args := map[string]any{"prompt": "x"}
	p, err := r.prepareClaude(context.Background(), tool.Caller{}, args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.runClaude(context.Background(), tool.Caller{}, args, p)
	if err != nil {
		t.Fatal(err)
	}
	if out, ok := res.Output.(CommandOutput); !ok || out.Stdout != "Not logged in" {
		t.Errorf("output %#v", res.Output)
	}
}
