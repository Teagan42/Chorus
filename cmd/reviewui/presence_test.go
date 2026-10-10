package main

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
)

// thursday is midnight of the day the Browse tests look at, in UTC.
var thursday = time.Date(2025, 10, 9, 0, 0, 0, 0, time.UTC)

func mark(h, m int, state string) presenceMark {
	return presenceMark{thursday.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute), state}
}

// A span opens when the radar sees someone and closes on anything else:
// leaving, or the native API dropping, which is presence nobody can vouch
// for. A repeat while present changes nothing.
//
// verifies SPEC §3.3.1
func TestPresenceSpansOpenOnPresentAndCloseOnAnythingElse(t *testing.T) {
	next := thursday.AddDate(0, 0, 1)
	evening := thursday.Add(22 * time.Hour)
	for _, tc := range []struct {
		name  string
		marks []presenceMark
		now   time.Time
		want  [][2]float64
	}{
		{
			name: "breakfast, then the radio dropped at lunch",
			marks: []presenceMark{
				mark(6, 45, "present"), mark(7, 30, "present"), mark(7, 45, "absent"),
				mark(12, 0, "present"), mark(12, 30, "unknown"),
			},
			now:  evening,
			want: [][2]float64{{6.75, 7.75}, {12, 12.5}},
		},
		{
			name:  "still in the living room: the span runs to now",
			marks: []presenceMark{mark(19, 0, "present")},
			now:   evening,
			want:  [][2]float64{{19, 22}},
		},
		{
			name:  "in the bedroom since last night: clipped to the day",
			marks: []presenceMark{mark(-1, 30, "present"), mark(6, 30, "absent")},
			now:   evening,
			want:  [][2]float64{{0, 6.5}},
		},
		{
			name:  "a past day with someone still there at midnight runs to its end",
			marks: []presenceMark{mark(23, 0, "present")},
			now:   next.Add(9 * time.Hour),
			want:  [][2]float64{{23, 24}},
		},
		{
			name:  "absent or unknown alone opens nothing",
			marks: []presenceMark{mark(8, 0, "absent"), mark(9, 0, "unknown")},
			now:   evening,
		},
	} {
		got := presenceSpans(tc.marks, thursday, next, tc.now)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: spans = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// withPresence writes the kitchen radar's morning to its device log, where
// chorusd journals it (listen.DeviceConversation).
func withPresence(t *testing.T, store *journal.MemStore) *journal.MemStore {
	t.Helper()
	ctx := context.Background()
	for _, m := range []presenceMark{mark(7, 0, "present"), mark(7, 40, "absent"), mark(11, 0, "present")} {
		j := journal.New(store, journal.FixedClock(m.at), journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"})
		r := journal.Record{Kind: journal.KindPresenceChanged, Fields: map[string]string{"state": m.state, "sensor": "room_presence"}}
		if _, err := j.Append(ctx, "device:kitchen", r); err != nil {
			t.Fatalf("append presence: %v", err)
		}
	}
	return store
}

// Browse draws the kitchen's presence on its lane from the device log: the
// breakfast span, and the one still open at twenty to twelve. The legend
// names it only when a radar reported something.
//
// verifies SPEC §3.3.1, §9.2
func TestBrowseDrawsEachRoomsPresence(t *testing.T) {
	now := func() time.Time { return thursday.Add(11*time.Hour + 40*time.Minute) }
	store := withPresence(t, withWakeReject(t, withFailure(t, bargeInLog(t))))
	h := get(t, newServer(store, curation.NewMemStore(), fixtureBlobs(t), now), "/conversations")

	if got := strings.Count(h, `class="day-lanes__presence"`); got != 2 {
		t.Errorf("Browse draws %d presence spans, want breakfast and the one still open", got)
	}
	if !strings.Contains(h, "room occupied (mmWave)") {
		t.Error("the legend does not say what the presence band is")
	}
	if strings.Contains(h[strings.Index(h, `id="conversations"`):], `href="/conversations/device:kitchen`) {
		t.Error("a device's log is listed as a conversation")
	}

	device := get(t, newServer(store, curation.NewMemStore(), fixtureBlobs(t), now), "/conversations/device:kitchen")
	if !strings.Contains(device, "presence: present") || !strings.Contains(device, "room_presence") {
		t.Error("the kitchen's own log does not say what its radar reported")
	}

	quiet := get(t, newBrowseServer(t), "/conversations")
	if strings.Contains(quiet, "day-lanes__presence") || strings.Contains(quiet, "room occupied") {
		t.Error("a household with no radar is drawn with presence")
	}
}
