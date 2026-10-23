package config_test

import (
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/config"
)

// The kitchen's echo threshold is its own: a front end that cancels less
// leaves more of the device's voice to match, and the score that separates
// it from a person is the room's to tune (ADR-0096).
//
// verifies SPEC §13
func TestASatellitesOwnEchoThreshold(t *testing.T) {
	c, err := config.Load(writeInventory(t, "    echo_threshold: 0.7\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Satellites[0].EchoThreshold; got != 0.7 {
		t.Errorf("echo_threshold is %v, want 0.7", got)
	}
}

// Absent is zero, the built-in; above 1 nothing scores, which is the stage
// turned off, and is allowed.
//
// verifies SPEC §13
func TestEchoThresholdAbsentOrOffIsAccepted(t *testing.T) {
	for body, want := range map[string]float64{"": 0, "    echo_threshold: 1.5\n": 1.5} {
		c, err := config.Load(writeInventory(t, body))
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if got := c.Satellites[0].EchoThreshold; got != want {
			t.Errorf("%q: echo_threshold is %v, want %v", body, got, want)
		}
	}
}

// A negative score is no threshold at all, refused at startup rather than
// read as the default.
//
// verifies SPEC §13
func TestANegativeEchoThresholdIsRefused(t *testing.T) {
	_, err := config.Load(writeInventory(t, "    echo_threshold: -0.2\n"))
	if err == nil || !strings.Contains(err.Error(), "echo_threshold") {
		t.Errorf("echo_threshold -0.2: err = %v, want one naming the field", err)
	}
}
