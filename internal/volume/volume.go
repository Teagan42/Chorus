// Package volume sets how loud a satellite's own speaker is: the set_volume
// tool, over whatever reaches each satellite's volume control (ADR-0067).
package volume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/session"
)

// The reasons a satellite's volume cannot be read or set. Each is also the
// whole error the model sees when it is the only reason (SPEC §7).
var (
	// ErrNotImplemented is a satellite whose firmware lists no volume control.
	ErrNotImplemented = errors.New("not_implemented")
	// ErrNotConnected is a satellite whose control connection is down, so
	// what it has is not known.
	ErrNotConnected = errors.New("not_connected")
	// ErrLevelUnknown is a volume control that has not reported its level.
	ErrLevelUnknown = errors.New("level_unknown")
)

// Speakers reads and sets each satellite's volume, 0 to 100. The daemon fills
// it with each satellite's native API connection.
type Speakers interface {
	Level(satellite string) (int, error)
	// SetLevel sends level and returns the level the device reported back,
	// confirmed, or the level sent, unconfirmed, when no report came in time.
	SetLevel(ctx context.Context, satellite string, level int) (got int, confirmed bool, err error)
}

type setLevel struct {
	Satellite string `json:"satellite"`
	Room      string `json:"room"`
	Level     int    `json:"level"`
	Was       *int   `json:"was,omitempty"`
	// Unconfirmed is a level the device did not report back: the one sent.
	Unconfirmed bool `json:"unconfirmed,omitempty"`
}

type unavailable struct {
	Satellite string `json:"satellite"`
	Reason    string `json:"reason"`
}

type result struct {
	Set         []setLevel    `json:"set"`
	Unavailable []unavailable `json:"unavailable,omitempty"`
}

// Tool is the set_volume executor. A room means every satellite in it; none
// means the satellite the call was made on (SPEC §5).
func Tool(to Speakers, sats []config.Satellite) session.Tool {
	return session.ToolFunc(func(ctx context.Context, args string) (string, error) {
		var in struct {
			Level *float64 `json:"level"`
			Step  *float64 `json:"step"`
			Room  string   `json:"room"`
		}
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return "", fmt.Errorf("bad arguments: %w", err)
		}
		switch {
		case in.Level == nil && in.Step == nil:
			return "", errors.New("bad arguments: give level or step")
		case in.Level != nil && in.Step != nil:
			return "", errors.New("bad arguments: give level or step, not both")
		case in.Level != nil && (*in.Level < 0 || *in.Level > 100):
			return "", fmt.Errorf("bad arguments: level %v is not 0 to 100", *in.Level)
		}
		caller, _ := session.CallerFrom(ctx)
		targets, err := where(sats, in.Room, caller.Satellite)
		if err != nil {
			return "", err
		}
		out := result{Set: []setLevel{}}
		for _, sat := range targets {
			set, err := apply(ctx, to, sat, in.Level, in.Step)
			if err != nil {
				out.Unavailable = append(out.Unavailable, unavailable{Satellite: sat.Name, Reason: err.Error()})
				continue
			}
			out.Set = append(out.Set, set)
		}
		if len(out.Set) == 0 {
			return "", failed(out.Unavailable)
		}
		// Strings and ints: marshalling cannot fail.
		b, _ := json.Marshal(out)
		return string(b), nil
	})
}

// apply sets one satellite to level, or steps it from where it is.
func apply(ctx context.Context, to Speakers, sat config.Satellite, level, step *float64) (setLevel, error) {
	was, err := to.Level(sat.Name)
	known := err == nil
	switch {
	case errors.Is(err, ErrLevelUnknown) && step == nil:
		// An exact level needs no starting point.
	case err != nil:
		return setLevel{}, err
	}
	var next int
	if step != nil {
		next = min(100, max(0, was+int(math.Round(*step))))
	} else {
		next = int(math.Round(*level))
	}
	got, confirmed, err := to.SetLevel(ctx, sat.Name, next)
	if err != nil {
		return setLevel{}, err
	}
	out := setLevel{Satellite: sat.Name, Room: roomOf(sat), Level: got, Unconfirmed: !confirmed}
	if known {
		out.Was = &was
	}
	return out, nil
}

// failed is the error for a call that set nothing: the reason alone when
// every satellite gave the same one, so a lone Voice PE says not_implemented.
func failed(why []unavailable) error {
	reasons := make([]string, 0, len(why))
	same := true
	for _, u := range why {
		reasons = append(reasons, u.Satellite+": "+u.Reason)
		same = same && u.Reason == why[0].Reason
	}
	if same {
		return errors.New(why[0].Reason)
	}
	return errors.New(strings.Join(reasons, "; "))
}

// where names the satellites a room means: every one whose room or name it
// is, or with none named, the one the call was made on.
func where(sats []config.Satellite, room, from string) ([]config.Satellite, error) {
	room = strings.TrimSpace(room)
	var out []config.Satellite
	var rooms []string
	for _, s := range sats {
		if r := roomOf(s); !slices.Contains(rooms, r) {
			rooms = append(rooms, r)
		}
		switch {
		case room == "" && s.Name == from:
			out = append(out, s)
		case room != "" && (strings.EqualFold(room, s.Room) || strings.EqualFold(room, s.Name)):
			out = append(out, s)
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	if room == "" {
		return nil, errors.New("bad arguments: name a room; this call was made on no satellite")
	}
	slices.Sort(rooms)
	return nil, fmt.Errorf("bad arguments: no satellite is in a room called %q; the rooms are %s", room, strings.Join(rooms, ", "))
}

// roomOf is a satellite's room, or its name when the inventory gives none.
func roomOf(s config.Satellite) string {
	if s.Room == "" {
		return s.Name
	}
	return s.Room
}
