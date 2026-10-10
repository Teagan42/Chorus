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
	// Every part is confirmed, the receptacle's power contacts by a meter on
	// a household HAT, so nothing tells anyone not to order.
	if strings.Contains(string(out), "do not order") {
		t.Errorf("kicadnet warned about a board with nothing unverified:\n%s", out)
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

// JLCPCB shows the PoE module out of stock, so someone marks it unverified
// until a restock is confirmed. The netlist and BOM are still written, for
// layout to start, but every run says the board is not to be ordered and
// which part holds it up.
func TestAnUnverifiedPartIsSaidOnEveryRun(t *testing.T) {
	dir := copyBoard(t)
	parts := filepath.Join(dir, "parts.yaml")
	b, err := os.ReadFile(parts)
	if err != nil {
		t.Fatal(err)
	}
	const ag9912 = "    lcsc: C20939164\n"
	if !bytes.Contains(b, []byte(ag9912)) {
		t.Fatalf("parts.yaml no longer has %q; update this test", ag9912)
	}
	b = bytes.Replace(b, []byte(ag9912), []byte(ag9912+"    unverified: JLCPCB lists none in stock\n"), 1)
	if err := os.WriteFile(parts, b, 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	var stderr bytes.Buffer
	if code := run([]string{"-out", out, dir}, &stderr); code != 0 {
		t.Fatalf("exit %d, want 0; stderr:\n%s", code, stderr.String())
	}
	want := "not yet verified, do not order: AG9912-MTB: JLCPCB lists none in stock"
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr does not say %q:\n%s", want, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(out, "bom.csv")); err != nil {
		t.Errorf("no BOM written for a board that only waits on a part: %v", err)
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
