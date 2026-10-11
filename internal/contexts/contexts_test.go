package contexts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
)

// fingerprint is the engine's label, written out again here so the archive
// is checked against the rule rather than against itself.
func fingerprint(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])[:12]
}

// Every archived prompt hashes to the label it is filed under: the persona
// alone, or the persona and the summary prompt for a build that summarized.
//
// verifies SPEC §8, §13
func TestEveryArchivedPromptHashesToItsLabel(t *testing.T) {
	ps, err := prompts(archiveFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) == 0 {
		t.Fatal("the archive holds no prompts")
	}
	for label, p := range ps {
		hashed := p.System
		if p.Summary != "" {
			hashed += "\x00" + p.Summary
		}
		if got := fingerprint([]byte(hashed)); got != label {
			t.Errorf("%s holds a prompt that hashes to %s", fileName(label), got)
		}
	}
}

// A tool schema hashes as the wire carried it, compact; the file is indented
// for a reviewer, which compacting undoes byte for byte.
//
// verifies SPEC §8, §13
func TestEveryArchivedToolSchemaHashesToItsLabel(t *testing.T) {
	a := MustArchive()
	if len(a.ToolSchemas) == 0 {
		t.Fatal("the archive holds no tool schemas")
	}
	for label, schema := range a.ToolSchemas {
		var b bytes.Buffer
		if err := json.Compact(&b, []byte(schema)); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if got := fingerprint(b.Bytes()); got != label {
			t.Errorf("%s holds a schema that hashes to %s", fileName(label), got)
		}
	}
}

// The build's own prompt and tools are archived, so every version a build
// can record has its text here. Changing either fails this test until the
// new version is filed: CHORUS_UPDATE_CONTEXTS=1 writes it.
//
// verifies SPEC §13
func TestTheBuildsOwnContextIsArchived(t *testing.T) {
	promptLabel := ollama.PromptVersion(ollama.DefaultPrompt)
	toolsLabel := ollama.ToolSchemaVersion(registry.Offered())
	schema, err := json.Marshal(json.RawMessage(ollama.ToolSchema(registry.Offered())))
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint(schema) != toolsLabel {
		t.Fatalf("the offered schema hashes to %s compacted, the engine records %s", fingerprint(schema), toolsLabel)
	}
	if os.Getenv("CHORUS_UPDATE_CONTEXTS") != "" {
		write(t, promptLabel, toolsLabel)
	}
	// From disk, not the embed: a version just written is not compiled in yet.
	a, err := archive(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := a.Prompt(promptLabel); !ok || got != ollama.DefaultPrompt {
		t.Errorf("the build's prompt %s is not archived; rerun with CHORUS_UPDATE_CONTEXTS=1 and commit archive/prompts/%s", promptLabel, fileName(promptLabel))
	}
	if _, ok := a.ToolSchema(toolsLabel); !ok {
		t.Errorf("the build's tool schema %s is not archived; rerun with CHORUS_UPDATE_CONTEXTS=1 and commit archive/tools/%s", toolsLabel, fileName(toolsLabel))
	}
}

// write files the build's prompt and tool schema under their labels. An
// archived version is never rewritten: its text is what the label hashed.
func write(t *testing.T, promptLabel, toolsLabel string) {
	t.Helper()
	var p bytes.Buffer
	enc := json.NewEncoder(&p)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(prompt{System: ollama.DefaultPrompt, Summary: ollama.SummaryPrompt}); err != nil {
		t.Fatal(err)
	}
	// Indent keeps the wire's escapes, so compacting gives back what was hashed.
	wire, err := json.Marshal(json.RawMessage(ollama.ToolSchema(registry.Offered())))
	if err != nil {
		t.Fatal(err)
	}
	var tools bytes.Buffer
	if err := json.Indent(&tools, wire, "", "  "); err != nil {
		t.Fatal(err)
	}
	tools.WriteString("\n")
	for path, b := range map[string][]byte{
		filepath.Join("archive", "prompts", fileName(promptLabel)): p.Bytes(),
		filepath.Join("archive", "tools", fileName(toolsLabel)):    tools.Bytes(),
	} {
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("archived %s", path)
	}
}

// A chain answers from the first source that knows a label, and an empty
// label is known to none: an unattributed turn has no context.
//
// verifies SPEC §13
func TestAChainAnswersFromTheFirstSourceThatKnows(t *testing.T) {
	household := Table{Prompts: map[string]string{"sys@3": "You are a voice assistant in a home."}}
	c := Chain{MustArchive(), household}
	if p, ok := c.Prompt("sys@3"); !ok || p != "You are a voice assistant in a home." {
		t.Errorf("Prompt(sys@3) = %q, %v", p, ok)
	}
	if _, ok := c.Prompt("sys@2"); ok {
		t.Error("a label nobody archived resolved")
	}
	if _, ok := c.ToolSchema(""); ok {
		t.Error("an empty label resolved")
	}
	if _, ok := (Table{Prompts: map[string]string{"": "x"}}).Prompt(""); ok {
		t.Error("an empty label resolved from a table that keys one")
	}
}

// File names round-trip to labels, and nothing else in a directory passes
// for one.
func TestFileNamesRoundTripToLabels(t *testing.T) {
	if l, ok := labelOf(fileName("sha256:46d59bfc62c0")); !ok || l != "sha256:46d59bfc62c0" {
		t.Errorf("round trip = %q, %v", l, ok)
	}
	for _, name := range []string{"README.md", "sha256-46d59bfc62c0.txt", "sys@3.json"} {
		if _, ok := labelOf(name); ok {
			t.Errorf("%s passed for a label", name)
		}
	}
}
