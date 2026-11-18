package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/google/jsonschema-go/jsonschema"
)

type Provider interface {
	Name() (id string, description string)
	Tools(ctx context.Context) ([]Spec, error)
	Invoke(ctx context.Context, c tool.Call) (Result, error)
}

type Spec struct {
	Name        string
	Description string
	InputSchema any
	Annotations tool.Annotations
	Content     func(ctx context.Context, c tool.Call) (policy.Content, error)
	Prepare     func(ctx context.Context, c tool.Call) (tool.Plan, error)
	Serial      bool
}

type Result struct {
	Output    any
	Artifacts []conversation.Artifact
	Ask       any
	Launch    *tool.Launch
}

var (
	ErrUnknownTool      = errors.New("unknown tool")
	ErrInvalidArguments = errors.New("invalid arguments")
)

type Registry struct {
	unavailable []string
	providers   map[string]Provider
	tools       map[string]entry
	listed      []tool.Spec
}

type entry struct {
	owner    string
	provider Provider
	spec     Spec
	schema   *jsonschema.Resolved
}

var _ tool.Set = (*Registry)(nil)

func New(ctx context.Context, unavailable []string, providers ...Provider) (*Registry, error) {
	r := &Registry{unavailable: slices.Clone(unavailable), providers: map[string]Provider{}, tools: map[string]entry{}}
	for _, p := range append(providers, introspection{r}) {
		id, _ := p.Name()
		if id == "" {
			return nil, errors.New("tool registry: a provider without a name")
		}
		if _, exists := r.providers[id]; exists {
			return nil, fmt.Errorf("tool registry: provider %q twice", id)
		}
		r.providers[id] = p
	}
	for _, id := range r.ids() {
		specs, err := r.providers[id].Tools(ctx)
		if err != nil {
			return nil, fmt.Errorf("tool registry: the tools of %q: %w", id, err)
		}
		for _, s := range specs {
			if s.Name == "" {
				return nil, fmt.Errorf("tool registry: provider %q has a tool without a name", id)
			}
			if prev, ok := r.tools[s.Name]; ok {
				return nil, fmt.Errorf("tool registry: tool %q twice (providers %q and %q)", s.Name, prev.owner, id)
			}
			resolved, schema, err := schemas(s.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("tool registry: tool %q: %w", s.Name, err)
			}
			r.tools[s.Name] = entry{owner: id, provider: r.providers[id], spec: s, schema: resolved}
			r.listed = append(r.listed, tool.Spec{Name: s.Name, Provider: id, Description: s.Description, Schema: schema,
				Annotations: s.Annotations, Content: s.Content != nil, Serial: s.Serial})
		}
	}
	sort.Slice(r.listed, func(i, j int) bool {
		if r.listed[i].Provider != r.listed[j].Provider {
			return r.listed[i].Provider < r.listed[j].Provider
		}
		return r.listed[i].Name < r.listed[j].Name
	})
	return r, nil
}

