package board

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The bench header on the hallway fixture, as the board's own footprint: the
// test jig's three-pin lead with a mounting hole that carries no net.
const benchJig = `(footprint "Bench_Jig_1x03" (version 20221018) (generator pcbnew) (layer "F.Cu")
  (pad "" np_thru_hole circle (at 0 -3) (size 2.2 2.2) (drill 2.2) (layers "*.Cu" "*.Mask"))
  (pad "1" thru_hole rect (at 0 0) (size 1.7 1.7) (drill 1) (layers "*.Cu" "*.Mask"))
  (pad "2" thru_hole oval (at 0 2.54) (size 1.7 1.7) (drill 1) (layers "*.Cu" "*.Mask"))
  (pad "3" thru_hole oval (at 0 5.08) (size 1.7 1.7) (drill 1) (layers "*.Cu" "*.Mask"))
)
`

// hallwayJig is the hallway fixture with the bench header on the board's own
// footprint, written as given.
func hallwayJig(t *testing.T, footprint string) string {
	t.Helper()
	dir := hallway(t, [3]string{
		"parts.yaml",
		"footprint: Connector_PinHeader_2.54mm:PinHeader_1x03_P2.54mm_Vertical",
		"footprint: chorus-sat:Bench_Jig_1x03",
	})
	if footprint == "" {
		return dir
	}
	lib := filepath.Join(dir, "chorus-sat.pretty")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "Bench_Jig_1x03.kicad_mod"), []byte(footprint), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestABoardFootprintWithThePartsPadsChecksClean(t *testing.T) {
	s := loadSchematic(t, hallwayJig(t, benchJig))
	if err := s.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if u := s.Undrawn(); len(u) != 0 {
		t.Errorf("Undrawn = %v with the jig footprint drawn", u)
	}
}

// Pcbnew would import J1's GND node onto a pad that is not there and drop
// it: the jig would power up with no ground.
func TestAFootprintMissingAListedPadFails(t *testing.T) {
	short := benchJig[:len(benchJig)-len(`  (pad "3" thru_hole oval (at 0 5.08) (size 1.7 1.7) (drill 1) (layers "*.Cu" "*.Mask"))
)
`)] + ")\n"
	wantERC(t, loadSchematic(t, hallwayJig(t, short)), "BENCH_HEADER lists pad 3 (GND)", "chorus-sat:Bench_Jig_1x03")
}

func TestAFootprintPadNoPinListsFails(t *testing.T) {
	extra := benchJig[:len(benchJig)-2] +
		`  (pad "4" thru_hole oval (at 0 7.62) (size 1.7 1.7) (drill 1) (layers "*.Cu" "*.Mask"))
)
`
	wantERC(t, loadSchematic(t, hallwayJig(t, extra)), "has pad 4, which BENCH_HEADER does not list")
}

func TestAFootprintNotYetDrawnIsListedNotFailed(t *testing.T) {
	s := loadSchematic(t, hallwayJig(t, ""))
	if err := s.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if u := s.Undrawn(); !slices.Equal(u, []string{"chorus-sat:Bench_Jig_1x03"}) {
		t.Errorf("Undrawn = %v", u)
	}
}

// KiCad 5 wrote pad numbers bare; a vendored footprint may be that old.
func TestABareKiCad5PadNumberCounts(t *testing.T) {
	old := `(module Bench_Jig_1x03 (layer F.Cu)
  (pad 1 thru_hole rect (at 0 0) (size 1.7 1.7) (drill 1) (layers *.Cu *.Mask))
  (pad 2 thru_hole oval (at 0 2.54) (size 1.7 1.7) (drill 1) (layers *.Cu *.Mask))
  (pad 3 thru_hole oval (at 0 5.08) (size 1.7 1.7) (drill 1) (layers *.Cu *.Mask))
)
`
	if err := loadSchematic(t, hallwayJig(t, old)).Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
}
