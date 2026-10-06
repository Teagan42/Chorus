// Command docexec runs the code examples in markdown so documentation cannot
// drift from the code it describes (CONTRIBUTING.md §5). Go blocks must
// compile; sh and cue blocks must exit zero. Tag a fence `norun` to opt out.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Block is one fenced code block.
type Block struct {
	File string
	Line int // 1-indexed line of the opening fence
	Lang string
	Code string
	Err  string // set when the block is malformed
}

// Location renders a clickable file:line reference.
func (b Block) Location() string { return fmt.Sprintf("%s:%d", b.File, b.Line) }

// runnable languages. Everything else is illustrative by nature.
var runnable = map[string]bool{"go": true, "sh": true, "bash": true, "cue": true}

func main() {
	targets := os.Args[1:]
	if len(targets) == 0 {
		targets = []string{"docs", "CONTRIBUTING.md"}
	}

	files, err := collect(targets)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docexec: %v\n", err)
		os.Exit(2)
	}

	var ran, failed int
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "docexec: %v\n", err)
			os.Exit(2)
		}
		for _, b := range ParseBlocks(file, string(src)) {
			if b.Err != "" {
				failed++
				fmt.Printf("  FAIL %s: %s\n", b.Location(), b.Err)
				continue
			}
			ran++
			dir, err := os.MkdirTemp("", "docexec")
			if err != nil {
				fmt.Fprintf(os.Stderr, "docexec: %v\n", err)
				os.Exit(2)
			}
			runErr := RunBlock(dir, b)
			os.RemoveAll(dir)
			if runErr != nil {
				failed++
				fmt.Printf("  FAIL %s (%s)\n         %v\n", b.Location(), b.Lang, runErr)
				continue
			}
			fmt.Printf("  ok   %s (%s)\n", b.Location(), b.Lang)
		}
	}

	fmt.Printf("\ndocexec: %d examples, %d failed\n", ran, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func collect(targets []string) ([]string, error) {
	var files []string
	for _, target := range targets {
		info, err := os.Stat(target)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			files = append(files, target)
			continue
		}
		err = filepath.WalkDir(target, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(path, ".md") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// ParseBlocks extracts runnable fenced blocks. Fence length is tracked so a
// block whose body contains a fence is not cut short.
func ParseBlocks(file, src string) []Block {
	lines := strings.Split(src, "\n")
	var out []Block

	for i := 0; i < len(lines); i++ {
		fence, info, ok := openFence(lines[i])
		if !ok {
			continue
		}
		fields := strings.Fields(info)
		lang, tags := "", []string(nil)
		if len(fields) > 0 {
			lang, tags = fields[0], fields[1:]
		}

		var body []string
		closed := false
		j := i + 1
		for ; j < len(lines); j++ {
			if isCloseFence(lines[j], fence) {
				closed = true
				break
			}
			body = append(body, lines[j])
		}

		block := Block{File: file, Line: i + 1, Lang: lang, Code: joinBody(body)}
		i = j // resume after the block regardless of outcome

		if !runnable[lang] || hasTag(tags, "norun") {
			continue
		}
		if !closed {
			block.Err = "unterminated code fence"
		}
		out = append(out, block)
	}
	return out
}

func openFence(line string) (fence, info string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	n := 0
	for n < len(trimmed) && trimmed[n] == '`' {
		n++
	}
	if n < 3 {
		return "", "", false
	}
	return trimmed[:n], strings.TrimSpace(trimmed[n:]), true
}

func isCloseFence(line, fence string) bool {
	trimmed := strings.TrimSpace(line)
	// A closing fence is at least as long as the opener and carries no info.
	return strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, "`") == ""
}

func joinBody(body []string) string {
	if len(body) == 0 {
		return ""
	}
	return strings.Join(body, "\n") + "\n"
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// RunBlock executes one block in dir. Go blocks are type-checked but not run:
// examples should prove the API exists, not have side effects.
func RunBlock(dir string, b Block) error {
	switch b.Lang {
	case "sh", "bash":
		return run(dir, "sh", "-e", "-c", b.Code)

	case "cue":
		path := filepath.Join(dir, "example.cue")
		if err := os.WriteFile(path, []byte(b.Code), 0o644); err != nil {
			return err
		}
		return run(dir, "cue", "vet", "-c", path)

	case "go":
		return buildGo(b.Code)
	}
	return fmt.Errorf("unrunnable language %q", b.Lang)
}

// buildGo type-checks a snippet against this module, so an example referencing
// a renamed symbol fails.
func buildGo(code string) error {
	// Fragments without a package clause are type-checked as a library, not a
	// command: most doc examples show a function, not a whole program.
	src := code
	if !strings.Contains(src, "package ") {
		src = "package example\n\n" + src
	}

	root, err := moduleRoot()
	if err != nil {
		return err
	}

	// Build inside the real module so imports of chorus packages resolve.
	// The directory name is unique because blocks may run concurrently.
	target, err := os.MkdirTemp(filepath.Join(root, "internal", "tools", "docexec"), "tmpexample")
	if err != nil {
		return err
	}
	defer os.RemoveAll(target)

	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte(src), 0o644); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	// -o os.DevNull: the goal is type-checking, not an artifact. A non-main
	// package produces no binary, so a fixed output path would fail.
	return run(root, "go", "build", "-o", os.DevNull, "./"+filepath.ToSlash(rel))
}

func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", err
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", fmt.Errorf("not inside a Go module")
	}
	return filepath.Dir(gomod), nil
}

func run(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
