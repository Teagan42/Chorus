package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Every file the shell page and script ask for is in the built site, and the
// server inflates to a WebAssembly module: a renamed asset is a demo that
// never boots, which no static host would warn about.
func TestTheSiteHoldsEverythingTheShellAsksFor(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles reviewui to wasm")
	}
	out := t.TempDir()
	t.Chdir(filepath.Join("..", "..", "..")) // build runs from the module root
	if err := build(context.Background(), out); err != nil {
		t.Fatal(err)
	}

	page, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	shell, err := os.ReadFile(filepath.Join(out, "shell.js"))
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	for _, m := range regexp.MustCompile(`(?:src|href)="([^":#]+)"`).FindAllSubmatch(page, -1) {
		asked = append(asked, string(m[1]))
	}
	if m := regexp.MustCompile(`const WASM = "([^"]+)"`).FindSubmatch(shell); m != nil {
		asked = append(asked, string(m[1]))
	}
	if len(asked) < 5 {
		t.Fatalf("found only %v; the shell's references were not read", asked)
	}
	for _, f := range asked {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("the shell asks for %s, which the site does not have", f)
		}
	}
	// The fonts come with the kit's static files, so the page asks no third
	// party for them, and tells none it was opened.
	if m := regexp.MustCompile(`(?:src|href)="https?://[^"]+"`).FindAll(page, -1); m != nil {
		t.Errorf("the shell reaches off the site for %s", m)
	}
	for _, f := range []string{"static/css/fonts.css", "static/fonts/Outfit-Variable.ttf", "static/fonts/JetBrainsMono-Regular.woff2"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("the site lacks %s, so the demo falls back to system fonts", f)
		}
	}

	gz, err := os.ReadFile(filepath.Join(out, wasmName))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(zr, head); err != nil || !bytes.Equal(head, []byte("\x00asm")) {
		t.Errorf("%s does not inflate to a wasm module (starts %q, %v)", wasmName, head, err)
	}
}
