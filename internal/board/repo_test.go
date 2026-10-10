package board

import (
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
		"XMOS_RST_N", "XMOS_SPI_CS_N", "XMOS_SPI_SCLK", "XMOS_SPI_MOSI", "XMOS_SPI_MISO",
		"I2C_SDA", "I2C_SCL", "PD_INT_N",
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
