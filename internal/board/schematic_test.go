package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hallway satellite as far as these tests need it: the mute switch the
// ESP32 reads and the LED ring it drives through a 5 V buffer, fed from a
// bench header. Small enough to read, real enough to get wrong.
const hallwayPins = `board: chorus-sat
revision: A
module: ESP32-S3-WROOM-1-N16R8
pins:
  - {gpio: 2, net: MUTE_SENSE, dir: in, peer: hardware mute switch}
  - {gpio: 14, net: LED_DATA, dir: out, peer: 74AHCT1G125 into the SK6812 ring}
`

const hallwayParts = `parts:
  ESP32-S3-WROOM-1-N16R8:
    symbol: RF_Module:ESP32-S3-WROOM-1
    footprint: RF_Module:ESP32-S3-WROOM-1
    pinout: ESP32-S3-WROOM-1 datasheet table 3-1, the pads this fixture needs
    lcsc: C2913202
    pins:
      - {pad: "1", name: GND, type: power_in}
      - {pad: "2", name: 3V3, type: power_in}
      - {pad: "22", name: IO14, type: bidirectional, gpio: 14}
      - {pad: "8", name: IO15, type: bidirectional, gpio: 15}
      - {pad: "38", name: IO2, type: bidirectional, gpio: 2}
      - {pad: "40", name: GND, type: passive}
  74AHCT1G125:
    symbol: 74xGxx:74AHCT1G125
    footprint: Package_TO_SOT_SMD:SOT-23-5
    pinout: SN74AHCT1G125 datasheet, DBV package
    lcsc: C7484
    pins:
      - {pad: "1", name: OE_N, type: input}
      - {pad: "2", name: A, type: input}
      - {pad: "3", name: GND, type: power_in}
      - {pad: "4", name: "Y", type: tri_state}
      - {pad: "5", name: VCC, type: power_in}
  SK6812MINI-E:
    symbol: LED:SK6812MINI-E
    footprint: LED_SMD:LED_SK6812MINI-E_3.2x2.8mm_P1.5mm_ReverseMount
    pinout: SK6812MINI-E datasheet
    mpn: SK6812MINI-E
    lcsc: C5149201
    pins:
      - {pad: "1", name: VSS, type: power_in}
      - {pad: "2", name: DIN, type: input}
      - {pad: "3", name: VDD, type: power_in}
      - {pad: "4", name: DOUT, type: output}
  R_0402:
    symbol: Device:R
    footprint: Resistor_SMD:R_0402_1005Metric
    pinout: two-terminal
    values: {10k: C25744, 330R: C25104}
    pins:
      - {pad: "1", name: "1", type: passive}
      - {pad: "2", name: "2", type: passive}
  SW_SPDT:
    symbol: Switch:SW_SPDT
    footprint: Button_Switch_SMD:SW_SPDT_PCM12
    pinout: PCM12SMTR datasheet
    lcsc: C221841
    pins:
      - {pad: "1", name: A, type: passive}
      - {pad: "2", name: COM, type: passive}
      - {pad: "3", name: B, type: passive}
  BENCH_HEADER:
    symbol: Connector_Generic:Conn_01x03
    footprint: Connector_PinHeader_2.54mm:PinHeader_1x03_P2.54mm_Vertical
    pinout: header
    hand: the bench lead, fitted on the test jig only
    pins:
      - {pad: "1", name: 5V0, type: passive}
      - {pad: "2", name: 3V3, type: passive}
      - {pad: "3", name: GND, type: passive}
`

const hallwayPower = `sheet: power
title: Bench supply
parts:
  J1: {part: BENCH_HEADER}
power:
  5V0: the bench supply on J1
  3V3: the bench supply on J1
  GND: the bench supply's return
nets:
  5V0: [J1.5V0]
  3V3: [J1.3V3]
  GND: [J1.GND]
`

const hallwayMCU = `sheet: mcu
title: ESP32-S3 and the mute switch
parts:
  U1: {part: ESP32-S3-WROOM-1-N16R8}
  SW1: {part: SW_SPDT, note: mute; COM to ground cuts the mics}
  R1: {part: R_0402, value: 10k}
nets:
  3V3: [U1.3V3, R1.1]
  GND: [U1.GND, SW1.COM]
  MUTE_SENSE: [U1.IO2, R1.2, SW1.A]
  LED_DATA: [U1.IO14]
nc: [SW1.B, U1.IO15]
`

