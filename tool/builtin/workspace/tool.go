package workspace

import (
	"context"
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

type Options struct {
	Config Config
	// Boxes finds the box of the environment a call's conversation runs in.
	Boxes sandbox.Boxes
}

type Provider struct {
	boxes         sandbox.Boxes
	timeoutSec    int
	maxTimeoutSec int
	blacklist     []string
}

func New(o Options) (*Provider, error) {
	c := o.Config
	if err := c.normalize(); err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if o.Boxes == nil {
		return nil, fmt.Errorf("workspace: no environments to run commands in")
	}
	return &Provider{boxes: o.Boxes, timeoutSec: c.CommandTimeoutSec, maxTimeoutSec: c.MaxCommandTimeoutSec, blacklist: append([]string(nil), c.CommandBlacklist...)}, nil
}

const providerName = "workspace"

func (r *Provider) Name() (string, string) {
	return providerName, "Run commands and edit files in the working folders, under the sandbox unless it is turned off."
}

func (r *Provider) Invoke(ctx context.Context, c tool.Call) (kit.Result, error) {
	if l, ok := r.locals()[c.Tool]; ok {
		return l.invoke(ctx, c)
	}
	return r.invokeRead(ctx, c.Caller, c.Tool, c.Arguments)
}

func (r *Provider) Tools(context.Context) ([]kit.Spec, error) {
	return slices.Concat([]kit.Spec{r.commandTool(), r.claudeTool()}, r.fileTools(), r.readTools()), nil
}
