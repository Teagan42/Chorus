// Command atomic enforces commit atomicity rules from CONTRIBUTING.md §4.
//
// Convention alone does not prevent the failure that hurts: a large diff where
// most lines are regenerated and a few change behavior. These rules are
// mechanical so that case cannot pass review unnoticed.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	rangeSpec := "HEAD~1..HEAD"
	if len(os.Args) > 1 && os.Args[1] != "" {
		rangeSpec = os.Args[1]
	}

	commits, err := commitsIn(rangeSpec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "atomic: %v\n", err)
		os.Exit(2)
	}
	if len(commits) == 0 {
		fmt.Println("atomic: no commits in range", rangeSpec)
		return
	}

	failed := false
	for _, sha := range commits {
		subject, err := gitOut("log", "-1", "--format=%s", sha)
		if err != nil {
			fmt.Fprintf(os.Stderr, "atomic: %v\n", err)
			os.Exit(2)
		}
		paths, err := changedPaths(sha)
		if err != nil {
			fmt.Fprintf(os.Stderr, "atomic: %v\n", err)
			os.Exit(2)
		}
		whitespaceOnly := func() bool { return isWhitespaceOnly(sha) }

		violations := Check(subject, paths, whitespaceOnly)
		short := sha
		if len(short) > 8 {
			short = short[:8]
		}
		if len(violations) == 0 {
			fmt.Printf("  ok   %s %s\n", short, subject)
			continue
		}
		failed = true
		fmt.Printf("  FAIL %s %s\n", short, subject)
		for _, v := range violations {
			fmt.Printf("         %s\n", v)
		}
	}
	if failed {
		fmt.Fprintln(os.Stderr, "\nSee CONTRIBUTING.md §4. Split the commit.")
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- pure rules

var conventionalTypes = []string{
	"feat", "fix", "refactor", "perf", "test", "docs", "style", "chore", "build", "ci", "revert",
}

// Commit describes a parsed conventional-commit subject.
type Commit struct {
	Type  string
	Scope string
}

// ParseSubject splits "type(scope): subject". Returns ok=false when the subject
// does not follow the convention.
func ParseSubject(subject string) (Commit, bool) {
	colon := strings.Index(subject, ":")
	if colon < 0 || colon+1 >= len(subject) || subject[colon+1] != ' ' {
		return Commit{}, false
	}
	// Breaking-change marker sits after the scope: "feat(api)!: ...".
	head := strings.TrimSuffix(subject[:colon], "!")
	if strings.TrimSpace(subject[colon+2:]) == "" {
		return Commit{}, false
	}

	typ, scope := head, ""
	if open := strings.Index(head, "("); open >= 0 {
		if !strings.HasSuffix(head, ")") {
			return Commit{}, false
		}
		typ = head[:open]
		scope = head[open+1 : len(head)-1]
		if scope == "" {
			return Commit{}, false
		}
	}
	typ = strings.TrimSuffix(typ, "!") // breaking-change marker

	for _, t := range conventionalTypes {
		if typ == t {
			return Commit{Type: typ, Scope: scope}, true
		}
	}
	return Commit{}, false
}

// IsGenerated reports whether a path is produced by `task gen`.
func IsGenerated(path string) bool {
	switch {
	case strings.HasSuffix(path, ".gen.go"),
		strings.HasPrefix(path, "internal/pb/"),
		strings.HasPrefix(path, "schema/json/"),
		strings.HasPrefix(path, "docs/reference/"):
		return true
	}
	return false
}

// IsDependency reports whether a path is a manifest, lockfile, or vendored code.
func IsDependency(path string) bool {
	switch path {
	case "go.mod", "go.sum", "uv.lock", "pyproject.toml", "buf.lock", "package-lock.json":
		return true
	}
	return strings.HasPrefix(path, "vendor/") || strings.HasPrefix(path, "proto/esphome/")
}

// Check applies the atomicity rules. whitespaceOnly is called lazily because it
// costs a git invocation and only `style` commits need it.
func Check(subject string, paths []string, whitespaceOnly func() bool) []string {
	var out []string

	c, ok := ParseSubject(subject)
	if !ok {
		return []string{fmt.Sprintf("subject %q is not Conventional Commits (type(scope): subject)", subject)}
	}
	if len(paths) == 0 {
		return nil
	}

	var gen, dep, hand []string
	for _, p := range paths {
		switch {
		case IsGenerated(p):
			gen = append(gen, p)
		case IsDependency(p):
			dep = append(dep, p)
		default:
			hand = append(hand, p)
		}
	}

	isGenCommit := c.Type == "chore" && c.Scope == "gen"
	isDepCommit := c.Type == "chore" && (c.Scope == "deps" || c.Scope == "vendor")

	switch {
	case isGenCommit:
		if len(hand) > 0 || len(dep) > 0 {
			out = append(out, fmt.Sprintf("chore(gen) may only touch generated paths; also changed: %s", list(append(hand, dep...))))
		}
	case isDepCommit:
		if len(hand) > 0 || len(gen) > 0 {
			out = append(out, fmt.Sprintf("chore(deps) may only touch manifests/lockfiles/vendor; also changed: %s", list(append(hand, gen...))))
		}
	case c.Type == "style":
		if whitespaceOnly != nil && !whitespaceOnly() {
			out = append(out, "style commits must be formatting-only (diff is non-empty ignoring whitespace)")
		}
	default:
		if len(gen) > 0 {
			out = append(out, fmt.Sprintf("generated files belong in a separate chore(gen) commit: %s", list(gen)))
		}
		if len(dep) > 0 {
			out = append(out, fmt.Sprintf("dependency changes belong in a separate chore(deps) commit: %s", list(dep)))
		}
	}
	return out
}

func list(paths []string) string {
	const max = 5
	if len(paths) <= max {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(paths[:max], ", "), len(paths)-max)
}

// ------------------------------------------------------------------- git glue

func gitOut(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(b)), nil
}

func commitsIn(rangeSpec string) ([]string, error) {
	if !strings.Contains(rangeSpec, "..") {
		return []string{rangeSpec}, nil
	}
	out, err := gitOut("rev-list", rangeSpec)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Fields(out), nil
}

func changedPaths(sha string) ([]string, error) {
	// --root so the initial commit reports its files instead of failing.
	out, err := gitOut("show", "--pretty=", "--name-only", "--root", sha)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// isWhitespaceOnly reports whether a commit's diff vanishes when whitespace is
// ignored, which is the mechanical definition of a formatting-only change.
func isWhitespaceOnly(sha string) bool {
	out, err := gitOut("show", "-w", "--pretty=", "--numstat", sha)
	if err != nil {
		return false
	}
	return out == ""
}
