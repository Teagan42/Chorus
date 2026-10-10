package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const repoBoard = "../../../hardware/chorus-main"

// copyBoard copies the checked-in board's data, not its outputs, so a test
// can break it without touching the tree.
func copyBoard(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	for _, rel := range []string{"pins.yaml", "parts.yaml", "satellite1-hat.yaml"} {
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
	out, err := exec.Command(bin, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("kicadnet %s: %v\n%s", dir, err, out)
	}
	// The receptacle's power contacts are inferred; every run says so.
	if !strings.Contains(string(out), "FX23L-80S-0.5SV") || !strings.Contains(string(out), "do not order") {
		t.Errorf("kicadnet did not warn about the unverified receptacle:\n%s", out)
	}
	for _, name := range []string{"chorus-main.net", "bom.csv"} {
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

// Someone moves the W5500's select onto the XMOS's select pad. The ESP32
// would deselect the XU316 every Ethernet frame; the command must refuse,
// name the pad, and leave no netlist behind to upload by mistake.
func TestABoardThatFailsItsChecksGetsNoNetlist(t *testing.T) {
	dir := copyBoard(t)
	sheet := filepath.Join(dir, "sheets", "connector.yaml")
	b, err := os.ReadFile(sheet)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ETH_CS_N: [J1.37]", "  - J1.24\n"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("connector sheet no longer has %q; update this test", want)
		}
	}
	b = bytes.Replace(b, []byte("ETH_CS_N: [J1.37]"), []byte("ETH_CS_N: [J1.24]"), 1)
	b = bytes.Replace(b, []byte("  - J1.24\n"), []byte("  - J1.37\n"), 1)
	if err := os.WriteFile(sheet, b, 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	var stderr bytes.Buffer
	if code := run([]string{"-out", out, dir}, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1; stderr:\n%s", code, stderr.String())
	}
	for _, want := range []string{"J1.24", "XMOS_SPI_CS_N", "no netlist was written"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not name %q:\n%s", want, stderr.String())
		}
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("wrote %d files for a board that failed its checks", len(entries))
	}
}

func TestAMissingBoardIsAnError(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{filepath.Join(t.TempDir(), "chorus-main")}, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "pins.yaml") {
		t.Errorf("stderr = %q, want the missing file named", stderr.String())
	}
}
