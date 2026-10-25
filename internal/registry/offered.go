package registry

import "strings"

// Offered is every declared tool the model may be shown: all of Specs but the
// deferred, which keep their policy and docs while nothing runs them (SPEC
// §14). A fresh map, so a caller may narrow it further.
func Offered() map[string]ToolSpec {
	out := make(map[string]ToolSpec, len(Specs))
	for name, spec := range Specs {
		if !spec.Deferred {
			out[name] = spec
		}
	}
	return out
}

// ProvidedPrefix opens the name of every tool a household's MCP server
// provides: mcp_<server>_<tool> (ADR-0075). No declared tool takes it.
const ProvidedPrefix = "mcp_"

// WithProvided is offered with each of recorded's provided tools that
// offered does not name: what a re-run of a turn that ran under recorded is
// offered. The house's tools belong to the servers it had then, which no
// build can declare, so leaving them out would ask the model a question the
// turn was never asked (ADR-0101). A fresh map; offered is not changed.
func WithProvided(offered, recorded map[string]ToolSpec) map[string]ToolSpec {
	out := make(map[string]ToolSpec, len(offered))
	for name, spec := range offered {
		out[name] = spec
	}
	for name, spec := range recorded {
		if _, named := out[name]; !named && strings.HasPrefix(name, ProvidedPrefix) {
			out[name] = spec
		}
	}
	return out
}
