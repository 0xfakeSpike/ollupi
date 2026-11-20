package info

import (
	"context"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

type Provider struct{ st store.Store }

func New(st store.Store) Provider { return Provider{st: st} }

func (Provider) Name() (string, string) {
	return agent.InfoProvider, "Tells which conversation and workspace the agent works in, and the user's workspaces."
}

type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Root string `json:"root"`
}

type Info struct {
	Conversation string      `json:"conversation"`
	Workspace    string      `json:"workspace,omitempty"`
	Workspaces   []Workspace `json:"workspaces"`
}

func (Provider) Tools(context.Context) ([]kit.Spec, error) {
	return []kit.Spec{{
		Name: agent.InfoTool,
		Description: "Tells the id of this conversation and of the workspace it works in (none when it has no workspace), " +
			"and lists the user's workspaces with their ids, names and folders. " +
			"Pass a workspace id to " + string(agent.GeneralSub) + " to have the subagent work in that workspace.",
		InputSchema: kit.InputSchema[struct{}](),
		Annotations: tool.Annotations{Title: "Conversation info", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)},
	}}, nil
}

func (p Provider) Invoke(ctx context.Context, c tool.Call) (kit.Result, error) {
	var all []account.Workspace
	if err := p.st.Read(ctx, func(r store.Reader) (err error) { all, err = r.Workspaces(c.User); return err }); err != nil {
		return kit.Result{}, err
	}
	out := Info{Conversation: c.Thread, Workspace: c.Workspace, Workspaces: []Workspace{}}
	for _, w := range all {
		if w.Archived == nil {
			out.Workspaces = append(out.Workspaces, Workspace{ID: w.ID, Name: w.Name, Root: w.Root})
		}
	}
	return kit.Result{Output: out}, nil
}
