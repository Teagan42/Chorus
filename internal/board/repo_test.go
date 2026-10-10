package board

import (
	"slices"
	"testing"
)

// The checked-in files, not fixtures: the board the design doc describes and
// the Satellite1 config that already runs Chorus in the living room.
const (
	repoPins       = "../../hardware/chorus-sat/pins.yaml"
	repoSatellite1 = "../../esphome/satellite1.yaml"
)

func TestTheRevABoardChecksClean(t *testing.T) {
	b, err := Load(repoPins)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Check(); err != nil {
		t.Fatalf("hardware/chorus-sat/pins.yaml: %v", err)
	}
}

// verifies SPEC §3.3.2
// The XU316 masters the I2S bus and answers over SPI before any audio flows;
// keeping its wiring where Satellite1 has it reuses that proven bring-up.
func TestTheXMOSInterfaceIsWhereSatellite1HasIt(t *testing.T) {
	b, err := Load(repoPins)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Mirrors(repoSatellite1); err != nil {
		t.Fatal(err)
	}
	// Every net the XMOS firmware or FutureProofHomes' components drive must
	// be mirrored, not just the ones somebody remembered to annotate.
	for _, net := range []string{
		"I2S_LRCLK", "I2S_BCLK", "I2S_MCLK", "I2S_DOUT", "I2S_DIN",
		"XMOS_RST", "XMOS_SPI_CS_N", "XMOS_SPI_SCLK", "XMOS_SPI_MOSI", "XMOS_SPI_MISO",
		"I2C_SDA", "I2C_SCL", "I2C_IRQ",
	} {
		p := b.Net(net)
		if p == nil {
			t.Errorf("%s is not on the board", net)
			continue
		}
		if p.Satellite1 == "" {
			t.Errorf("%s on GPIO%d does not say where satellite1.yaml drives it", net, p.GPIO)
		}
	}
}

// verifies SPEC §3.2.1
// The truncation point is read from the DAC path, and the amplifier's sense
// output is what proves it on the bench; both have to reach the ESP32.
func TestThePlaybackPathAndItsSenseReachTheESP32(t *testing.T) {
	b, err := Load(repoPins)
	if err != nil {
		t.Fatal(err)
	}
	for _, net := range []string{"I2S_DOUT", "AMP_SENSE", "MUTE_SENSE", "ETH_CS_N"} {
		if b.Net(net) == nil {
			t.Errorf("%s is not on the board", net)
		}
	}
}

// verifies SPEC §3.3.2
// The XU316 masters every audio clock, its MCLK included; the ESP32 driving
// GPIO16 too would put two outputs on one net. Its reset is active high
// through an N-FET, so a pin named for an active-low RST_N is miswired.
func TestTheXU316OwnsItsClocksAndTheESP32HoldsItInReset(t *testing.T) {
	b, err := Load(repoPins)
	if err != nil {
		t.Fatal(err)
	}
	for _, net := range []string{"I2S_MCLK", "I2S_BCLK", "I2S_LRCLK"} {
		if p := b.Net(net); p == nil || p.Dir != "in" {
			t.Errorf("%s = %+v, want an input: the XU316 drives it", net, p)
		}
	}
	if b.Net("XMOS_RST_N") != nil {
		t.Error("XMOS_RST_N names the XU316 pin; the ESP32 drives XMOS_RST, the inverter's gate")
	}
}

// verifies SPEC §3.3.2
// The captured board passes its ERC and wires the ESP32 module exactly as
// pins.yaml says, pad by pad, so the netlist Pcbnew imports is the board
// the pin map and the ESPHome config describe.
func TestTheRevASchematicChecksClean(t *testing.T) {
	s, err := LoadSchematic("../../hardware/chorus-sat")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(); err != nil {
		t.Fatalf("hardware/chorus-sat:\n%v", err)
	}
}

// What still stands between rev A and a layout. Drawing one of these into
// chorus-sat.pretty takes it off this list; the check above then holds it
// to its part's pads.
func TestTheRevAFootprintsStillToDraw(t *testing.T) {
	s, err := LoadSchematic("../../hardware/chorus-sat")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"chorus-sat:CUI_CMM-4030DT",
		"chorus-sat:Silvertel_Ag9912-MTB",
		"chorus-sat:Texas_RYA0030A_VQFN-HR-30",
		"chorus-sat:XMOS_QFN-60_7x7mm_P0.4mm_VDD-Bars",
	}
	if got := s.Undrawn(); !slices.Equal(got, want) {
		t.Errorf("undrawn footprints = %q, want %q", got, want)
	}
}
