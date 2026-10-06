// Command spectrace maps SPEC.md clauses to the tests that verify them.
//
// Tests declare coverage with a comment: `// verifies SPEC §4.4`. Two failure
// modes matter: documented behavior nothing tests, and tests citing a clause
// that no longer exists (the spec moved and the test's intent is now unclear).
package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// clauseHeading matches numbered markdown headings: "## 4. Title", "### 4.4 Title".
var clauseHeading = regexp.MustCompile(`^#{2,6}\s+(\d+(?:\.\d+)*)\.?\s+(.+)$`)

// citation matches "verifies SPEC §4.4" and captures the trailing clause list
// so one comment can cite several clauses.
var citation = regexp.MustCompile(`verifies\s+SPEC\s+((?:§\s*\d+(?:\.\d+)*\s*,?\s*)+)`)

var clauseRef = regexp.MustCompile(`§\s*(\d+(?:\.\d+)*)`)

type clause struct {
	id    string
	title string
}

type cite struct {
	clause string
	file   string
	line   int
}

func main() {
	strict := flag.Bool("strict", false, "exit non-zero on orphan citations or uncovered critical clauses")
	root := flag.String("root", ".", "directory to scan for citations")
	flag.Parse()

	specPath := flag.Arg(0)
	if specPath == "" {
		specPath = "docs/SPEC.md"
	}

	code, err := run(specPath, *root, *strict, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spectrace: %v\n", err)
		os.Exit(2)
	}
	os.Exit(code)
}

func run(specPath, root string, strict bool, out *os.File) (int, error) {
	specBytes, err := os.ReadFile(specPath)
	if err != nil {
		return 0, err
	}
	spec := string(specBytes)

	clauses := parseClauses(spec)
	if len(clauses) == 0 {
		return 0, fmt.Errorf("no numbered clauses found in %s", specPath)
	}
	critical := parseCritical(spec)

	cites, err := scanCitations(root)
	if err != nil {
		return 0, err
	}

	return report(clauses, critical, cites, strict, out), nil
}

// parseClauses extracts clause ids from numbered headings.
func parseClauses(spec string) []clause {
	var out []clause
	inFence := false
	for line := range strings.SplitSeq(spec, "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := clauseHeading.FindStringSubmatch(line); m != nil {
			out = append(out, clause{id: m[1], title: strings.TrimSpace(m[2])})
		}
	}
	return out
}

// parseCritical returns clauses referenced from the "cannot be retrofitted"
// section. Those are the only ones whose coverage is mandatory.
func parseCritical(spec string) []string {
	var out []string
	var inSection bool
	for line := range strings.SplitSeq(spec, "\n") {
		if m := clauseHeading.FindStringSubmatch(line); m != nil {
			// Section titles change; match on intent, not number.
			inSection = strings.Contains(strings.ToLower(m[2]), "cannot be retrofitted")
			continue
		}
		if !inSection {
			continue
		}
		for _, ref := range clauseRef.FindAllStringSubmatch(line, -1) {
			if !slices.Contains(out, ref[1]) {
				out = append(out, ref[1])
			}
		}
	}
	return out
}

// isTestFile reports whether a path is a test. Coverage claims live in tests by
// definition, so non-test files are ignored — which also keeps this tool's own
// documentation and fixtures out of its report.
func isTestFile(path string) bool {
	base := filepath.Base(path)
	switch filepath.Ext(path) {
	case ".go":
		return strings.HasSuffix(base, "_test.go")
	case ".py":
		return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")
	}
	return false
}

// scanCitations walks test sources for coverage comments. Go files are parsed so
// only real comments count; a citation inside a string literal is not a claim.
func scanCitations(root string) ([]cite, error) {
	var out []cite
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".venv", ".task":
				return fs.SkipDir
			}
			return nil
		}
		if !isTestFile(path) {
			return nil
		}
		found, err := citationsIn(path)
		if err != nil {
			return err
		}
		out = append(out, found...)
		return nil
	})
	return out, err
}