const hallwayUI = `sheet: ui
title: LED ring
parts:
  U2: {part: 74AHCT1G125}
  D1: {part: SK6812MINI-E}
nets:
  5V0: [U2.VCC, D1.VDD]
  GND: [U2.GND, U2.OE_N, D1.VSS]
  LED_DATA: [U2.A]
  LED_DIN: [U2.Y, D1.DIN]
nc: [D1.DOUT]
`

// hallway writes the fixture, applying each edit (sheet name -> old -> new)
// to one file, and returns its directory.
func hallway(t *testing.T, edits ...[3]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"pins.yaml":         hallwayPins,
		"parts.yaml":        hallwayParts,
		"sheets/power.yaml": hallwayPower,
		"sheets/mcu.yaml":   hallwayMCU,
		"sheets/ui.yaml":    hallwayUI,
	}
	for _, e := range edits {
		body, ok := files[e[0]]
		if !ok || !strings.Contains(body, e[1]) {
			t.Fatalf("edit %q: %q not in %s", e, e[1], e[0])
		}
		files[e[0]] = strings.Replace(body, e[1], e[2], 1)
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func loadSchematic(t *testing.T, dir string) Schematic {
	t.Helper()
	s, err := LoadSchematic(dir)
	if err != nil {
		t.Fatalf("LoadSchematic: %v", err)
	}
	return s
}

func wantERC(t *testing.T, s Schematic, fragments ...string) {
	t.Helper()
	err := s.Check()
	if err == nil {
		t.Fatalf("Check passed; want a problem naming %q", fragments)
	}
	for _, f := range fragments {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("problem %q does not name %q", err, f)
		}
	}
}

func TestHallwaySchematicChecksClean(t *testing.T) {
	s := loadSchematic(t, hallway(t))
	if err := s.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

// LED_DATA is labelled on the mcu sheet and on the ui sheet; that is one net,
// as two KiCad global labels with one name are.
func TestALabelOnTwoSheetsIsOneNet(t *testing.T) {
	nets, errs := loadSchematic(t, hallway(t)).Nets()
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if got := nodeList(nets["LED_DATA"]); got != "U1.IO14, U2.A" {
		t.Errorf("LED_DATA = %s, want U1.IO14, U2.A", got)
	}
	// Both module ground pads, from one U1.GND, in pad order.
	var pads []string
	for _, n := range nets["GND"] {
		if n.Ref == "U1" {
			pads = append(pads, n.Pad)
		}
	}
	if strings.Join(pads, ",") != "1,40" {
		t.Errorf("U1 ground pads = %v, want 1,40", pads)
	}
}

// The LED ring wired to GPIO15 on the schematic while pins.yaml and the
// ESPHome config say GPIO14: the ring stays dark and nothing else notices.
func TestTheModuleMustBeWiredAsThePinMapSays(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/mcu.yaml", "nc: [SW1.B, U1.IO15]", "nc: [SW1.B, U1.IO14]"},
		[3]string{"sheets/mcu.yaml", "LED_DATA: [U1.IO14]", "LED_DATA: [U1.IO15]"}))
	wantERC(t, s, "U1.IO14", "GPIO14", "pins.yaml says LED_DATA", "U1.IO15 (GPIO15) carries LED_DATA")
}

func TestAPinMapNetMissingFromTheSchematicIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"pins.yaml", "peer: hardware mute switch}", "peer: hardware mute switch}\n  - {gpio: 17, net: AMP_SENSE, dir: in, peer: TAS2780 SDOUT}"}))
	wantERC(t, s, "AMP_SENSE", "GPIO17", "no pad")
}

// The buffer's output-enable left floating: it reads as high on some boards
// and the ring goes dark on those.
func TestAFloatingPinIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/ui.yaml", "U2.GND, U2.OE_N, D1.VSS", "U2.GND, D1.VSS"}))
	wantERC(t, s, "U2.OE_N (pad 1)", "on no net")
}

func TestANetWithOneEndIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/ui.yaml", "nc: [D1.DOUT]", "  LED_RING_OUT: [D1.DOUT]"}))
	wantERC(t, s, "LED_RING_OUT", "two ends")
}

