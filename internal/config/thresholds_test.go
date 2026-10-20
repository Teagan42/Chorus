package config_test

import (
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/config"
)

// A satellite may name its own speaker accept level and click ceiling;
// absent, each is zero, which the daemon reads as the house's (ADR-0098).
func TestASatellitesOwnThresholds(t *testing.T) {
	c, err := config.Load(writeInventory(t, "    speaker_accept: 0.55\n    max_crest: 26\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s := c.Satellites[0]; s.SpeakerAccept != 0.55 || s.MaxCrest != 26 {
		t.Errorf("got speaker_accept %v and max_crest %v, want 0.55 and 26", s.SpeakerAccept, s.MaxCrest)
	}
	c, err = config.Load(writeInventory(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if s := c.Satellites[0]; s.SpeakerAccept != 0 || s.MaxCrest != 0 {
		t.Errorf("absent gave speaker_accept %v and max_crest %v, want zeros", s.SpeakerAccept, s.MaxCrest)
	}
}

// A level that accepts everybody or nobody, or a ceiling no audio has, is
// refused at load, naming the field, rather than met at the first word.
func TestThresholdsOutOfRangeAreRefused(t *testing.T) {
	for _, line := range []string{
		"speaker_accept: 1", "speaker_accept: 1.2", "speaker_accept: -0.1",
		"max_crest: -1", "max_crest: 2", "max_crest: 61",
	} {
		_, err := config.Load(writeInventory(t, "    "+line+"\n"))
		field := strings.SplitN(line, ":", 2)[0]
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("%s: err = %v, want it refused by name", line, err)
		}
	}
}
