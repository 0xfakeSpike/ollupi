package tools

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

func ValidateArguments(args map[string]any, schema any) error {
	if schema == nil {
		return fmt.Errorf("missing input schema")
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshal input schema: %w", err)
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("parse input schema: %w", err)
	}
	resolved, err := parsed.Resolve(nil)
	if err != nil {
		return fmt.Errorf("resolve input schema: %w", err)
	}
	if args == nil {
		args = map[string]any{}
	}
	if err := resolved.Validate(args); err != nil {
		return fmt.Errorf("validate arguments: %w", err)
	}
	return nil
}

func normalizeInputSchema(schema any) (map[string]any, error) {
	if schema == nil {
		return nil, fmt.Errorf("missing schema")
	}
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

// Constrainer is a type whose JSON Schema says more than its Go type can:
// Constrain adds the lengths, patterns, item counts and enums its values
// keep to, to the schema inferred for it. Tool input schemas and the
// gateway contract are both built with it, so the two cannot drift.
type Constrainer interface {
	Constrain(*jsonschema.Schema)
}

// InputSchema is the JSON Schema of a tool's arguments T, as models take
// it: inferred from T with descriptions from jsonschema tags, each named
// type in it that is a Constrainer constrained, every list an array that
// is never null, and no $refs.
func InputSchema[T any]() *jsonschema.Schema {
	s, err := InputSchemaOf(reflect.TypeFor[T]())
	if err != nil {
		panic(fmt.Sprintf("tools: input schema of %s: %v", reflect.TypeFor[T](), err))
	}
	return s
}

// InputSchemaOf is InputSchema for a type known at run time.
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

// reached are the types t's fields hold, through pointers, slices and maps.
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

// DecodeArguments reads validated tool arguments into T.
func DecodeArguments[T any](arguments map[string]any) (T, error) {
	var out T
	raw, err := json.Marshal(arguments)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode arguments: %w", err)
	}
	return out, nil
}