// The last LED's DOUT tied back to the buffer's output: two outputs fighting.
func TestTwoDriversOnOneNetAreRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/ui.yaml", "LED_DIN: [U2.Y, D1.DIN]", "LED_DIN: [U2.Y, D1.DIN, D1.DOUT]"},
		[3]string{"sheets/ui.yaml", "nc: [D1.DOUT]", ""}))
	err := s.Check()
	if err != nil {
		t.Fatalf("a tri-state buffer and one output is legal: %v", err)
	}
	s = loadSchematic(t, hallway(t,
		[3]string{"parts.yaml", `name: "Y", type: tri_state`, `name: "Y", type: output`},
		[3]string{"sheets/ui.yaml", "LED_DIN: [U2.Y, D1.DIN]", "LED_DIN: [U2.Y, D1.DIN, D1.DOUT]"},
		[3]string{"sheets/ui.yaml", "nc: [D1.DOUT]", ""}))
	wantERC(t, s, "LED_DIN is driven by", "U2.Y", "D1.DOUT")
}

func TestANetOfOnlyInputsIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/ui.yaml", "LED_DIN: [U2.Y, D1.DIN]", "LED_DIN: [U2.A, D1.DIN]\n  LED_Y: [U2.Y]"},
		[3]string{"sheets/ui.yaml", "LED_DATA: [U2.A]", ""}))
	wantERC(t, s, "LED_DIN has only inputs")
}

// The ring moved to a 1V8 rail that no regulator makes.
func TestARailNothingSuppliesIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/ui.yaml", "5V0: [U2.VCC, D1.VDD]", "1V8: [U2.VCC, D1.VDD]"}))
	wantERC(t, s, "1V8 powers D1.VDD, U2.VCC but nothing supplies it", "5V0 has only J1.5V0")
}

func TestARailMadeOnTwoSheetsIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/ui.yaml", "parts:", "power:\n  5V0: a second buck nobody drew\nparts:"}))
	wantERC(t, s, "rail 5V0 is made on both sheet power and sheet ui")
}

func TestAPinOnTwoNetsIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/mcu.yaml", "GND: [U1.GND, SW1.COM]", "GND: [U1.GND, SW1.COM, R1.2]"}))
	wantERC(t, s, "R1.2 is on both GND and MUTE_SENSE")
}

func TestAPinThePartDoesNotHaveIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/ui.yaml", "LED_DIN: [U2.Y, D1.DIN]", "LED_DIN: [U2.Y, D1.DI]"}))
	wantERC(t, s, "D1.DI", "a SK6812MINI-E has no pin DI")
}

func TestAPartTheLibraryDoesNotDefineIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/ui.yaml", "{part: SK6812MINI-E}", "{part: WS2812B}"}))
	wantERC(t, s, "D1 is a \"WS2812B\", which parts.yaml does not define")
}

func TestOneReferenceOnTwoSheetsIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/ui.yaml", "U2: {part: 74AHCT1G125}", "U2: {part: 74AHCT1G125}\n  R1: {part: R_0402, value: 330R}"}))
	wantERC(t, s, "R1 is placed on both sheet mcu and sheet ui")
}

func TestANoConnectThatIsWiredIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/mcu.yaml", "nc: [SW1.B, U1.IO15]", "nc: [SW1.B, U1.IO15, R1.1]"}))
	wantERC(t, s, "R1.1 is marked nc but is on 3V3")
}

func TestAPadWithoutAPinoutSourceIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"parts.yaml", "pinout: SK6812MINI-E datasheet", `pinout: ""`}))
	wantERC(t, s, "SK6812MINI-E does not say where its pinout was read from")
}

func TestAPadListedTwiceIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"parts.yaml", `{pad: "4", name: DOUT, type: output}`, `{pad: "3", name: DOUT, type: output}`}))
	wantERC(t, s, "SK6812MINI-E lists pad 3 twice")
}

func TestASheetFileMustNameItsSheet(t *testing.T) {
	_, err := LoadSchematic(hallway(t, [3]string{"sheets/ui.yaml", "sheet: ui", "sheet: leds"}))
	if err == nil || !strings.Contains(err.Error(), `named "leds"`) {
		t.Errorf("LoadSchematic = %v, want the mismatch named", err)
	}
}

