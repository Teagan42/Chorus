package config

import (
	"strings"
	"testing"
	"time"
)

const day = 24 * time.Hour

// The house keeps a year of conversations; the kitchen keeps 30 days of its
// audio and inherits the year; the office sets nothing and keeps the house's.
//
// verifies SPEC §8
func TestRetentionIsPerSatelliteFallingBackToTheHouse(t *testing.T) {
	c, err := Load(writeConfig(t, `retention:
  journal: 365d
satellites:
  - name: kitchen
    address: 10.0.12.117:6053
    psk: `+goodPSK+`
    retention:
      audio: 30d
  - name: office
    address: 10.0.12.118:6053
    psk: `+goodPSK+`
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	kitchen := c.RetentionFor("kitchen")
	if kitchen.Audio.Duration() != 30*day || kitchen.Journal.Duration() != 365*day {
		t.Errorf("kitchen = %+v, want 30 days of audio and the house's year", kitchen)
	}
	office := c.RetentionFor("office")
	if office.Audio.Duration() != 0 || office.Journal.Duration() != 365*day {
		t.Errorf("office = %+v, want audio forever and the house's year", office)
	}
	if c.Retention.PruneCurated {
		t.Error("prune_curated is on by default")
	}
}

// An inventory written before retention existed keeps everything, as it
// always did.
//
// verifies SPEC §8
func TestNoRetentionKeepsEverythingForever(t *testing.T) {
	c, err := Load(writeConfig(t, `satellites:
  - name: kitchen
    address: 10.0.12.117:6053
    psk: `+goodPSK+`
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r := c.RetentionFor("kitchen"); r.Audio.Duration() != 0 || r.Journal.Duration() != 0 {
		t.Errorf("kitchen = %+v, want everything kept forever", r)
	}
}

// A satellite's explicit 0 keeps forever even when the house would prune:
// the nursery's recordings are the household's to keep.
//
// verifies SPEC §8
func TestASatellitesZeroOverridesTheHouse(t *testing.T) {
	c, err := Load(writeConfig(t, `retention:
  audio: 30d
satellites:
  - name: nursery
    address: 10.0.12.119:6053
    psk: `+goodPSK+`
    retention:
      audio: 0
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.RetentionFor("nursery").Audio; got.Duration() != 0 || !got.Set {
		t.Errorf("nursery audio = %+v, want an explicit forever", got)
	}
}

// A horizon that is not a number of days fails the load, naming the
// satellite and showing how to write one.
//
// verifies SPEC §8, §13
func TestABadRetentionFailsTheLoadWithAUsableError(t *testing.T) {
	for _, tc := range []struct{ name, block, want string }{
		{"bare number", "audio: 30", `"30" is not a number of days: write 30d, or 0 to keep forever`},
		{"hours", "audio: 72h", `"72h" is not a number of days`},
		{"negative", "journal: -5d", `"-5d" is not a number of days`},
		{"audio outlives the log", "audio: 90d\n      journal: 30d", "retention.audio 90d outlives retention.journal 30d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, `satellites:
  - name: kitchen
    address: 10.0.12.117:6053
    psk: `+goodPSK+`
    retention:
      `+tc.block+`
`))
			if err == nil {
				t.Fatal("Load accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "kitchen") {
				t.Errorf("err = %v; want %q naming the kitchen", err, tc.want)
			}
		})
	}
}

// The house-wide default is checked the same way.
//
// verifies SPEC §8, §13
func TestABadHouseRetentionFailsTheLoad(t *testing.T) {
	_, err := Load(writeConfig(t, `retention:
  journal: a year
satellites:
  - name: kitchen
    address: 10.0.12.117:6053
    psk: `+goodPSK+`
`))
	if err == nil || !strings.Contains(err.Error(), `"a year" is not a number of days`) {
		t.Errorf("err = %v; want the house's horizon refused", err)
	}
}

// The example inventory carries a retention block that loads.
//
// verifies SPEC §8
func TestTheExampleRetentionParses(t *testing.T) {
	c, err := Load(writeConfig(t, `retention:
  audio: 30d
  journal: 365d
  prune_curated: false
satellites:
  - name: kitchen
    address: 10.0.12.117:6053
    psk: `+goodPSK+`
    retention:
      audio: 7d
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.RetentionFor("kitchen").Audio.Duration(); got != 7*day {
		t.Errorf("kitchen audio = %v, want 7 days", got)
	}
}
