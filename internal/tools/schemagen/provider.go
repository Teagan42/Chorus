package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Provider is one declaration from schema/provider.cue. Capabilities are
// every field but the three that name it, so a capability added to the
// schema reaches the Go and the docs without this file learning its name.
type Provider struct {
	Name         string
	Role         string
	Description  string
	Capabilities map[string]json.RawMessage
}

func (p *Provider) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("provider: %w", err)
	}
	for key, into := range map[string]*string{"name": &p.Name, "role": &p.Role, "description": &p.Description} {
		if err := json.Unmarshal(raw[key], into); err != nil {
			return fmt.Errorf("provider %s: %w", key, err)
		}
		delete(raw, key)
	}
	p.Capabilities = raw
	return nil
}

// Need is one entry of needs, under its role and capability.
type Need struct {
	Level       string `json:"level"`
	Consequence string `json:"consequence"`
	Source      string `json:"source"`
}

// providerSchema is everything schema/provider.cue exports.
type providerSchema struct {
	Roles     []string
	Providers map[string]map[string]Provider
	Needs     map[string]map[string]Need
}

// capabilityKind is a capability's Go shape: a flag, or a list of languages.
type capabilityKind int

const (
	flagCapability capabilityKind = iota
	listCapability
)

// capabilities is every capability any provider declares, with its shape,
// sorted, cancels first: the one every role has.
func (s providerSchema) capabilities() ([]string, map[string]capabilityKind, error) {
	kinds := map[string]capabilityKind{}
	for _, byName := range s.Providers {
		for _, p := range byName {
			for name, v := range p.Capabilities {
				kind := flagCapability
				if strings.HasPrefix(strings.TrimSpace(string(v)), "[") {
					kind = listCapability
				}
				if seen, ok := kinds[name]; ok && seen != kind {
					return nil, nil, fmt.Errorf("capability %s is a flag on one provider and a list on another", name)
				}
				kinds[name] = kind
			}
		}
	}
	names := sortedKeys(kinds)
	slices.SortStableFunc(names, func(a, b string) int {
		switch {
		case a == "cancels":
			return -1
		case b == "cancels":
			return 1
		}
		return 0
	})
	for role, byCap := range s.Needs {
		for name := range byCap {
			if _, ok := kinds[name]; !ok {
				return nil, nil, fmt.Errorf("needs.%s.%s: no provider declares %s", role, name, name)
			}
		}
	}
	return names, kinds, nil
}