func TestASheetFieldTheFormatDoesNotDefineIsRefused(t *testing.T) {
	_, err := LoadSchematic(hallway(t, [3]string{"sheets/ui.yaml", "title: LED ring", "title: LED ring\nrev: B"}))
	if err == nil || !strings.Contains(err.Error(), "rev") {
		t.Errorf("LoadSchematic = %v, want the unknown field named", err)
	}
}

func TestAModuleTheSheetsNeverPlaceIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/mcu.yaml", "U1: {part: ESP32-S3-WROOM-1-N16R8}", ""},
		[3]string{"sheets/mcu.yaml", "3V3: [U1.3V3, R1.1]", "3V3: [R1.1]"},
		[3]string{"sheets/mcu.yaml", "GND: [U1.GND, SW1.COM]", "GND: [SW1.COM]"},
		[3]string{"sheets/mcu.yaml", "MUTE_SENSE: [U1.IO2, R1.2, SW1.A]", "MUTE_SENSE: [R1.2, SW1.A]"},
		[3]string{"sheets/mcu.yaml", "LED_DATA: [U1.IO14]\n", ""},
		[3]string{"sheets/mcu.yaml", ", U1.IO15]", "]"}))
	wantERC(t, s, "pins.yaml is for one ESP32-S3-WROOM-1-N16R8; the sheets place 0")
}

// A 4k7 pull-up typed as "4.7K" where the library lists 4k7: JLCPCB would be
// handed a BOM line in a spelling nobody checked.
func TestAValueTheLibraryDoesNotListIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/mcu.yaml", "{part: R_0402, value: 10k}", "{part: R_0402, value: 4.7K}"}))
	wantERC(t, s, "R1 is a R_0402 of 4.7K, a value parts.yaml does not list for it")
}

// A listed value with no number is JLCPCB's to match from its basic
// library, by value and footprint, which is how its BOM upload treats it.
func TestAListedValueWithoutANumberIsLeftForJLCPCBToMatch(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"parts.yaml", "values: {10k: C25744, 330R: C25104}", `values: {10k: C25744, 330R: C25104, 4k7: ""}`},
		[3]string{"sheets/mcu.yaml", "{part: R_0402, value: 10k}", "{part: R_0402, value: 4k7}"}))
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(s.BOM()), "4k7,R1,Resistor_SMD:R_0402_1005Metric,,") {
		t.Errorf("BOM does not leave R1's number for JLCPCB:\n%s", s.BOM())
	}
}

func TestAPartJLCPCBCannotPlaceMustSayWhy(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"parts.yaml", "    lcsc: C5149201\n", ""}))
	wantERC(t, s, "SK6812MINI-E has no LCSC number", "hand:")
}

func TestAPartBoughtByValueNeedsOne(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/mcu.yaml", "{part: R_0402, value: 10k}", "{part: R_0402}"}))
	wantERC(t, s, "R1 is a R_0402 with no value")
	s = loadSchematic(t, hallway(t,
		[3]string{"sheets/ui.yaml", "{part: SK6812MINI-E}", "{part: SK6812MINI-E, value: red}"}))
	wantERC(t, s, "D1 gives value red, but a SK6812MINI-E is not bought by value")
}

// The TPS2121 brings its output out on two pads; that is one output on
// VSYS, not two outputs fighting.
func TestTwoPadsOfOnePinAreOneDriver(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"parts.yaml", `{pad: "4", name: "Y", type: tri_state}`, `{pad: "4", name: "Y", type: output}`},
		[3]string{"parts.yaml", `{pad: "5", name: VCC, type: power_in}`, `{pad: "5", name: VCC, type: power_in}
      - {pad: "6", name: "Y", type: output}`}))
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
}

// The ring's LEDs mount on B.Cu and shine up through their cutouts; "bottom"
// is a typo the board generator would read as the top, so it is refused.
func TestASideOtherThanBackIsRefused(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"parts.yaml", "lcsc: C5149201", "lcsc: C5149201\n    side: bottom"}))
	wantERC(t, s, `SK6812MINI-E: side "bottom" is neither empty (the top) nor back`)
}
