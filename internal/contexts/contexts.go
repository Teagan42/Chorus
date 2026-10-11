// Package contexts keeps the text behind every prompt and tool-schema version
// a build has recorded. The journal stamps each event with a content hash of
// what the model was offered (SPEC §8), never the text itself, and a build can
// only name its own: the system prompt and tool declarations of a turn from
// last month live here, so an export can ship them beside the turn (SPEC §13,
// ADR-0071).
//
// The archive is content-addressed and append-only. Each file is named by the
// label it hashes to, and a test recomputes every hash, so an entry cannot
// name text it does not hold.
package contexts

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// Source resolves a version label to the text it names.
type Source interface {
	// Prompt is the system prompt the label names: the persona a turn's
	// system message starts with.
	Prompt(version string) (string, bool)
	// ToolSchema is the tool declarations the label names, as the JSON
	// array the model was offered.
	ToolSchema(version string) (string, bool)
}

// Table is a Source over two maps keyed by label.
type Table struct {
	Prompts     map[string]string
	ToolSchemas map[string]string
}

// Prompt is the table's system prompt for version.
func (t Table) Prompt(version string) (string, bool) {
	s, ok := t.Prompts[version]
	return s, ok && version != ""
}

// ToolSchema is the table's tool declarations for version.
func (t Table) ToolSchema(version string) (string, bool) {
	s, ok := t.ToolSchemas[version]
	return s, ok && version != ""
}

// Chain asks each source in turn; the first that knows a label answers.
type Chain []Source

// Prompt is the first source's system prompt for version.
func (c Chain) Prompt(version string) (string, bool) {
	for _, s := range c {
		if p, ok := s.Prompt(version); ok {
			return p, true
		}
	}
	return "", false
}

// ToolSchema is the first source's tool declarations for version.
func (c Chain) ToolSchema(version string) (string, bool) {
	for _, s := range c {
		if t, ok := s.ToolSchema(version); ok {
			return t, true
		}
	}
	return "", false
}

//go:embed archive
var archiveFS embed.FS

// prompt is one archived prompt version. Summary is set when the label
// hashed the summary prompt beside the persona, as builds have since the
// engine began summarizing conversations.
type prompt struct {
	System  string `json:"system"`
	Summary string `json:"summary,omitempty"`
}

// MustArchive is every version a build on main has recorded. It panics only
// on corrupt embedded data, which the package's tests read whole.
func MustArchive() Table {
	t, err := archive(archiveFS)
	if err != nil {
		panic(err)
	}
	return t
}

// archive reads the prompts and tools directories into a Table.
func archive(fsys fs.FS) (Table, error) {
	prompts, err := prompts(fsys)
	if err != nil {
		return Table{}, err
	}
	t := Table{Prompts: map[string]string{}, ToolSchemas: map[string]string{}}
	for label, p := range prompts {
		t.Prompts[label] = p.System
	}
	err = each(fsys, "archive/tools", func(label string, b []byte) error {
		if !json.Valid(b) {
			return fmt.Errorf("tools %s: not JSON", label)
		}
		t.ToolSchemas[label] = strings.TrimSpace(string(b))
		return nil
	})
	return t, err
}

// prompts reads every archived prompt version, keyed by label.
func prompts(fsys fs.FS) (map[string]prompt, error) {
	out := map[string]prompt{}
	err := each(fsys, "archive/prompts", func(label string, b []byte) error {
		var p prompt
		if err := json.Unmarshal(b, &p); err != nil {
			return fmt.Errorf("prompt %s: %w", label, err)
		}
		out[label] = p
		return nil
	})
	return out, err
}

func each(fsys fs.FS, dir string, f func(label string, b []byte) error) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("contexts: %s: %w", dir, err)
	}
	for _, e := range entries {
		label, ok := labelOf(e.Name())
		if !ok {
			return fmt.Errorf("contexts: %s/%s is not named by a label", dir, e.Name())
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return fmt.Errorf("contexts: %w", err)
		}
		if err := f(label, b); err != nil {
			return fmt.Errorf("contexts: %w", err)
		}
	}
	return nil
}

// fileName is where label's text is archived. A colon is not a file name
// every filesystem takes, so the scheme's separator becomes a dash.
func fileName(label string) string {
	return strings.Replace(label, ":", "-", 1) + ".json"
}

// labelOf is the version a file name archives, or false for any other file.
func labelOf(name string) (string, bool) {
	stem, ok := strings.CutSuffix(name, ".json")
	if !ok || !strings.HasPrefix(stem, "sha256-") {
		return "", false
	}
	return strings.Replace(stem, "-", ":", 1), true
}
