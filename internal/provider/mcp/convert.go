package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/teagan42/chorus/internal/registry"
)

// maxName is the longest function name the model's wire accepts, as
// OpenAI-style tool calling bounds it.
const maxName = 64

// maxDescription bounds what one tool, or one of its parameters, puts in
// every prompt: a server's prose is not the household's to pay for.
const maxDescription = 1024

// Name is the name the model calls a server's tool by: mcp_<server>_<tool>,
// lowercased, with anything the registry's names cannot hold turned to _.
// The prefix keeps it clear of every native and Home Assistant tool.
func Name(server, tool string) (string, error) {
	var b strings.Builder
	for _, r := range strings.ToLower(tool) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := registry.ProvidedPrefix + server + "_" + b.String()
	if b.Len() == 0 {
		return "", errors.New("the server gave the tool no name")
	}
	if len(name) > maxName {
		return "", fmt.Errorf("%s is longer than the %d characters a tool name may be", name, maxName)
	}
	return name, nil
}

// schema is the part of a JSON Schema the model's wire can carry.
type schema struct {
	Type        json.RawMessage            `json:"type"`
	Title       string                     `json:"title"`
	Description string                     `json:"description"`
	Properties  map[string]json.RawMessage `json:"properties"`
	Required    []string                   `json:"required"`
	Enum        []any                      `json:"enum"`
	Items       json.RawMessage            `json:"items"`
	AnyOf       []json.RawMessage          `json:"anyOf"`
	OneOf       []json.RawMessage          `json:"oneOf"`
}

// Params maps a tool's input schema onto the registry's flat parameters,
// required first in the schema's order, then the rest by name, as the wire
// writes them back. The wire carries a type, a description, string enums
// and an array's item type; a parameter whose schema says more keeps the
// rest as JSON in its description, so the model is not told less than the
// server asks for. The server checks the arguments it is sent.
func Params(input any) ([]registry.ParamSpec, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("input schema: %w", err)
	}
	var s schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("input schema: %w", err)
	}
	if t, _ := typeOf(s); len(s.Type) > 0 && t != "object" {
		return nil, fmt.Errorf("input schema has type %s, want object", s.Type)
	}
	var required []string
	for _, name := range s.Required {
		if _, ok := s.Properties[name]; ok && !slices.Contains(required, name) {
			required = append(required, name)
		}
	}
	var rest []string
	for name := range s.Properties {
		if !slices.Contains(required, name) {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	out := make([]registry.ParamSpec, 0, len(s.Properties))
	for _, name := range append(required, rest...) {
		p, err := param(name, s.Properties[name])
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		p.Required = slices.Contains(required, name)
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func param(name string, raw json.RawMessage) (registry.ParamSpec, error) {
	var s schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return registry.ParamSpec{}, err
	}
	typ, exact := typeOf(s)
	p := registry.ParamSpec{Name: name, Type: typ}
	if enum, ok := allStrings(s.Enum); ok && len(enum) > 0 {
		p.Enum = enum
	}
	if typ == "array" {
		var items schema
		if len(s.Items) > 0 {
			if err := json.Unmarshal(s.Items, &items); err != nil {
				return registry.ParamSpec{}, fmt.Errorf("items: %w", err)
			}
		}
		p.Items, _ = typeOf(items)
	}
	p.Description = s.Description
	if p.Description == "" {
		p.Description = s.Title
	}
	if p.Description == "" {
		p.Description = "The " + name + "."
	}
	if extra := beyond(raw, exact); extra != "" {
		p.Description = strings.TrimRight(p.Description, " ") + " Schema: " + extra
	}
	p.Description = clip(p.Description)
	return p, nil
}

// typeOf is the one wire type a schema is offered as. exact is false when
// the schema did not say it in a single type, so the wire says less.
func typeOf(s schema) (string, bool) {
	if t, exact, ok := declared(s); ok {
		return t, exact
	}
	for _, alt := range append(s.AnyOf, s.OneOf...) {
		var a schema
		if json.Unmarshal(alt, &a) != nil {
			continue
		}
		if t, _, ok := declared(a); ok {
			return t, false
		}
	}
	switch {
	case len(s.Enum) > 0:
		if _, ok := allStrings(s.Enum); ok {
			return "string", false
		}
	case len(s.Properties) > 0:
		return "object", false
	case len(s.Items) > 0:
		return "array", false
	}
	return "string", false
}

// declared is the type a schema names itself. ["string","null"] is a
// string that may be null, said exactly; any other list is its first.
func declared(s schema) (typ string, exact, ok bool) {
	var one string
	if json.Unmarshal(s.Type, &one) == nil && slices.Contains(jsonTypes, one) {
		return one, true, true
	}
	var many []string
	if json.Unmarshal(s.Type, &many) != nil {
		return "", false, false
	}
	for _, t := range many {
		if slices.Contains(jsonTypes, t) {
			return t, len(many) == 2 && slices.Contains(many, "null"), true
		}
	}
	return "", false, false
}

// beyond is what a parameter's schema says that its wire parameter does
// not, as compact JSON. Empty when the wire carries all of it.
func beyond(raw json.RawMessage, exact bool) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	delete(m, "description")
	delete(m, "title")
	plain := exact
	for k, v := range m {
		switch k {
		case "type":
		case "enum":
			if vs, ok := v.([]any); !ok {
				plain = false
			} else if _, ok := allStrings(vs); !ok {
				plain = false
			}
		case "items":
			items, ok := v.(map[string]any)
			_, typed := items["type"].(string)
			if !ok || !typed || len(items) != 1 {
				plain = false
			}
		default:
			plain = false
		}
	}
	if plain {
		return ""
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	// A map of decoded JSON cannot fail to encode.
	_ = enc.Encode(m)
	return strings.TrimSpace(b.String())
}

// jsonTypes are the types the wire carries (registry.ParamSpec.Type).
var jsonTypes = []string{"string", "number", "integer", "boolean", "array", "object"}

// allStrings is vs as strings, when every one is a string.
func allStrings(vs []any) ([]string, bool) {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// clip bounds a description at maxDescription bytes, on a rune boundary.
func clip(s string) string {
	if len(s) <= maxDescription {
		return s
	}
	cut := maxDescription - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
