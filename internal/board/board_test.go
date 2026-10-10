package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePins(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pins.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// kitchen is the rev A board as far as these tests need it: the XMOS bus,
// the mute switch, and the Ethernet chip on its own SPI host.
const kitchen = `board: chorus-sat
revision: A
module: ESP32-S3-WROOM-1-N16R8
pins:
  - {gpio: 7, net: I2S_LRCLK, dir: in, peer: XU316 word clock}
  - {gpio: 8, net: I2S_BCLK, dir: in, peer: XU316 bit clock}
  - {gpio: 15, net: I2S_DIN, dir: in, peer: XU316 echo-cancelled mix}
  - {gpio: 9, net: I2S_DOUT, dir: out, peer: XU316 playback}
  - {gpio: 2, net: MUTE_SENSE, dir: in, peer: hardware mute switch}
  - {gpio: 38, net: ETH_CS_N, dir: out, peer: W5500}
`

func load(t *testing.T, body string) Board {
	t.Helper()
	b, err := Load(writePins(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return b
}

func wantProblem(t *testing.T, b Board, fragments ...string) {
	t.Helper()
	err := b.Check()
	if err == nil {
		t.Fatalf("Check passed; want a problem naming %q", fragments)
	}
	for _, f := range fragments {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("problem %q does not name %q", err, f)
		}
	}
}

func TestKitchenBoardChecksClean(t *testing.T) {
	b := load(t, kitchen)
	if err := b.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got := b.Net("MUTE_SENSE"); got == nil || got.GPIO != 2 {
		t.Errorf("Net(MUTE_SENSE) = %+v, want GPIO2", got)
	}
	if b.Net("SPEAKER_ENABLE") != nil {
		t.Error("Net found a net the board does not have")
	}
}

// The LED ring moved onto GPIO35 looks free on the module's pinout and is
// wired to the octal PSRAM inside the can.
func TestLEDRingOnAPSRAMPinIsRefused(t *testing.T) {
	b := load(t, kitchen+"  - {gpio: 35, net: LED_DATA, dir: out, peer: SK6812 ring}\n")
	wantProblem(t, b, "LED_DATA", "GPIO35", "PSRAM")
}

// A second SPI device on the XMOS chip-select would put the W5500 on the
// bus whose stall silences all audio (SPEC §3.3.2).
func TestEthernetSharingAPinWithTheXMOSIsRefused(t *testing.T) {
	b := load(t, kitchen+"  - {gpio: 15, net: ETH_MISO, dir: in, peer: W5500}\n")
	wantProblem(t, b, "GPIO15", "I2S_DIN", "ETH_MISO")
}

func TestOneNetOnTwoPinsIsRefused(t *testing.T) {
	b := load(t, kitchen+"  - {gpio: 47, net: MUTE_SENSE, dir: in, peer: second mute switch}\n")
	wantProblem(t, b, "MUTE_SENSE", "GPIO2", "GPIO47")
}

// GPIO46 decides whether the ROM prints its boot log; a button pulling it
// low at power-on changes boot behaviour, so the map has to say why it is safe.
func TestStrappingPinWithoutAReasonIsRefused(t *testing.T) {
	b := load(t, kitchen+"  - {gpio: 46, net: BTN_VOL_UP, dir: in, peer: volume-up button}\n")
	wantProblem(t, b, "BTN_VOL_UP", "GPIO46", "strap")
}

func TestStrappingPinWithAReasonPasses(t *testing.T) {
	b := load(t, kitchen+`  - gpio: 0
    net: BTN_ACTION
    dir: in
    peer: action button
    strap: pulled up; held at power-on it enters download mode, which is wanted
`)
	if err := b.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

// GPIO45 picks the flash supply voltage: no reason makes a peripheral there safe.
func TestFlashVoltageStrapIsRefusedEvenWithAReason(t *testing.T) {
	b := load(t, kitchen+"  - {gpio: 45, net: RADAR_TX, dir: out, peer: HLK-LD2450, strap: it idles high}\n")
	wantProblem(t, b, "RADAR_TX", "GPIO45", "flash")
}

func TestNativeUSBPinsAreKeptForFlashingAndLogs(t *testing.T) {
	b := load(t, kitchen+"  - {gpio: 19, net: BTN_VOL_DOWN, dir: in, peer: volume-down button}\n")
	wantProblem(t, b, "BTN_VOL_DOWN", "GPIO19", "USB")
}

func TestAGPIOTheChipDoesNotHaveIsRefused(t *testing.T) {
	b := load(t, kitchen+"  - {gpio: 23, net: PWR_SRC, dir: in, peer: TPS2121 status}\n")
	wantProblem(t, b, "PWR_SRC", "GPIO23", "no such")
}

func TestUnknownDirectionIsRefused(t *testing.T) {
	b := load(t, kitchen+"  - {gpio: 17, net: AMP_SENSE, dir: input, peer: TAS2780 SDOUT}\n")
	wantProblem(t, b, "AMP_SENSE", "input")
}

func TestNetWithoutAPeerIsRefused(t *testing.T) {
	b := load(t, kitchen+"  - {gpio: 17, net: AMP_SENSE, dir: in}\n")
	wantProblem(t, b, "AMP_SENSE", "peer")
}

// Net names become KiCad labels verbatim; a lower-case or spaced one splits
// a net in two the moment someone retypes it.
func TestNetNamesAreSchematicLabels(t *testing.T) {
	b := load(t, kitchen+"  - {gpio: 17, net: amp sense, dir: in, peer: TAS2780 SDOUT}\n")
	wantProblem(t, b, "amp sense", "label")
}

func TestEveryProblemIsReportedAtOnce(t *testing.T) {
	b := load(t, kitchen+
		"  - {gpio: 35, net: LED_DATA, dir: out, peer: SK6812 ring}\n"+
		"  - {gpio: 46, net: BTN_VOL_UP, dir: in, peer: volume-up button}\n")
	err := b.Check()
	if err == nil {
		t.Fatal("Check passed")
	}
	for _, want := range []string{"LED_DATA", "BTN_VOL_UP"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q missing from %v", want, err)
		}
	}
}

func TestLoadNamesTheFileItCouldNotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.yaml")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("Load(missing) = %v, want an error naming %s", err, path)
	}
}

func TestLoadRejectsAFieldTheMapDoesNotDefine(t *testing.T) {
	_, err := Load(writePins(t, kitchen+"  - {gpio: 17, net: AMP_SENSE, dir: in, peer: TAS2780, pull: up}\n"))
	if err == nil || !strings.Contains(err.Error(), "pull") {
		t.Errorf("Load = %v, want the unknown field named", err)
	}
}

func TestLoadRejectsAModuleItDoesNotKnow(t *testing.T) {
	_, err := Load(writePins(t, strings.Replace(kitchen, "N16R8", "N8", 1)))
	if err == nil || !strings.Contains(err.Error(), "ESP32-S3-WROOM-1-N8") {
		t.Errorf("Load = %v, want the module named", err)
	}
}
