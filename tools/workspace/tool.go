package workspace

import (
	"context"
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tools"
)

type Options struct {
	Config    Config
	Agent     AgentFiles
	DenyRead  []string
	EnvMarker string
	// Box serves any user BoxFor does not; by default a sandbox on this machine made from Config.
	Box sandbox.Box
	// BoxFor keeps a box's lifetime: this package only closes Box.
	BoxFor func(user string) (sandbox.Box, error)
	// BoxIn has the last word over BoxFor: a nil box with no error leaves the call in Box. It keeps a box's
	// lifetime as BoxFor does.
	BoxIn func(caller tools.Caller) (sandbox.Box, error)
}

type Provider struct {
	box           sandbox.Box
	boxFor        func(user string) (sandbox.Box, error)
	boxIn         func(caller tools.Caller) (sandbox.Box, error)
	timeoutSec    int
	maxTimeoutSec int
	blacklist     []string
}

func New(o Options) (*Provider, error) {
	c := o.Config
	c.WorkspaceDirs = append([]string(nil), c.WorkspaceDirs...)
	if o.Box != nil {
		c.WorkspaceDirs = o.Box.Roots()
	}
	if err := c.normalize(o.Agent.Home); err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	box := o.Box
	if box == nil {
		cfg := sandboxConfig(o, c)
		cfg.Roots = make([]string, 0, len(c.WorkspaceDirs))
		for _, r := range c.WorkspaceDirs {
			abs, err := sandbox.CanonicalDirectory(r)
			if err != nil {
				return nil, fmt.Errorf("workspace: workspace dir: %w", err)
			}
			cfg.Roots = append(cfg.Roots, abs)
		}
		local, err := sandbox.New(cfg)
		if err != nil {
			return nil, fmt.Errorf("workspace: %w", err)
		}
		box = local
	}
	return &Provider{box: box, boxFor: o.BoxFor, boxIn: o.BoxIn, timeoutSec: c.CommandTimeoutSec, maxTimeoutSec: c.MaxCommandTimeoutSec, blacklist: append([]string(nil), c.CommandBlacklist...)}, nil
}

func (r *Provider) Close() error { return r.box.Close() }

const ProviderName = "workspace"

func (r *Provider) Name() (string, string) {
	return ProviderName, "Run commands and edit files in the working folders, under the sandbox unless it is turned off."
}

func (r *Provider) Invoke(ctx context.Context, caller tools.Caller, tool string, args map[string]any) (tools.Result, error) {
	switch tool {
	case ToolReadFile, ToolGlob, ToolGrep:
		return r.invokeRead(ctx, caller, tool, args)
	}
	return tools.Result{}, fmt.Errorf("workspace: %s runs only inside a turn", tool)
}

func (r *Provider) ToolList(context.Context) ([]*tools.Spec, error) {
	return slices.Concat([]*tools.Spec{r.tool(), r.claudeTool()}, r.fileTools(), r.readTools()), nil
}
