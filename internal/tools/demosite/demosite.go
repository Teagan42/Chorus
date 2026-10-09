// Command demosite builds the hosted review UI demo: reviewui compiled to
// WebAssembly over the household's Thursday, the shell page it runs under
// (cmd/reviewui/web), and the kit's static files, as one directory any
// static host can serve from any path. The docs site publishes it at /demo/.
//
//	go run ./internal/tools/demosite -out site/demo
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/teaganglenn/chorus/cmd/reviewui/web"
	"github.com/teaganglenn/chorus/internal/reviewui/ui"
)

// wasmName is what the shell fetches; gzipped here because a static host may
// not compress WebAssembly, and the shell inflates it as it streams in.
const wasmName = "reviewui.wasm.gz"

func main() {
	out := flag.String("out", "site/demo", "directory to build the demo into; replaced")
	flag.Parse()
	if err := build(context.Background(), *out); err != nil {
		fmt.Fprintf(os.Stderr, "demosite: %v\n", err)
		os.Exit(1)
	}
}

func build(ctx context.Context, out string) error {
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	wasm, err := compile(ctx)
	if err != nil {
		return err
	}
	if err := writeGzip(filepath.Join(out, wasmName), wasm); err != nil {
		return err
	}
	goroot, err := goEnv(ctx, "GOROOT")
	if err != nil {
		return err
	}
	// wasm_exec.js must match the toolchain that compiled the module.
	glue, err := os.ReadFile(filepath.Join(goroot, "lib", "wasm", "wasm_exec.js"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "wasm_exec.js"), glue, 0o644); err != nil {
		return err
	}
	if err := os.CopyFS(out, web.Files); err != nil {
		return fmt.Errorf("shell: %w", err)
	}
	if err := os.CopyFS(filepath.Join(out, "static"), ui.StaticFS()); err != nil {
		return fmt.Errorf("static: %w", err)
	}
	return report(out)
}

// compile builds cmd/reviewui for the browser. Run from the module root.
func compile(ctx context.Context) ([]byte, error) {
	dir, err := os.MkdirTemp("", "demosite")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	bin := filepath.Join(dir, "reviewui.wasm")
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", bin, "./cmd/reviewui")
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("compile reviewui to wasm: %w", err)
	}
	return os.ReadFile(bin)
}

func writeGzip(path string, b []byte) error {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := zw.Write(b); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func goEnv(ctx context.Context, key string) (string, error) {
	b, err := exec.CommandContext(ctx, "go", "env", key).Output()
	if err != nil {
		return "", fmt.Errorf("go env %s: %w", key, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func report(out string) error {
	var files int
	var size int64
	err := filepath.WalkDir(out, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files++
		size += info.Size()
		return nil
	})
	fmt.Printf("demosite: %d files, %.1f MB in %s\n", files, float64(size)/1e6, out)
	return err
}
