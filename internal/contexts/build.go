package contexts

import (
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
)

// Build is the context this build offers its model: the default prompt and
// the registry's offered tools, under the labels an engine records for them.
// A dev build's edited prompt resolves from here before it is archived.
func Build() Table {
	offered := registry.Offered()
	return Table{
		Prompts:     map[string]string{ollama.PromptVersion(ollama.DefaultPrompt): ollama.DefaultPrompt},
		ToolSchemas: map[string]string{ollama.ToolSchemaVersion(offered): ollama.ToolSchema(offered)},
	}
}

// Default resolves from the build, then the archive: every label a build of
// this repository can have recorded.
func Default() Source { return Chain{Build(), MustArchive()} }
