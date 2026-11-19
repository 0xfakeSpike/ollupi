package kit

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/google/jsonschema-go/jsonschema"
)

func ResolveSchema(schema any) (*jsonschema.Resolved, error) {
	resolved, _, err := schemas(schema)
	return resolved, err
}

func schemas(schema any) (*jsonschema.Resolved, map[string]any, error) {
	if schema == nil {
		return nil, nil, fmt.Errorf("missing input schema")
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal input schema: %w", err)
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, nil, fmt.Errorf("parse input schema: %w", err)
	}
	resolved, err := parsed.Resolve(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve input schema: %w", err)
	}
	var listed map[string]any
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, nil, fmt.Errorf("parse input schema: %w", err)
	}
	return resolved, listed, nil
}

const backgroundDescription = "Run this call in the background: the reply comes back at once with a task id, and the result arrives later as a report. " +
	"Use it for long work when there is something else to do meanwhile."

func withBackground(schema any) any {
	raw, _ := json.Marshal(schema)
	var out map[string]any
	json.Unmarshal(raw, &out)
	properties, _ := out["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	properties[tool.BackgroundArgument] = map[string]any{"type": "boolean", "description": backgroundDescription}
	out["properties"] = properties
	return out
}

func validate(schema *jsonschema.Resolved, args map[string]any) error {
	if args == nil {
		args = map[string]any{}
	}
	if err := schema.Validate(args); err != nil {
		return fmt.Errorf("validate arguments: %w", err)
	}
	return nil
}

type Constrainer interface {
	Constrain(*jsonschema.Schema)
}

func InputSchema[T any]() *jsonschema.Schema {
	s, err := inputSchema(reflect.TypeFor[T](), map[reflect.Type]*jsonschema.Schema{})
	if err != nil {
		panic(fmt.Sprintf("tools: input schema of %s: %v", reflect.TypeFor[T](), err))
	}
	return s
}

func inputSchema(t reflect.Type, known map[reflect.Type]*jsonschema.Schema) (*jsonschema.Schema, error) {
	for _, u := range fieldTypes(t) {
		if _, done := known[u]; done || !u.Implements(constrainer) {
			continue
		}
		s, err := inputSchema(u, known)
		if err != nil {
			return nil, err
		}
		known[u] = s
	}
	s, err := jsonschema.ForType(t, &jsonschema.ForOptions{TypeSchemas: known})
	if err != nil {
		return nil, err
	}
	nonNullLists(s)
	if c, ok := reflect.Zero(t).Interface().(Constrainer); ok {
		c.Constrain(s)
	}
	return s, nil
}

var constrainer = reflect.TypeFor[Constrainer]()

func fieldTypes(t reflect.Type) []reflect.Type {
	var out []reflect.Type
	var add func(reflect.Type)
	add = func(u reflect.Type) {
		switch u.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map:
			add(u.Elem())
		default:
			out = append(out, u)
		}
	}
	if t.Kind() == reflect.Struct {
		for _, f := range reflect.VisibleFields(t) {
			add(f.Type)
		}
	} else {
		add(t)
		out = slices.DeleteFunc(out, func(u reflect.Type) bool { return u == t })
	}
	return out
}

func nonNullLists(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if slices.Equal(s.Types, []string{"null", "array"}) {
		s.Types, s.Type = nil, "array"
	}
	for _, p := range s.Properties {
		nonNullLists(p)
	}
	nonNullLists(s.Items)
	nonNullLists(s.AdditionalProperties)
}

func DecodeArguments[T any](arguments map[string]any) (T, error) {
	var out T
	raw, _ := json.Marshal(arguments)
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode arguments: %w", err)
	}
	return out, nil
}
