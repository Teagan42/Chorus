package main

import (
	"os"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/teagan42/chorus/internal/session"
)

// The checked-in firmware every Satellite1 in the house runs, the kit on the
// Chorus main board included: it builds on this file.
const satellite1YAML = "../../esphome/satellite1.yaml"

// ringEffects is every effect the LED ring in path declares.
func ringEffects(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Light []struct {
			ID      string                             `yaml:"id"`
			Effects []map[string]struct{ Name string } `yaml:"effects"`
		} `yaml:"light"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var names []string
	for _, l := range doc.Light {
		if l.ID != "led_ring" {
			continue
		}
		for _, e := range l.Effects {
			for _, v := range e {
				names = append(names, v.Name)
			}
		}
	}
	return names
}

// The effect names are the contract between chorusd and the firmware
// (ADR-0056): the Satellite1's ring has one for every state chorusd shows,
// so none of them leaves the kitchen's ring dark.
//
// verifies SPEC §3.3.1
func TestTheSatellite1RingHasAnEffectForEveryState(t *testing.T) {
	effects := ringEffects(t, satellite1YAML)
	for _, st := range []session.RingState{
		session.RingListening, session.RingThinking, session.RingSpeaking, session.RingMuted, session.RingError,
	} {
		if !slices.Contains(effects, ringEffect(st)) {
			t.Errorf("the ring in %s has no %q effect for %s; it has %v", satellite1YAML, ringEffect(st), st, effects)
		}
	}
}
