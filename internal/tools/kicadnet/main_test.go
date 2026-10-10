package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const repoBoard = "../../../hardware/chorus-sat"

// copyBoard copies the checked-in board's data, not its outputs, so a test
// can break it without touching the tree.
func copyBoard(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	for _, rel := range []string{"pins.yaml", "parts.yaml"} {
		copyFile(t, filepath.Join(repoBoard, rel), filepath.Join(dst, rel))
	}
	sheets, err := filepath.Glob(filepath.Join(repoBoard, "sheets", "*.yaml"))
	if err != nil || len(sheets) == 0 {
		t.Fatalf("no sheets under %s: %v", repoBoard, err)
	}
	for _, s := range sheets {
		copyFile(t, s, filepath.Join(dst, "sheets", filepath.Base(s)))
	}
	return dst
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The command as `task gen:hardware` runs it, built and executed, on a copy
// of the real board: what it writes must be byte for byte what is committed,
// or JLCPCB gets a BOM for a board nobody checked.
func TestTheCommittedOutputsAreWhatTheCommandWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the command")
	}
	bin := filepath.Join(t.TempDir(), "kicadnet")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	dir := copyBoard(t)
	if out, err := exec.Command(bin, dir).CombinedOutput(); err != nil {
		t.Fatalf("kicadnet %s: %v\n%s", dir, err, out)
	}
	for _, name := range []string{"chorus-sat.net", "bom.csv"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(repoBoard, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale; run task gen:hardware and commit it in a chore(gen) commit", name)
		}
	}
}

// Someone moves the LED ring to GPIO15 on the sheet and not in pins.yaml.
// The command must refuse, name the pad, and leave no netlist behind to
// upload by mistake.
func TestABoardThatFailsItsChecksGetsNoNetlist(t *testing.T) {
	dir := copyBoard(t)
	ui := filepath.Join(dir, "sheets", "mcu.yaml")
	b, err := os.ReadFile(ui)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("U1.IO14")) {
		t.Fatal("mcu sheet no longer wires U1.IO14; update this test")
	}
	if err := os.WriteFile(ui, bytes.Replace(b, []byte("U1.IO14"), []byte("U1.IO15"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	var stderr bytes.Buffer
	if code := run([]string{"-out", out, dir}, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1; stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "LED_DATA") || !strings.Contains(stderr.String(), "no netlist was written") {
		t.Errorf("stderr does not say why:\n%s", stderr.String())
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("wrote %d files for a board that failed its checks", len(entries))
	}
}

func TestAMissingBoardIsAnError(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{filepath.Join(t.TempDir(), "chorus-sat")}, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "pins.yaml") {
		t.Errorf("stderr = %q, want the missing file named", stderr.String())
	}
}
