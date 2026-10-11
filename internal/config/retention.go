package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Days is a retention horizon as devices.yaml writes it: 30d, or 0 to keep
// forever. Set tells an absent horizon, which inherits, from an explicit 0.
type Days struct {
	N   int
	Set bool
	raw string
}

// UnmarshalYAML keeps what was written; Load checks it, so the error can
// name the satellite it belongs to.
func (d *Days) UnmarshalYAML(n *yaml.Node) error {
	*d = Days{Set: true, raw: n.Value}
	return nil
}

// parse reads 30d or 0. A bare number other than 0 is refused: 30 could
// mean days, hours or megabytes, and only one of those is meant.
func (d *Days) parse(field string) error {
	if !d.Set {
		return nil
	}
	s := strings.TrimSpace(d.raw)
	if s == "0" {
		d.N = 0
		return nil
	}
	num, ok := strings.CutSuffix(s, "d")
	n, err := strconv.Atoi(num)
	if !ok || err != nil || n < 0 || strings.HasPrefix(num, "+") {
		return fmt.Errorf("retention.%s %q is not a number of days: write 30d, or 0 to keep forever", field, d.raw)
	}
	d.N = n
	return nil
}

// Duration is the horizon as a span of time; zero keeps forever.
func (d Days) Duration() time.Duration { return time.Duration(d.N) * 24 * time.Hour }

// or is d when it was written, else the inherited horizon.
func (d Days) or(inherited Days) Days {
	if d.Set {
		return d
	}
	return inherited
}

// Retention is how long a satellite's audio and logs are kept (SPEC §8).
// Absent, or 0, keeps forever, which is the default.
type Retention struct {
	// Audio is how long the PCM a log names is kept after it was recorded.
	Audio Days `yaml:"audio"`
	// Journal is how long a conversation is kept after its last event, and
	// a long-lived log's events after each was recorded.
	Journal Days `yaml:"journal"`
}

// HouseRetention is the house-wide default, and the switch only the house
// has: whether what a reviewer curated may be pruned too.
type HouseRetention struct {
	Retention `yaml:",inline"`

	// PruneCurated lets retention remove what a reviewer kept for the
	// dataset. Off by default: a curated pair outlives every horizon.
	PruneCurated bool `yaml:"prune_curated"`
}

// parse reads both horizons as written.
func (r *Retention) parse() error {
	if err := r.Audio.parse("audio"); err != nil {
		return err
	}
	return r.Journal.parse("journal")
}

// check refuses audio kept longer than the log that names it, which would
// go first and take the audio with it.
func (r Retention) check() error {
	if a, j := r.Audio.N, r.Journal.N; a > 0 && j > 0 && a > j {
		return fmt.Errorf("retention.audio %dd outlives retention.journal %dd: the audio would go with the log that names it first", a, j)
	}
	return nil
}

// RetentionFor is the satellite's horizons, each falling back to the
// house's when the satellite does not set it.
func (c *Config) RetentionFor(name string) Retention {
	r := c.Retention.Retention
	for _, s := range c.Satellites {
		if s.Name == name && s.Retention != nil {
			return Retention{Audio: s.Retention.Audio.or(r.Audio), Journal: s.Retention.Journal.or(r.Journal)}
		}
	}
	return r
}
