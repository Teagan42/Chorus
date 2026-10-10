package main

import (
	"strings"
	"testing"
)

func TestParseSubject(t *testing.T) {
	cases := []struct {
		in         string
		ok         bool
		typ, scope string
	}{
		{"feat(session): add supervisor", true, "feat", "session"},
		{"fix: handle short frame", true, "fix", ""},
		{"chore(gen): regenerate bindings", true, "chore", "gen"},
		{"feat(api)!: drop password auth", true, "feat", "api"},
		{"wip", false, "", ""},
		{"feat add thing", false, "", ""},
		{"nope(scope): unknown type", false, "", ""},
		{"feat(): empty scope", false, "", ""},
		{"feat:", false, "", ""},
		{"feat:no space", false, "", ""},
		{"feat(session: unbalanced", false, "", ""},
	}
	for _, c := range cases {
		got, ok := ParseSubject(c.in)
		if ok != c.ok {
			t.Errorf("%q: ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && (got.Type != c.typ || got.Scope != c.scope) {
			t.Errorf("%q: got %+v, want {%s %s}", c.in, got, c.typ, c.scope)
		}
	}
}

func TestIsGenerated(t *testing.T) {
	gen := []string{
		"internal/journal/event.gen.go",
		"internal/pb/api.pb.go",
		"schema/json/tool.json",
		"docs/reference/events.md",
		"hardware/chorus-sat/chorus-sat.net",
		"hardware/chorus-sat/bom.csv",
	}
	for _, p := range gen {
		if !IsGenerated(p) {
			t.Errorf("%s should be generated", p)
		}
	}
	hand := []string{
		"internal/session/supervisor.go", "docs/SPEC.md", "schema/tool.cue",
		"hardware/chorus-sat/pins.yaml", "hardware/chorus-sat/sheets/mics.yaml",
	}
	for _, p := range hand {
		if IsGenerated(p) {
			t.Errorf("%s should not be generated", p)
		}
	}
}

func TestCheckRejectsMixedGeneratedAndHandWritten(t *testing.T) {
	v := Check("feat(journal): add interrupt event", []string{
		"internal/journal/journal.go",
		"internal/journal/event.gen.go",
	}, nil)
	if len(v) != 1 {
		t.Fatalf("got %d violations, want 1: %v", len(v), v)
	}
	if !strings.Contains(v[0], "chore(gen)") {
		t.Errorf("violation should name chore(gen): %q", v[0])
	}
}

func TestCheckAllowsPureGenCommit(t *testing.T) {
	v := Check("chore(gen): regenerate from schema", []string{
		"internal/journal/event.gen.go",
		"schema/json/event.json",
	}, nil)
	if len(v) != 0 {
		t.Errorf("want no violations, got %v", v)
	}
}

func TestCheckRejectsGenCommitTouchingSource(t *testing.T) {
	v := Check("chore(gen): regenerate", []string{
		"internal/journal/event.gen.go",
		"internal/journal/journal.go",
	}, nil)
	if len(v) != 1 || !strings.Contains(v[0], "only touch generated") {
		t.Errorf("want generated-only violation, got %v", v)
	}
}

func TestCheckSeparatesDependencyChanges(t *testing.T) {
	v := Check("feat(esphome): dial satellites", []string{
		"internal/esphome/client.go",
		"go.mod",
		"go.sum",
	}, nil)
	if len(v) != 1 || !strings.Contains(v[0], "chore(deps)") {
		t.Errorf("want deps violation, got %v", v)
	}

	if v := Check("chore(deps): add flynn/noise", []string{"go.mod", "go.sum"}, nil); len(v) != 0 {
		t.Errorf("pure deps commit should pass, got %v", v)
	}
}

func TestCheckStyleMustBeWhitespaceOnly(t *testing.T) {
	paths := []string{"internal/session/supervisor.go"}

	if v := Check("style: gofumpt", paths, func() bool { return true }); len(v) != 0 {
		t.Errorf("whitespace-only style commit should pass, got %v", v)
	}
	v := Check("style: gofumpt", paths, func() bool { return false })
	if len(v) != 1 || !strings.Contains(v[0], "formatting-only") {
		t.Errorf("want formatting-only violation, got %v", v)
	}
}

func TestCheckRejectsNonConventionalSubject(t *testing.T) {
	v := Check("fixed the thing", []string{"a.go"}, nil)
	if len(v) != 1 || !strings.Contains(v[0], "Conventional Commits") {
		t.Errorf("want convention violation, got %v", v)
	}
}

func TestCheckIgnoresEmptyCommits(t *testing.T) {
	if v := Check("chore: tag release", nil, nil); len(v) != 0 {
		t.Errorf("empty commit should pass, got %v", v)
	}
}

func TestCheckReportsBothGenAndDepsViolations(t *testing.T) {
	v := Check("feat(x): everything at once", []string{
		"internal/x/x.go",
		"internal/pb/api.pb.go",
		"go.mod",
	}, nil)
	if len(v) != 2 {
		t.Fatalf("want 2 violations, got %d: %v", len(v), v)
	}
}

func TestListTruncates(t *testing.T) {
	many := []string{"a", "b", "c", "d", "e", "f", "g"}
	got := list(many)
	if !strings.Contains(got, "+2 more") {
		t.Errorf("want truncation marker, got %q", got)
	}
}
