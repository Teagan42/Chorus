package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const satellite1YAML = `i2s_audio:
  - id: i2s_shared
    i2s_lrclk_pin: GPIO07
    i2s_bclk_pin: GPIO08
wifi:
  ssid: !secret wifi_ssid
satellite1:
  xmos_rst_pin: GPIO4
fusb302b:
  irq_pin: 1
`

func writeSatellite1(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "satellite1.yaml")
	if err := os.WriteFile(path, []byte(satellite1YAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLookupGPIOReadsListsMapsAndBothSpellings(t *testing.T) {
	path := writeSatellite1(t)
	for want, at := range map[int]string{
		7: "i2s_audio[0].i2s_lrclk_pin",
		8: "i2s_audio[0].i2s_bclk_pin",
		4: "satellite1.xmos_rst_pin",
		1: "fusb302b.irq_pin",
	} {
		got, err := LookupGPIO(path, at)
		if err != nil {
			t.Errorf("LookupGPIO(%s): %v", at, err)
			continue
		}
		if got != want {
			t.Errorf("LookupGPIO(%s) = %d, want %d", at, got, want)
		}
	}
}

func TestLookupGPIONamesAMissingKey(t *testing.T) {
	_, err := LookupGPIO(writeSatellite1(t), "i2s_audio[0].i2s_mclk_pin")
	if err == nil || !strings.Contains(err.Error(), "i2s_mclk_pin") {
		t.Errorf("err = %v, want the missing key named", err)
	}
}

func TestLookupGPIONamesAnIndexPastTheList(t *testing.T) {
	_, err := LookupGPIO(writeSatellite1(t), "i2s_audio[1].i2s_lrclk_pin")
	if err == nil || !strings.Contains(err.Error(), "i2s_audio[1]") {
		t.Errorf("err = %v, want the index named", err)
	}
}

func TestLookupGPIORefusesAValueThatIsNotAPin(t *testing.T) {
	_, err := LookupGPIO(writeSatellite1(t), "i2s_audio[0].id")
	if err == nil || !strings.Contains(err.Error(), "i2s_shared") {
		t.Errorf("err = %v, want the value named", err)
	}
}

// A pin map whose XMOS clock drifted from the Satellite1 wiring is the board
// on which the stock XMOS firmware drives a pin nobody listens to.
func TestMirrorFindsAPinThatMovedOffTheSatellite1Wiring(t *testing.T) {
	b := load(t, `board: chorus-sat
revision: A
module: ESP32-S3-WROOM-1-N16R8
pins:
  - {gpio: 7, net: I2S_LRCLK, dir: in, peer: XU316, satellite1: "i2s_audio[0].i2s_lrclk_pin"}
  - {gpio: 18, net: I2S_BCLK, dir: in, peer: XU316, satellite1: "i2s_audio[0].i2s_bclk_pin"}
`)
	err := b.Mirrors(writeSatellite1(t))
	if err == nil {
		t.Fatal("Mirrors passed with I2S_BCLK on GPIO18")
	}
	for _, want := range []string{"I2S_BCLK", "GPIO18", "GPIO8"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q missing from %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "I2S_LRCLK") {
		t.Errorf("the clock that did not move is reported: %v", err)
	}
}
