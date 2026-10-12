package ollama

import (
	"github.com/teagan42/chorus/internal/provider/chat"
	"github.com/teagan42/chorus/internal/registry"
)

// ToolSchema is the declarations the model is offered, indented for a
// reviewer to edit. ParseToolSchema reads it back (SPEC §9.2).
func ToolSchema(specs map[string]registry.ToolSpec) string { return chat.ToolSchema(specs) }

// ParseToolSchema reads an edited ToolSchema into the specs New takes. It
// refuses what the wire's shape cannot carry rather than drop it, so the
// version names what the model was really offered.
func ParseToolSchema(schema string) (map[string]registry.ToolSpec, error) {
	return chat.ParseToolSchema(schema)
}
