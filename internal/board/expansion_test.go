package board

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The porch satellite's main board, as far as these tests need it: under a
// Satellite1 HAT, it takes two free GPIOs for the W5500's reset and select,
// grounds the kit's ground, and makes its own 3V3 from the HAT's 5 V. The
// kit is cut down to the pads that show each rule; the real one has 86.
const porchKit = `kit: FutureProofHomes Satellite1, HAT rev 6.1 on Core rev 5.1
connector: J7
plug: Hirose FX23L-80P-0.5SV8
mate: FX23L-80S-0.5SV
pads:
  - {pad: "1", label: USB_2_Sel, net: CORE_3V3, use: "the Core's 3V3, header pin 1"}
  - {pad: "2", label: ""}
  - {pad: "3", label: I2C_SDA, net: I2C_SDA, gpio: 5, use: "I2C to the HAT's PD controller", satellite1: "i2c[0].sda"}
  - {pad: "6", label: GND, net: GND, use: ground}
  - {pad: "13", label: GPIO02, gpio: 2}
  - {pad: "24", label: SPI_CS_N, net: XMOS_SPI_CS_N, gpio: 10, use: "the XU316's SPI select", satellite1: "satellite1.cs_pin"}
  - {pad: "37", label: GPIO03, gpio: 3}
  - {pad: MH2, label: +5V, net: 5V0, use: "the HAT's 5 V rail"}
  - {pad: MH4, label: VBUS, net: VBUS, use: "the HAT's input rail"}
`

const porchPins = `board: porch-main
revision: A
module: ESP32-S3-WROOM-1-N16R8
expansion: satellite1-hat.yaml
pins:
  - {gpio: 2, pad: "13", net: ETH_RST_N, dir: out, peer: W5500 RST_N}
  - {gpio: 3, pad: "37", net: ETH_CS_N, dir: out, peer: W5500 SCS_N, strap: "JTAG source, read only with the eFuse burned"}
`

const porchParts = `parts:
  FX23L-80S-0.5SV:
    symbol: chorus-main:FX23L-80S-0.5SV
    footprint: chorus-main:Hirose_FX23L-80S-0.5SV
    pinout: the HAT's J7, pad for pad
    hand: no LCSC number confirmed
    pins:
      - {pad: "1", name: "1", type: passive}
      - {pad: "2", name: "2", type: passive}
      - {pad: "3", name: "3", type: passive}
      - {pad: "6", name: "6", type: passive}
      - {pad: "13", name: "13", type: passive}
      - {pad: "24", name: "24", type: passive}
      - {pad: "37", name: "37", type: passive}
      - {pad: MH2, name: MH2, type: passive}
      - {pad: MH4, name: MH4, type: passive}
  SPX3819M5-L-3-3:
    symbol: Regulator_Linear:SPX3819M5-L-3-3
    footprint: Package_TO_SOT_SMD:SOT-23-5
    pinout: MaxLinear SPX3819 SOT-23-5
    lcsc: C20617301
    pins:
      - {pad: "1", name: IN, type: power_in}
      - {pad: "2", name: GND, type: power_in}
      - {pad: "3", name: EN, type: input}
      - {pad: "4", name: BP, type: passive}
      - {pad: "5", name: OUT, type: power_out}
  R_0402:
    symbol: Device:R
    footprint: Resistor_SMD:R_0402_1005Metric
    pinout: two-terminal
    values: {10k: C25744}
    pins:
      - {pad: "1", name: "1", type: passive}
      - {pad: "2", name: "2", type: passive}
  C_0402:
    symbol: Device:C
    footprint: Capacitor_SMD:C_0402_1005Metric
    pinout: two-terminal
    values: {1uF: C52923}
    pins:
      - {pad: "1", name: "1", type: passive}
      - {pad: "2", name: "2", type: passive}
`

