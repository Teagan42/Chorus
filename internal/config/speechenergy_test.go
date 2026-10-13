package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/config"
)

// write lays out an inventory with the given satellite body.
func writeInventory(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "devices.yaml")
	doc := "satellites:\n  - name: sat-abcdef\n    address: 10.0.0.42:6053\n" +
		"    psk: AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=\n" + body
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A satellite whose front end rides its own gain needs its own speech gate:
// the XMOS AGC lifts an empty room above the default -40 dBFS, so nothing
// ever counts as quiet and no turn ends (SPEC §4.5).
//
// verifies SPEC §13
func TestASatellitesOwnSpeechEnergy(t *testing.T) {
	c, err := config.Load(writeInventory(t, "    speech_energy: 0.05\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Satellites[0].SpeechEnergy; got != 0.05 {
		t.Errorf("speech_energy is %v, want 0.05", got)
	}
}

// Absent, it is zero, which the daemon reads as the built-in default rather
// than as a gate nothing passes.
//
// verifies SPEC §13
func TestSpeechEnergyAbsentIsTheDefault(t *testing.T) {
	c, err := config.Load(writeInventory(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Satellites[0].SpeechEnergy; got != 0 {
		t.Errorf("speech_energy is %v, want 0 for absent", got)
	}
}

// A gate at or above full scale hears nothing, and a negative one hears
// everything; both are refused at startup rather than at the first word.
//
// verifies SPEC §13
func TestSpeechEnergyOutOfRangeIsRefused(t *testing.T) {
	for _, v := range []string{"1", "1.5", "-0.1"} {
		if _, err := config.Load(writeInventory(t, "    speech_energy: "+v+"\n")); err == nil {
			t.Errorf("speech_energy %s was accepted", v)
		} else if !strings.Contains(err.Error(), "speech_energy") {
			t.Errorf("speech_energy %s: error does not name the field: %v", v, err)
		}
	}
}
