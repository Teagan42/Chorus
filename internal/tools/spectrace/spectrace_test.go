package main

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleSpec = `# Title

## 1. Non-goals
Prose.

## 4. Session model
### 4.1 Action stream
### 4.4 Interrupted-turn semantics

## 15. Decisions that cannot be retrofitted
1. Interrupted-turn truth (§4.4) — the corpus depends on it.
2. Event log as source of truth (§8).

## 8. Event journal
`

func TestParseClausesFindsNestedNumbering(t *testing.T) {
	got := parseClauses(sampleSpec)
	want := []string{"1", "4", "4.1", "4.4", "15", "8"}
	if len(got) != len(want) {
		t.Fatalf("got %d clauses %v, want %d", len(got), got, len(want))
	}
	for i, id := range want {
		if got[i].id != id {
			t.Errorf("clause %d: got %q want %q", i, got[i].id, id)
		}
	}
}

func TestParseClausesIgnoresFencedBlocks(t *testing.T) {
	spec := "## 1. Real\n\n```\n## 2. Not a clause\n```\n\n## 3. Also real\n"
	got := parseClauses(spec)
	if len(got) != 2 {
		t.Fatalf("got %d clauses, want 2: %v", len(got), got)
	}
	if got[1].id != "3" {
		t.Errorf("second clause = %q, want 3", got[1].id)
	}
}

func TestParseCriticalMatchesOnIntentNotNumber(t *testing.T) {
	got := parseCritical(sampleSpec)
	if len(got) != 2 {
		t.Fatalf("got %v, want [4.4 8]", got)
	}
	if got[0] != "4.4" || got[1] != "8" {
		t.Errorf("got %v, want [4.4 8]", got)
	}
}

func TestScanCitationsMultipleClausesOneComment(t *testing.T) {
	dir := t.TempDir()
	src := "package p\n\n// verifies SPEC §4.4, §8\nfunc TestThing() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "a_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := scanCitations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d citations, want 2: %+v", len(got), got)
	}
	if got[0].clause != "4.4" || got[1].clause != "8" {
		t.Errorf("clauses = %q,%q want 4.4,8", got[0].clause, got[1].clause)
	}
	if got[0].line != 3 {
		t.Errorf("line = %d, want 3", got[0].line)
	}
}

func TestScanCitationsSkipsNonSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("verifies SPEC §4.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := scanCitations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("markdown should not be scanned, got %+v", got)
	}
}

func TestReportFailsStrictOnOrphanCitation(t *testing.T) {
	clauses := parseClauses(sampleSpec)
	cites := []cite{{clause: "99.9", file: "x_test.go", line: 1}}

	devnull, _ := os.Open(os.DevNull)
	defer devnull.Close()
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}

	if code := report(clauses, nil, cites, true, out); code != 1 {
		t.Errorf("strict orphan: exit = %d, want 1", code)
	}
	if code := report(clauses, nil, cites, false, out); code != 0 {
		t.Errorf("non-strict orphan: exit = %d, want 0", code)
	}
}

func TestReportFailsStrictOnUncoveredCriticalClause(t *testing.T) {
	clauses := parseClauses(sampleSpec)
	critical := parseCritical(sampleSpec)
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}

	// Nothing covers §4.4 or §8.
	if code := report(clauses, critical, nil, true, out); code != 1 {
		t.Errorf("uncovered critical: exit = %d, want 1", code)
	}

	// Cover both and it passes.
	cites := []cite{
		{clause: "4.4", file: "a_test.go", line: 1},
		{clause: "8", file: "b_test.go", line: 1},
	}
	if code := report(clauses, critical, cites, true, out); code != 0 {
		t.Errorf("covered critical: exit = %d, want 0", code)
	}
}

func TestUncoveredNonCriticalClausesDoNotFail(t *testing.T) {
	clauses := parseClauses(sampleSpec)
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	// §1 is prose, uncovered, and not critical.
	cites := []cite{
		{clause: "4.4", file: "a_test.go", line: 1},
		{clause: "8", file: "b_test.go", line: 1},
	}
	if code := report(clauses, parseCritical(sampleSpec), cites, true, out); code != 0 {
		t.Errorf("exit = %d, want 0 (prose clauses are allowed to be uncovered)", code)
	}
}

func TestScanCitationsIgnoresNonTestFilesAndStringLiterals(t *testing.T) {
	dir := t.TempDir()
	// A doc comment in non-test source is documentation, not a coverage claim.
	prose := "package p\n\n// verifies SPEC §4.4 is the convention\nfunc F() {}\n"
	// A citation inside a string literal is a fixture, not a claim.
	fixture := "package p\n\nvar s = \"verifies SPEC §4.4\"\n"
	if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte(prose), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fix_test.go"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := scanCitations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d citations, want 0: %+v", len(got), got)
	}
}