const porchConnector = `sheet: connector
title: The HAT's expansion connector
parts:
  J1: {part: FX23L-80S-0.5SV}
nets:
  GND: [J1.6]
  5V0: [J1.MH2]
  ETH_RST_N: [J1.13]
  ETH_CS_N: [J1.37]
nc: [J1.1, J1.2, J1.3, J1.24, J1.MH4]
`

const porchEthernet = `sheet: ethernet
title: The W5500's 3V3 and its pull-ups
parts:
  U1: {part: SPX3819M5-L-3-3}
  C1: {part: C_0402, value: 1uF}
  R1: {part: R_0402, value: 10k, note: RST pull-up}
  R2: {part: R_0402, value: 10k, note: CS pull-up}
power:
  5V0: the HAT, through J1 MH2
  GND: the HAT, through J1
nets:
  5V0: [U1.IN, U1.EN]
  3V3: [U1.OUT, C1.1, R1.1, R2.1]
  GND: [U1.GND, C1.2]
  ETH_RST_N: [R1.2]
  ETH_CS_N: [R2.2]
nc: [U1.BP]
`

// porch writes the fixture as hallway does, edits keyed by file name.
func porch(t *testing.T, edits ...[3]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"pins.yaml":             porchPins,
		"satellite1-hat.yaml":   porchKit,
		"parts.yaml":            porchParts,
		"sheets/connector.yaml": porchConnector,
		"sheets/ethernet.yaml":  porchEthernet,
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

func TestThePorchMainBoardChecksClean(t *testing.T) {
	s := loadSchematic(t, porch(t))
	if s.Kit == nil || len(s.Kit.Pads) != 9 {
		t.Fatalf("kit = %+v, want the nine pads of the fixture", s.Kit)
	}
	if err := s.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

// Pad 2 is unconnected on the HAT; a ground there grounds nothing and hides
// that the board thinks it has one more return than it does.
func TestAPadTheKitLeavesUnconnectedStaysUnconnected(t *testing.T) {
	s := loadSchematic(t, porch(t,
		[3]string{"sheets/connector.yaml", "GND: [J1.6]", "GND: [J1.6, J1.2]"},
		[3]string{"sheets/connector.yaml", "J1.2, ", ""}))
	wantERC(t, s, "J1.2 is on GND", "J7 pad 2 is unconnected on the kit")
}

// Every kit ground is a return for the HAT's audio and the W5500's SPI.
func TestAKitGroundLeftOpenIsRefused(t *testing.T) {
	s := loadSchematic(t, porch(t,
		[3]string{"sheets/connector.yaml", "  GND: [J1.6]\n", ""},
		[3]string{"sheets/connector.yaml", "nc: [J1.1,", "nc: [J1.6, J1.1,"}))
	wantERC(t, s, `J1.6 is on ""`, "the kit grounds J7 pad 6")
}

// The W5500's select moved onto the XMOS's: every Ethernet frame would
// deselect the XU316 mid-transaction.
func TestAPinTheKitAlreadyUsesIsRefused(t *testing.T) {
	s := loadSchematic(t, porch(t,
		[3]string{"pins.yaml", `{gpio: 3, pad: "37"`, `{gpio: 10, pad: "24"`},
		[3]string{"sheets/connector.yaml", "ETH_CS_N: [J1.37]", "ETH_CS_N: [J1.24]"},
		[3]string{"sheets/connector.yaml", "J1.24, ", "J1.37, "}))
	wantERC(t, s,
		"pins.yaml takes GPIO10 (pad 24) for ETH_CS_N, but the kit already uses it: the XU316's SPI select",
		"J1.24 is on ETH_CS_N, but the kit puts XMOS_SPI_CS_N there")
}

// The HAT labels pad 13 GPIO02 and the Core puts GPIO2 behind it; a map
// that says GPIO4 would drive the XU316's reset instead.
func TestAPinMapGPIOTheKitPadDoesNotCarryIsRefused(t *testing.T) {
	s := loadSchematic(t, porch(t,
		[3]string{"pins.yaml", `{gpio: 2, pad: "13"`, `{gpio: 4, pad: "13"`}))
	wantERC(t, s, "pins.yaml puts ETH_RST_N on GPIO4 at pad 13, but the kit's pad 13 (GPIO02) is GPIO2")
}

// A free pad wired with no pins.yaml entry is a GPIO the firmware never
// learns about.
func TestAFreePadWiredWithoutAPinMapEntryIsRefused(t *testing.T) {
	s := loadSchematic(t, porch(t,
		[3]string{"pins.yaml", "  - {gpio: 3, pad: \"37\", net: ETH_CS_N, dir: out, peer: W5500 SCS_N, strap: \"JTAG source, read only with the eFuse burned\"}\n", ""}))
	wantERC(t, s, "J1.37 (GPIO3) carries ETH_CS_N, which pins.yaml does not put on pad 37")
}

// The LDO's output named CORE_3V3 reads as the Core's rail on every sheet
// and in KiCad, but it never reaches the Core.
func TestANetNamedForAKitNetMustReachIt(t *testing.T) {
	s := loadSchematic(t, porch(t,
		[3]string{"sheets/ethernet.yaml", "3V3: [U1.OUT", "CORE_3V3: [U1.OUT"}))
	wantERC(t, s, "CORE_3V3 is the kit's name for J7 pad 1, but this board's CORE_3V3 never reaches it")
}

func TestAReceptacleMissingAPlugPadIsRefused(t *testing.T) {
	s := loadSchematic(t, porch(t,
		[3]string{"parts.yaml", "      - {pad: MH4, name: MH4, type: passive}\n", ""},
		[3]string{"sheets/connector.yaml", ", J1.MH4]", "]"}))
	wantERC(t, s, "FX23L-80S-0.5SV lacks pad MH4 (VBUS)")
}

func TestTwoReceptaclesAreRefused(t *testing.T) {
	s := loadSchematic(t, porch(t,
		[3]string{"sheets/connector.yaml", "  J1: {part: FX23L-80S-0.5SV}", "  J1: {part: FX23L-80S-0.5SV}\n  J2: {part: FX23L-80S-0.5SV}"}))
	wantERC(t, s, "mates one FX23L-80S-0.5SV; the sheets place 2 (J1, J2)")
}

// The module is on the Core. A second one on the main board would be a
// GPIO map nobody checks.
func TestAModuleOnAnExpansionBoardIsRefused(t *testing.T) {
	dir := porch(t,
		[3]string{"sheets/ethernet.yaml", "  U1: {part: SPX3819M5-L-3-3}", "  U1: {part: SPX3819M5-L-3-3}\n  U9: {part: ESP32-S3-WROOM-1-N16R8}"},
		[3]string{"sheets/ethernet.yaml", "nc: [U1.BP]", "nc: [U1.BP, U9.GND]"},
		[3]string{"parts.yaml", "parts:\n", `parts:
  ESP32-S3-WROOM-1-N16R8:
    symbol: RF_Module:ESP32-S3-WROOM-1
    footprint: RF_Module:ESP32-S3-WROOM-1
    pinout: the pad this fixture needs
    lcsc: C2913202
    pins:
      - {pad: "1", name: GND, type: power_in}
`})
	wantERC(t, loadSchematic(t, dir), "U9 places a ESP32-S3-WROOM-1-N16R8, but the module is on satellite1-hat.yaml")
}

func TestAKitThatContradictsItselfIsRefused(t *testing.T) {
	s := loadSchematic(t, porch(t,
		[3]string{"satellite1-hat.yaml", `{pad: "2", label: ""}`, `{pad: "2", label: "", net: GND}`},
		[3]string{"satellite1-hat.yaml", `{pad: "37", label: GPIO03, gpio: 3}`, `{pad: "37", label: GPIO03, gpio: 2}`},
		[3]string{"satellite1-hat.yaml", `use: "the HAT's input rail"`, `use: ""`}))
	wantERC(t, s,
		"satellite1-hat.yaml",
		"pad 2 is unconnected on the kit, so it carries no net",
		"GPIO2 is on both pad 13 and pad 37",
		"pad MH4 (VBUS) carries VBUS but does not say what the kit uses it for")
}

func TestAnExpansionPinNeedsItsPad(t *testing.T) {
	b := load(t, strings.Replace(porchPins, `pad: "13", `, "", 1))
	wantProblem(t, b, "ETH_RST_N on GPIO2", "needs the pad it arrives on")
}

func TestAPadOnABoardWithItsOwnModuleIsRefused(t *testing.T) {
	b := load(t, strings.Replace(porchPins, "expansion: satellite1-hat.yaml\n", "", 1))
	wantProblem(t, b, "pad 13 names a connector pad, but the board carries its own module")
}

func TestKitMirrorsReadsTheSatellite1Config(t *testing.T) {
	path := filepath.Join(porch(t), "satellite1-hat.yaml")
	k, err := LoadKit(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "satellite1.yaml")
	if err := os.WriteFile(cfg, []byte("i2c:\n  - sda: GPIO5\nsatellite1:\n  cs_pin: GPIO12\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = k.Mirrors(cfg)
	if err == nil || !strings.Contains(err.Error(), "pad 24 (SPI_CS_N) is GPIO10 here but GPIO12 at satellite1.cs_pin") {
		t.Fatalf("Mirrors = %v, want the moved select named", err)
	}
	if strings.Contains(err.Error(), "pad 3") {
		t.Errorf("the SDA pad that did not move is reported: %v", err)
	}
}

func TestConfigGPIOsFindsEveryPinAndWhereItIsSet(t *testing.T) {
	got, err := ConfigGPIOs(writeSatellite1(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[int][]string{
		7: {"i2s_audio[0].i2s_lrclk_pin"},
		8: {"i2s_audio[0].i2s_bclk_pin"},
		4: {"satellite1.xmos_rst_pin"},
		1: {"fusb302b.irq_pin"},
	}
	if len(got) != len(want) {
		t.Errorf("ConfigGPIOs = %v, want %v", got, want)
	}
	for g, paths := range want {
		if !slices.Equal(got[g], paths) {
			t.Errorf("GPIO%d at %v, want %v", g, got[g], paths)
		}
	}
}

// A part that says it is unverified is listed, and the note rides on the
// netlist so it is in front of whoever lays the board out.
func TestAnUnverifiedPartIsListedAndCarriedOnTheNetlist(t *testing.T) {
	s := loadSchematic(t, porch(t,
		[3]string{"parts.yaml", "    hand: no LCSC number confirmed\n", "    hand: no LCSC number confirmed\n    unverified: which power contact is VBUS\n"}))
	if err := s.Check(); err != nil {
		t.Fatalf("an unverified part fails the checks: %v", err)
	}
	if got := s.Unverified(); !slices.Equal(got, []string{"FX23L-80S-0.5SV: which power contact is VBUS"}) {
		t.Errorf("Unverified = %v", got)
	}
	if !bytes.Contains(s.KiCadNetlist(), []byte(`(field (name "Unverified") "which power contact is VBUS")`)) {
		t.Errorf("the netlist does not carry the note:\n%s", s.KiCadNetlist())
	}
	if len(loadSchematic(t, porch(t)).Unverified()) != 0 {
		t.Error("a board with nothing unverified lists something")
	}
}

// The receptacle is marked on the netlist as the kit's mate, which is how
// the KiCad build knows which footprint to lock under the HAT's plug.
func TestTheMateIsMarkedOnTheNetlist(t *testing.T) {
	netlist := loadSchematic(t, porch(t)).KiCadNetlist()
	want := `(field (name "Mates") "J7 of FutureProofHomes Satellite1, HAT rev 6.1 on Core rev 5.1")`
	if bytes.Count(netlist, []byte(want)) != 1 {
		t.Errorf("want one %s on the netlist:\n%s", want, netlist)
	}
}