// roleCapabilities is the capabilities a role's providers declare, in
// capabilities' order.
func (s providerSchema) roleCapabilities(role string, all []string) []string {
	var out []string
	for _, name := range all {
		for _, p := range s.Providers[role] {
			if _, ok := p.Capabilities[name]; ok {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

func renderProvidersGo(s providerSchema) (string, error) {
	caps, kinds, err := s.capabilities()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("package capability\n\n")

	b.WriteString("// Role is the seam a provider serves (SPEC §10).\ntype Role string\n\n// The roles, as schema/provider.cue names them.\nconst (\n")
	for _, r := range s.Roles {
		b.WriteString(fmt.Sprintf("\tRole%s Role = %q\n", roleName(r), r))
	}
	b.WriteString(")\n\n")
	b.WriteString("// Roles is every role, in the order a turn meets them.\nvar Roles = []Role{")
	for i, r := range s.Roles {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("Role" + roleName(r))
	}
	b.WriteString("}\n\n")

	b.WriteString("// Capability names one thing a provider may be able to do.\ntype Capability string\n\nconst (\n")
	for _, c := range caps {
		b.WriteString(fmt.Sprintf("\t%s Capability = %q\n", goName(c), c))
	}
	b.WriteString(")\n\n")

	b.WriteString(`// Level is what lacking a capability costs.
type Level string

const (
	// Required refuses the pipeline at startup.
	Required Level = "required"
	// Degraded is said at startup, with what it costs the household.
	Degraded Level = "degraded"
)

// Capabilities is what one provider declares. A capability its role has no
// word for is zero.
type Capabilities struct {
`)
	for _, c := range caps {
		typ := "bool"
		if kinds[c] == listCapability {
			typ = "[]string"
		}
		b.WriteString(fmt.Sprintf("\t%s %s\n", goName(c), typ))
	}
	b.WriteString("}\n\n")

	b.WriteString("// Has reports whether a flag capability is declared. A list is not a\n// flag, so it is never had.\nfunc (c Capabilities) Has(name Capability) bool {\n\tswitch name {\n")
	for _, c := range caps {
		if kinds[c] == flagCapability {
			b.WriteString(fmt.Sprintf("\tcase %s:\n\t\treturn c.%s\n", goName(c), goName(c)))
		}
	}
	b.WriteString("\t}\n\treturn false\n}\n\n")

	b.WriteString(`// Declaration is one provider's, as schema/provider.cue declares it.
type Declaration struct {
	Name        string
	Role        Role
	Description string
	Capabilities
}

// Need is what the architecture asks of a role's provider (SPEC §10).
type Need struct {
	Role        Role
	Capability  Capability
	Level       Level
	Consequence string
	Source      string
}

`)
	b.WriteString("// Declared is every provider chorusd can run, by role and name.\nvar Declared = map[Role]map[string]Declaration{\n")
	for _, r := range s.Roles {
		byName := s.Providers[r]
		if len(byName) == 0 {
			continue
		}
		b.WriteString(fmt.Sprintf("\tRole%s: {\n", roleName(r)))
		for _, name := range sortedKeys(byName) {
			p := byName[name]
			b.WriteString(fmt.Sprintf("\t\t%q: {Name: %q, Role: Role%s, Description: %q, Capabilities: Capabilities{", p.Name, p.Name, roleName(r), p.Description))
			var fields []string
			for _, c := range caps {
				v, ok := p.Capabilities[c]
				if !ok {
					continue
				}
				lit, err := goLiteral(kinds[c], v)
				if err != nil {
					return "", fmt.Errorf("providers.%s.%s.%s: %w", r, name, c, err)
				}
				fields = append(fields, goName(c)+": "+lit)
			}
			b.WriteString(strings.Join(fields, ", ") + "}},\n")
		}
		b.WriteString("\t},\n")
	}
	b.WriteString("}\n\n")

	b.WriteString("// Needs is every need, by role in Roles' order and then by capability.\nvar Needs = []Need{\n")
	for _, r := range s.Roles {
		for _, c := range caps {
			n, ok := s.Needs[r][c]
			if !ok {
				continue
			}
			b.WriteString(fmt.Sprintf("\t{Role: Role%s, Capability: %s, Level: %s, Consequence: %q, Source: %q},\n",
				roleName(r), goName(c), goName(n.Level), n.Consequence, n.Source))
		}
	}
	b.WriteString("}\n")
	return b.String(), nil
}

func goLiteral(kind capabilityKind, v json.RawMessage) (string, error) {
	if kind == flagCapability {
		var on bool
		if err := json.Unmarshal(v, &on); err != nil {
			return "", err
		}
		return fmt.Sprintf("%t", on), nil
	}
	var list []string
	if err := json.Unmarshal(v, &list); err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "nil", nil
	}
	quoted := make([]string, len(list))
	for i, l := range list {
		quoted[i] = fmt.Sprintf("%q", l)
	}
	return "[]string{" + strings.Join(quoted, ", ") + "}", nil
}

func renderProviderDocs(s providerSchema) (string, error) {
	caps, kinds, err := s.capabilities()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("<!-- Generated by schemagen from schema/provider.cue. DO NOT EDIT. -->\n\n")
	b.WriteString("# Provider capability reference\n\nSee [SPEC §10](../SPEC.md) and [ADR-0102](../adr/0102-every-provider-declares-what-it-can-do-and-chorusd-refuses-a-pipeline-that-cannot-work.md). `chorusd` checks the providers it is configured with against these same declarations at startup: a required need a provider lacks stops it, and a degraded one is logged with what it costs.\n\n")

	b.WriteString("## What each role needs\n\n| Role | Capability | Lacking it | Source |\n|---|---|---|---|\n")
	for _, r := range s.Roles {
		for _, c := range caps {
			n, ok := s.Needs[r][c]
			if !ok {
				continue
			}
			level := "degraded"
			if n.Level == "required" {
				level = "**refused**"
			}
			b.WriteString(fmt.Sprintf("| %s | `%s` | %s: %s | %s |\n", r, c, level, n.Consequence, n.Source))
		}
	}
	b.WriteString("\n")

	for _, r := range s.Roles {
		byName := s.Providers[r]
		if len(byName) == 0 {
			continue
		}
		cols := s.roleCapabilities(r, caps)
		b.WriteString(fmt.Sprintf("## %s\n\n| Provider | `%s` |\n|---|", r, strings.Join(cols, "` | `")))
		b.WriteString(strings.Repeat("---|", len(cols)) + "\n")
		for _, name := range sortedKeys(byName) {
			p := byName[name]
			cells := make([]string, len(cols))
			for i, c := range cols {
				cells[i] = docCell(kinds[c], p.Capabilities[c])
			}
			b.WriteString(fmt.Sprintf("| `%s` | %s |\n", name, strings.Join(cells, " | ")))
		}
		b.WriteString("\n")
		for _, name := range sortedKeys(byName) {
			b.WriteString(fmt.Sprintf("- `%s`: %s\n", name, byName[name].Description))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// docCell is a capability as a table cell. An empty language list is the
// server's model's to say, which a blank would read as "none".
func docCell(kind capabilityKind, v json.RawMessage) string {
	if v == nil {
		return ""
	}
	if kind == flagCapability {
		var on bool
		_ = json.Unmarshal(v, &on)
		return yesNo(on)
	}
	var list []string
	_ = json.Unmarshal(v, &list)
	if len(list) == 0 {
		return "its model's"
	}
	return strings.Join(list, ", ")
}

// roleName is goName with the initialisms Go spells in capitals, so a
// role reads RoleSTT rather than RoleStt.
func roleName(r string) string {
	var b strings.Builder
	for part := range strings.SplitSeq(r, "_") {
		switch part {
		case "stt", "llm", "tts", "id":
			b.WriteString(strings.ToUpper(part))
		default:
			b.WriteString(goName(part))
		}
	}
	return b.String()
}
