package main

import (
	"strings"
	"testing"
)

func TestParseBlocksFindsFencedCode(t *testing.T) {
	md := "Intro text.\n\n" +
		"```sh\necho hello\n```\n\n" +
		"More prose.\n\n" +
		"```go\npackage main\n```\n"

	blocks := ParseBlocks("x.md", md)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2: %+v", len(blocks), blocks)
	}
	if blocks[0].Lang != "sh" || blocks[0].Code != "echo hello\n" {
		t.Errorf("block 0 = %+v", blocks[0])
	}
	if blocks[1].Lang != "go" {
		t.Errorf("block 1 lang = %q", blocks[1].Lang)
	}
	// Line numbers must point at the fence so failures are navigable.
	if blocks[0].Line != 3 {
		t.Errorf("block 0 line = %d, want 3", blocks[0].Line)
	}
}

func TestParseBlocksSkipsUnrunnableLanguages(t *testing.T) {
	md := "```yaml\nkey: value\n```\n```\nplain\n```\n```text\nfoo\n```\n"
	if blocks := ParseBlocks("x.md", md); len(blocks) != 0 {
		t.Errorf("want no runnable blocks, got %+v", blocks)
	}
}

// `norun` marks illustrative snippets. Without it, docs could not show a
// counter-example or a command with real side effects.
func TestParseBlocksHonorsNorun(t *testing.T) {
	md := "```sh norun\nrm -rf /\n```\n```go norun\nnot valid go at all\n```\n"
	if blocks := ParseBlocks("x.md", md); len(blocks) != 0 {
		t.Errorf("norun blocks must be skipped, got %+v", blocks)
	}
}

func TestParseBlocksIgnoresFencesInsideBlocks(t *testing.T) {
	// A sh block that prints a markdown fence must not end the block early.
	md := "````sh\necho '```'\n````\n"
	blocks := ParseBlocks("x.md", md)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1: %+v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[0].Code, "```") {
		t.Errorf("inner fence lost: %q", blocks[0].Code)
	}
}

func TestParseBlocksUnterminatedFenceIsAnError(t *testing.T) {
	md := "```sh\necho oops\n"
	blocks := ParseBlocks("x.md", md)
	if len(blocks) != 1 || blocks[0].Err == "" {
		t.Fatalf("unterminated fence should surface an error: %+v", blocks)
	}
}

func TestRunBlockShell(t *testing.T) {
	ok := Block{File: "x.md", Line: 1, Lang: "sh", Code: "true\n"}
	if err := RunBlock(t.TempDir(), ok); err != nil {
		t.Errorf("passing block reported failure: %v", err)
	}

	bad := Block{File: "x.md", Line: 1, Lang: "sh", Code: "exit 3\n"}
	if err := RunBlock(t.TempDir(), bad); err == nil {
		t.Error("failing shell block should report failure")
	}
}

// A doc example that does not compile is a lie about the API.
func TestRunBlockGoCompilesWithoutRunning(t *testing.T) {
	good := Block{File: "x.md", Line: 1, Lang: "go", Code: `package main

import "fmt"

func main() { fmt.Println("hi") }
`}
	if err := RunBlock(t.TempDir(), good); err != nil {
		t.Errorf("valid go example rejected: %v", err)
	}

	bad := Block{File: "x.md", Line: 1, Lang: "go", Code: "package main\nfunc main() { undefinedCall() }\n"}
	if err := RunBlock(t.TempDir(), bad); err == nil {
		t.Error("go example that does not compile should fail")
	}
}

// Most doc examples are a function, not a program. Requiring func main would
// push authors toward `norun`, which defeats the point.
func TestRunBlockGoAcceptsLibraryFragment(t *testing.T) {
	frag := Block{File: "x.md", Line: 1, Lang: "go", Code: `import "time"

func backoff(n int) time.Duration { return time.Duration(n) * time.Second }
`}
	if err := RunBlock(t.TempDir(), frag); err != nil {
		t.Errorf("library fragment rejected: %v", err)
	}
}

// A fragment citing a chorus symbol must resolve, or docs can claim an API
// that does not exist.
func TestRunBlockGoResolvesModuleImports(t *testing.T) {
	good := Block{File: "x.md", Line: 1, Lang: "go", Code: `import "github.com/teaganglenn/chorus/internal/config"

func load() (*config.Config, error) { return config.Load("devices.yaml") }
`}
	if err := RunBlock(t.TempDir(), good); err != nil {
		t.Errorf("valid module import rejected: %v", err)
	}

	bad := Block{File: "x.md", Line: 1, Lang: "go", Code: `import "github.com/teaganglenn/chorus/internal/config"

func load() { config.NoSuchFunction() }
`}
	if err := RunBlock(t.TempDir(), bad); err == nil {
		t.Error("example citing a nonexistent symbol should fail")
	}
}

func TestBlockLocationIsNavigable(t *testing.T) {
	b := Block{File: "docs/SPEC.md", Line: 42, Lang: "sh"}
	if got := b.Location(); got != "docs/SPEC.md:42" {
		t.Errorf("Location() = %q", got)
	}
}
