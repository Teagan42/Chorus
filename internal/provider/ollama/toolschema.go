package ollama

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/teagan42/chorus/internal/registry"
)

// ToolSchema is the declarations the model is offered, indented for a
// reviewer to edit. ParseToolSchema reads it back (SPEC §9.2).
func ToolSchema(specs map[string]registry.ToolSpec) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // a <name> placeholder reads as written
	enc.SetIndent("", "  ")
	_ = enc.Encode(wireTools(specs)) // strings and slices of them cannot fail to encode
	return strings.TrimSuffix(b.String(), "\n")
}

// paramTypes are the JSON Schema types a parameter may have on the wire.
var paramTypes = []string{"string", "integer", "number", "boolean", "array", "object"}

// ParseToolSchema reads an edited ToolSchema into the specs New takes. It
// refuses what the wire's shape cannot carry rather than drop it, so the
// version names what the model was really offered.
func ParseToolSchema(schema string) (map[string]registry.ToolSpec, error) {
	dec := json.NewDecoder(strings.NewReader(schema))
	dec.DisallowUnknownFields()
	var tools []wireTool
	if err := dec.Decode(&tools); err != nil {
		return nil, schemaError(schema, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("tool schema: there is more after the closing ]")
	}
	specs := make(map[string]registry.ToolSpec, len(tools))
	for i, t := range tools {
		f := t.Function
		switch {
		case f.Name == "":
			return nil, fmt.Errorf("tool schema: tool %d has no name", i+1)
		case t.Type != "function":
			return nil, fmt.Errorf("tool schema: %s has type %q, want function", f.Name, t.Type)
		case f.Parameters.Type != "object":
			return nil, fmt.Errorf("tool schema: %s's parameters have type %q, want object", f.Name, f.Parameters.Type)
		}
		if _, ok := specs[f.Name]; ok {
			return nil, fmt.Errorf("tool schema: %s is declared twice", f.Name)
		}
		params, err := modelParams(f.Name, f.Parameters)
		if err != nil {
			return nil, fmt.Errorf("tool schema: %w", err)
		}
		specs[f.Name] = registry.ToolSpec{Name: f.Name, Description: f.Description, ModelDescription: f.Description, ModelParams: params}
	}
	return specs, nil
}

// modelParams puts the required parameters first, in the order given, then
// the rest by name. wireTools writes required back in that order, so an
// unedited schema keeps its version.
func modelParams(tool string, s wireSchema) ([]registry.ParamSpec, error) {
	var out []registry.ParamSpec
	for i, name := range s.Required {
		p, ok := s.Properties[name]
		if !ok {
			return nil, fmt.Errorf("%s requires %q, which it does not declare", tool, name)
		}
		if slices.Contains(s.Required[:i], name) {
			return nil, fmt.Errorf("%s requires %q twice", tool, name)
		}
		ps, err := param(tool, name, p)
		if err != nil {
			return nil, err
		}
		ps.Required = true
		out = append(out, ps)
	}
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		if !slices.Contains(s.Required, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		ps, err := param(tool, name, s.Properties[name])
		if err != nil {
			return nil, err
		}
		out = append(out, ps)
	}
	return out, nil
}

func param(tool, name string, p wireProp) (registry.ParamSpec, error) {
	if !slices.Contains(paramTypes, p.Type) {
		return registry.ParamSpec{}, fmt.Errorf("%s.%s has type %q, want one of %s", tool, name, p.Type, strings.Join(paramTypes, ", "))
	}
	ps := registry.ParamSpec{Name: name, Type: p.Type, Description: p.Description, Enum: p.Enum}
	if it := p.Items; it != nil {
		switch {
		case p.Type != "array":
			return ps, fmt.Errorf("%s.%s has items but is not an array", tool, name)
		case !slices.Contains(paramTypes, it.Type) || it.Description != "" || it.Enum != nil || it.Items != nil:
			return ps, fmt.Errorf("%s.%s's items carry only a type", tool, name)
		}
		ps.Items = it.Type
	}
	return ps, nil
}

// schemaError says where a schema stopped reading, by line: what a reviewer
// looking at a textarea can find.
func schemaError(schema string, err error) error {
	line := func(offset int64) int { return 1 + strings.Count(schema[:min(int(offset), len(schema))], "\n") }
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("tool schema: it ends before it is closed")
	case errors.As(err, &syntax):
		return fmt.Errorf("tool schema line %d: %s", line(syntax.Offset), strings.TrimPrefix(syntax.Error(), "json: "))
	case errors.As(err, &typ) && typ.Field == "":
		return errors.New("tool schema: it must be a JSON array of tools")
	case errors.As(err, &typ):
		return fmt.Errorf("tool schema line %d: %s cannot be a JSON %s", line(typ.Offset), typ.Field, typ.Value)
	}
	return fmt.Errorf("tool schema: %s", strings.TrimPrefix(err.Error(), "json: "))
}
