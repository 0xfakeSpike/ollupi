package tools

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

func ValidateArguments(args map[string]any, schema any) error {
	resolved, err := ResolveSchema(schema)
	if err != nil {
		return err
	}
	return validate(resolved, args)
}

// ResolveSchema makes a tool's input schema ready to check arguments against, or says why the registry cannot take a
// tool with it: a provider whose tools come from outside leaves such a tool out.
func ResolveSchema(schema any) (*jsonschema.Resolved, error) {
	if schema == nil {
		return nil, fmt.Errorf("missing input schema")
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal input schema: %w", err)
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse input schema: %w", err)
	}
	resolved, err := parsed.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve input schema: %w", err)
	}
	return resolved, nil
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

func normalizeInputSchema(schema any) (map[string]any, error) {
	if value, ok := schema.(map[string]any); ok {
		return value, nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

type Constrainer interface {
	Constrain(*jsonschema.Schema)
}

func InputSchema[T any]() *jsonschema.Schema {
	s, err := InputSchemaOf(reflect.TypeFor[T]())
	if err != nil {
		panic(fmt.Sprintf("tools: input schema of %s: %v", reflect.TypeFor[T](), err))
	}
	return s
}

func InputSchemaOf(t reflect.Type) (*jsonschema.Schema, error) {
	return inputSchema(t, map[reflect.Type]*jsonschema.Schema{})
}

func inputSchema(t reflect.Type, known map[reflect.Type]*jsonschema.Schema) (*jsonschema.Schema, error) {
	for _, u := range reached(t) {
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

func reached(t reflect.Type) []reflect.Type {
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
	raw, _ := json.Marshal(arguments) // decoded from JSON: it cannot fail
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode arguments: %w", err)
	}
	return out, nil
}
