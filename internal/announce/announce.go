// Package announce says things on satellites nobody woke: the announce tool,
// and the seam a timer going off is said through (SPEC §4, ADR-0045).
package announce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/teaganglenn/chorus/internal/config"
	"github.com/teaganglenn/chorus/internal/session"
)

// ErrNotConnected is a satellite with no live link to say anything on.
var ErrNotConnected = errors.New("not connected")

// Announcer says an announcement on one satellite, and returns the
// conversation it was said in. The daemon fills it with each link's
// Listening child.
type Announcer interface {
	Announce(ctx context.Context, satellite string, a session.Announcement) (string, error)
}

// Tool is the announce executor. Rooms come from the inventory: one nothing
// is in is refused with the rooms that exist, for the model to retry (SPEC §7).
func Tool(to Announcer, sats []config.Satellite) session.Tool {
	return session.ToolFunc(func(ctx context.Context, args string) (string, error) {
		var in struct {
			Text              string `json:"text"`
			Room              string `json:"room"`
			StartConversation bool   `json:"start_conversation"`
		}
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return "", fmt.Errorf("bad arguments: %w", err)
		}
		in.Text = strings.TrimSpace(in.Text)
		if in.Text == "" {
			return "", errors.New("there is nothing to announce")
		}
		caller, _ := session.CallerFrom(ctx)
		targets, err := where(sats, in.Room, caller.Satellite)
		if err != nil {
			return "", err
		}
		out := struct {
			AnnouncedIn  []string `json:"announced_in"`
			NotConnected []string `json:"not_connected,omitempty"`
		}{AnnouncedIn: []string{}}
		var failed []string
		for _, sat := range targets {
			_, err := to.Announce(ctx, sat, session.Announcement{
				Text: in.Text, Source: session.SourceRequest, RequestedBy: caller.Person,
				FromSatellite: caller.Satellite, FromConversation: caller.ConversationID,
				StartConversation: in.StartConversation,
			})
			switch {
			case errors.Is(err, ErrNotConnected):
				out.NotConnected = append(out.NotConnected, sat)
			case err != nil:
				failed = append(failed, fmt.Sprintf("%s: %v", sat, err))
			default:
				out.AnnouncedIn = append(out.AnnouncedIn, sat)
			}
		}
		if len(out.AnnouncedIn) == 0 {
			if len(failed) > 0 {
				return "", fmt.Errorf("announced nowhere: %s", strings.Join(failed, "; "))
			}
			return "", fmt.Errorf("announced nowhere: %s not connected", strings.Join(out.NotConnected, ", "))
		}
		// Strings: marshalling cannot fail.
		b, _ := json.Marshal(out)
		return string(b), nil
	})
}

// where names the satellites a room means. Empty is every room but the
// caller's, as a broadcast is. A room matches a satellite's room or name.
func where(sats []config.Satellite, room, from string) ([]string, error) {
	room = strings.TrimSpace(room)
	var out, rooms []string
	for _, s := range sats {
		r := s.Room
		if r == "" {
			r = s.Name
		}
		if !slices.Contains(rooms, r) {
			rooms = append(rooms, r)
		}
		switch {
		case room == "" && s.Name != from:
			out = append(out, s.Name)
		case room != "" && (strings.EqualFold(room, s.Room) || strings.EqualFold(room, s.Name)):
			out = append(out, s.Name)
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	if room == "" {
		return nil, errors.New("there is no other room to announce in")
	}
	slices.Sort(rooms)
	return nil, fmt.Errorf("no satellite is in a room called %q; the rooms are %s", room, strings.Join(rooms, ", "))
}