func (r *Registry) ids() []string {
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (r *Registry) Specs() []tool.Spec    { return slices.Clone(r.listed) }
func (r *Registry) Unavailable() []string { return slices.Clone(r.unavailable) }

func (r *Registry) check(c tool.Call) (entry, error) {
	e, ok := r.tools[c.Tool]
	if !ok {
		return entry{}, fmt.Errorf("tool registry: %w %q", ErrUnknownTool, c.Tool)
	}
	if err := validate(e.schema, c.Arguments); err != nil {
		return entry{}, fmt.Errorf("tool registry: %w for %q: %v", ErrInvalidArguments, c.Tool, err)
	}
	return e, nil
}

func (r *Registry) Content(ctx context.Context, c tool.Call) (content policy.Content, err error) {
	e, err := r.check(c)
	if err != nil {
		return policy.Content{}, err
	}
	defer recovered(&err, "reading what the call touches")
	return e.spec.Content(ctx, c)
}

func (r *Registry) Prepare(ctx context.Context, c tool.Call) (plan tool.Plan, err error) {
	e, err := r.check(c)
	if err != nil || e.spec.Prepare == nil {
		return tool.Plan{}, err
	}
	defer recovered(&err, "preparing")
	return e.spec.Prepare(ctx, c)
}

func recovered(err *error, while string) {
	if p := recover(); p != nil {
		*err = fmt.Errorf("the tool crashed while %s: %v", while, p)
	}
}

const (
	maxOutputBytes = 1 << 20
	maxErrorBytes  = 16 << 10
)

func (r *Registry) Invoke(ctx context.Context, c tool.Call) (out tool.Result) {
	e, err := r.check(c)
	if err != nil {
		return tool.Result{Error: err.Error()}
	}
	defer func() {
		if p := recover(); p != nil {
			out = tool.Result{Error: fmt.Sprintf("the tool crashed: %v", p)}
		}
	}()
	res, err := e.provider.Invoke(ctx, c)
	return settle(res, err)
}

func settle(res Result, err error) tool.Result {
	out := tool.Result{Artifacts: res.Artifacts, Launch: res.Launch}
	if res.Ask != nil {
		raw, err := json.Marshal(res.Ask)
		if err != nil {
			return tool.Result{Error: fmt.Sprintf("the tool asked a question that cannot be encoded as JSON: %v", err)}
		}
		out.Ask = raw
	}
	if res.Output != nil {
		raw, err := json.Marshal(res.Output)
		switch {
		case err != nil:
			return tool.Result{Error: fmt.Sprintf("the tool returned output that cannot be encoded as JSON: %v", err)}
		case len(raw) > maxOutputBytes:
			raw, _ = json.Marshal(clip(string(raw), maxOutputBytes, "output"))
		}
		out.Output = raw
	}
	if err != nil {
		out.Error = err.Error()
		if len(out.Error) > maxErrorBytes {
			out.Error = clip(out.Error, maxErrorBytes, "error")
		}
	}
	return out
}

func clip(text string, limit int, what string) string {
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + fmt.Sprintf("\n[%s truncated: %d of %d bytes shown]", what, cut, len(text))
}

const (
	introspectionProvider = "tool_registry"
	ListProvidersTool     = "list_tool_providers"
	ListProviderToolsTool = "list_tools_for_provider"
)

type introspection struct{ r *Registry }

func (introspection) Name() (string, string) {
	return introspectionProvider, "Lists the assistant's tool providers and their tools."
}

func (introspection) Tools(context.Context) ([]Spec, error) {
	readCatalog := tool.Annotations{ReadOnlyHint: true, OpenWorldHint: new(false)}
	return []Spec{
		{
			Name: ListProvidersTool, Annotations: readCatalog,
			Description: "List tool providers with their id and description. Use only when the user asks what the assistant can do.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
		{
			Name: ListProviderToolsTool, Annotations: readCatalog,
			Description: "List the tools of one provider. Use only when the user asks about a provider's capabilities.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"provider": map[string]any{"type": "string", "minLength": 1, "description": `Provider id from list_tool_providers.`},
				},
				"required":             []string{"provider"},
				"additionalProperties": false,
			},
		},
	}, nil
}

type listed struct {
	Provider    string           `json:"provider"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Annotations tool.Annotations `json:"annotations"`
	InputSchema map[string]any   `json:"input_schema"`
}

func (i introspection) Invoke(_ context.Context, c tool.Call) (Result, error) {
	switch c.Tool {
	case ListProvidersTool:
		providers := make([]map[string]string, 0, len(i.r.providers))
		for _, id := range i.r.ids() {
			_, description := i.r.providers[id].Name()
			providers = append(providers, map[string]string{"id": id, "description": description})
		}
		return Result{Output: map[string]any{"providers": providers}}, nil
	case ListProviderToolsTool:
		provider, _ := c.Arguments["provider"].(string)
		if _, ok := i.r.providers[provider]; !ok {
			return Result{}, fmt.Errorf("tool registry: unknown provider %q", provider)
		}
		out := []listed{}
		for _, s := range i.r.listed {
			if s.Provider == provider {
				out = append(out, listed{Provider: s.Provider, Name: s.Name, Description: s.Description, Annotations: s.Annotations, InputSchema: s.Schema})
			}
		}
		return Result{Output: map[string]any{"provider": provider, "tools": out}}, nil
	}
	return Result{}, fmt.Errorf("tool registry: %w %q", ErrUnknownTool, c.Tool)
}