func citationsIn(path string) ([]cite, error) {
	if filepath.Ext(path) == ".go" {
		return goCitations(path)
	}
	return lineCommentCitations(path)
}

// goCitations extracts citations from Go comments only.
func goCitations(path string) ([]cite, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []cite
	for _, group := range f.Comments {
		for _, c := range group.List {
			for _, ref := range refsInComment(c.Text) {
				out = append(out, cite{clause: ref, file: path, line: fset.Position(c.Pos()).Line})
			}
		}
	}
	return out, nil
}

// lineCommentCitations handles languages we do not parse: the citation must be
// in a `#` comment.
func lineCommentCitations(path string) ([]cite, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []cite
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "#") {
			continue
		}
		for _, ref := range refsInComment(line) {
			out = append(out, cite{clause: ref, file: path, line: n})
		}
	}
	return out, sc.Err()
}

func refsInComment(text string) []string {
	m := citation.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	var out []string
	for _, ref := range clauseRef.FindAllStringSubmatch(m[1], -1) {
		out = append(out, ref[1])
	}
	return out
}

func report(clauses []clause, critical []string, cites []cite, strict bool, out *os.File) int {
	known := map[string]clause{}
	for _, c := range clauses {
		known[c.id] = c
	}

	covered := map[string][]cite{}
	var orphans []cite
	for _, c := range cites {
		if _, ok := known[c.clause]; !ok {
			orphans = append(orphans, c)
			continue
		}
		covered[c.clause] = append(covered[c.clause], c)
	}

	var uncovered []clause
	for _, c := range clauses {
		if len(covered[c.id]) == 0 {
			uncovered = append(uncovered, c)
		}
	}

	fmt.Fprintf(out, "spec clauses:    %d\n", len(clauses))
	fmt.Fprintf(out, "covered:         %d\n", len(clauses)-len(uncovered))
	fmt.Fprintf(out, "citations:       %d\n", len(cites))
	fmt.Fprintf(out, "critical clauses: %v\n\n", critical)

	if len(covered) > 0 {
		fmt.Fprintln(out, "=== covered ===")
		ids := make([]string, 0, len(covered))
		for id := range covered {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(out, "  §%-8s %-44s %d test(s)\n", id, truncate(known[id].title, 44), len(covered[id]))
		}
		fmt.Fprintln(out)
	}

	if len(uncovered) > 0 {
		fmt.Fprintln(out, "=== uncovered (informational) ===")
		for _, c := range uncovered {
			mark := " "
			if slices.Contains(critical, c.id) {
				mark = "!"
			}
			fmt.Fprintf(out, " %s §%-8s %s\n", mark, c.id, truncate(c.title, 56))
		}
		fmt.Fprintln(out)
	}

	exit := 0

	if len(orphans) > 0 {
		fmt.Fprintln(out, "=== ORPHAN CITATIONS (spec clause does not exist) ===")
		for _, o := range orphans {
			fmt.Fprintf(out, "  %s:%d cites §%s\n", o.file, o.line, o.clause)
		}
		fmt.Fprintln(out)
		if strict {
			exit = 1
		}
	}

	var missingCritical []string
	for _, id := range critical {
		if len(covered[id]) == 0 {
			missingCritical = append(missingCritical, id)
		}
	}
	if len(missingCritical) > 0 {
		fmt.Fprintf(out, "=== UNCOVERED CRITICAL CLAUSES ===\n")
		for _, id := range missingCritical {
			fmt.Fprintf(out, "  §%-8s %s\n", id, known[id].title)
		}
		fmt.Fprintln(out, "\nThese are listed as unretrofittable; coverage is mandatory.")
		if strict {
			exit = 1
		}
	}

	if exit == 0 {
		fmt.Fprintln(out, "OK")
	}
	return exit
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
