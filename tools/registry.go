package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
)

type Registry struct {
	unavailable []string
	providers   map[string]Provider
	owner       map[string]Provider
	specs       map[string]*Spec
	// schemas are the specs' input schemas, resolved once.
	schemas     map[string]*jsonschema.Resolved
	descriptors []ToolDescriptor
}

const ProviderSeparator = "__"

// Set is what one user adds to the tools everyone has, with the providers of theirs that could not be reached. The
// same Set means the same tools.
type Set struct {
	Providers   []Provider
	Unavailable []string
}

func New(ctx context.Context, unavailable []string, providers ...Provider) (*Registry, error) {
	r := &Registry{
		unavailable: slices.Clone(unavailable),
		providers:   make(map[string]Provider, len(providers)+1),
		owner:       make(map[string]Provider),
		specs:       make(map[string]*Spec),
		schemas:     make(map[string]*jsonschema.Resolved),
	}
	for _, p := range append(providers, newIntrospection(r)) {
		id, _ := p.Name()
		if id == "" {
			return nil, fmt.Errorf("tool registry: provider with empty name")
		}
		if _, exists := r.providers[id]; exists {
			return nil, fmt.Errorf("tool registry: duplicate provider %q", id)
		}
		r.providers[id] = p
	}
	for _, id := range r.providerIDs() {
		p := r.providers[id]
		list, err := p.ToolList(ctx)
		if err != nil {
			return nil, fmt.Errorf("tool registry: list tools of %q: %w", id, err)
		}
		for _, t := range list {
			if t.Name == "" {
				return nil, fmt.Errorf("tool registry: provider %q exposes a tool without a name", id)
			}
			if prev, ok := r.owner[t.Name]; ok {
				prevID, _ := prev.Name()
				return nil, fmt.Errorf("tool registry: duplicate tool %q (providers %q and %q)", t.Name, prevID, id)
			}
			schema, err := ResolveSchema(t.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("tool registry: tool %q input schema: %w", t.Name, err)
			}
			r.owner[t.Name] = p
			r.specs[t.Name] = t
			r.schemas[t.Name] = schema
		}
	}
	descriptors, err := r.buildDescriptors()
	if err != nil {
		return nil, err
	}
	r.descriptors = descriptors
	return r, nil
}

func (r *Registry) buildDescriptors() ([]ToolDescriptor, error) {
	out := make([]ToolDescriptor, 0, len(r.specs))
	for name, spec := range r.specs {
		schema, err := normalizeInputSchema(spec.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool registry: tool %q input schema: %w", name, err)
		}
		id, _ := r.owner[name].Name()
		descriptor := ToolDescriptor{Provider: id, Name: name, Description: spec.Description, InputSchema: schema, Serial: spec.Serial}
		if spec.Annotations != nil {
			descriptor.Annotations = *spec.Annotations
		}
		out = append(out, descriptor)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider == out[j].Provider {
			return out[i].Name < out[j].Name
		}
		return out[i].Provider < out[j].Provider
	})
	return out, nil
}

func (r *Registry) Unavailable(tool string) bool {
	provider, _, ok := strings.Cut(tool, ProviderSeparator)
	return ok && slices.Contains(r.unavailable, provider)
}

func (r *Registry) Offers(tool string) bool {
	_, ok := r.specs[tool]
	return ok
}

func (r *Registry) HasProvider(id string) bool {
	_, ok := r.providers[id]
	return ok
}

func (r *Registry) Descriptors() []ToolDescriptor {
	return slices.Clone(r.descriptors)
}

var (
	ErrUnknownTool      = errors.New("unknown tool")
	ErrInvalidArguments = errors.New("invalid arguments")
)

// A tool's output, and what it says of its failure, are kept up to these sizes: both are stored with the call and sent
// to those who watch it, and a server may fail with as much text as it answers with.
const (
	maxOutputBytes = 1 << 20
	maxErrorBytes  = 16 << 10
)

func guard(tool string, run func() (Result, error)) (result Result) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("tool crashed", "tool", tool, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			result = Result{Err: fmt.Errorf("the tool crashed: %v", p)}.For(tool)
		}
	}()
	result, err := run()
	if err != nil {
		result.Err = err
	}
	if result.Output != nil {
		raw, err := json.Marshal(result.Output)
		switch {
		case err != nil:
			result = Result{Err: fmt.Errorf("the tool returned output that cannot be encoded as JSON: %v", err)}
		case len(raw) > maxOutputBytes:
			result.Output = clip(string(raw), maxOutputBytes, "output")
		}
	}
	if result.Err != nil {
		if why := result.Err.Error(); len(why) > maxErrorBytes {
			result.Err = errors.New(clip(why, maxErrorBytes, "error"))
		}
	}
	return result.For(tool)
}

// clip keeps the first limit bytes of text and says how much that is. The cut goes back to the start of a character,
// so the text kept stays valid UTF-8.
func clip(text string, limit int, what string) string {
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + fmt.Sprintf("\n[%s truncated: %d of %d bytes shown]", what, cut, len(text))
}

// check says the registry has the tool and the arguments fit its schema.
func (r *Registry) check(tool string, arguments map[string]any) error {
	schema, ok := r.schemas[tool]
	if !ok {
		return fmt.Errorf("tool registry: %w %q", ErrUnknownTool, tool)
	}
	if err := validate(schema, arguments); err != nil {
		return fmt.Errorf("tool registry: %w for %q: %v", ErrInvalidArguments, tool, err)
	}
	return nil
}

func (r *Registry) HasPermissionContent(tool string) bool {
	spec, ok := r.specs[tool]
	return ok && spec.PermissionContent != nil
}

func (r *Registry) PermissionContent(ctx context.Context, caller Caller, tool string, arguments map[string]any) (content Content, ok bool, err error) {
	if !r.HasPermissionContent(tool) {
		return Content{}, false, nil
	}
	content, err = r.specs[tool].PermissionContent(ctx, caller, arguments)
	if err != nil {
		return Content{}, false, fmt.Errorf("tool registry: permission content of %q: %w", tool, err)
	}
	return content, true, nil
}

func (r *Registry) providerIDs() []string {
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (r *Registry) Prepare(ctx context.Context, caller Caller, tool string, arguments map[string]any) (prepared *Prepared, err error) {
	if err := r.check(tool, arguments); err != nil {
		return nil, err
	}
	local := r.specs[tool].Local
	if local == nil {
		return nil, nil
	}
	defer func() {
		if p := recover(); p != nil {
			slog.Error("tool crashed while preparing", "tool", tool, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			prepared, err = nil, fmt.Errorf("the tool crashed while preparing: %v", p)
		}
	}()
	return local.Prepare(ctx, caller, arguments)
}

func (r *Registry) InvokePrepared(ctx context.Context, caller Caller, tool string, arguments map[string]any, prepared *Prepared) Result {
	if err := r.check(tool, arguments); err != nil {
		return Result{Err: err}.For(tool)
	}
	if local := r.specs[tool].Local; local != nil {
		return guard(tool, func() (Result, error) { return local.Run(ctx, caller, arguments, prepared) })
	}
	if prepared != nil {
		return Result{Err: fmt.Errorf("tool registry: tool %q no longer supports saved execution parameters", tool)}.For(tool)
	}
	return guard(tool, func() (Result, error) { return r.owner[tool].Invoke(ctx, caller, tool, arguments) })
}
