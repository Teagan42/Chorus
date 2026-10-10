package harvest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
)

// kitchenLog is the kitchen satellite's own log on a Thursday afternoon: its
// radar, the dishwasher it woke on with both channels kept, and a guest's
// "hey eddie" stage three would not answer.
func kitchenLog(t *testing.T) journal.Store {
	t.Helper()
	store := journal.NewMemStore()
	clk := &stepClock{}
	j := journal.New(store, clk, versions())
	day := time.Date(2025, time.October, 9, 0, 0, 0, 0, time.UTC)
	for _, r := range []struct {
		at  time.Duration
		rec journal.Record
	}{
		{12*time.Hour + 8*time.Minute, record(journal.KindPresenceChanged, "", "state", "present", "sensor", "room_presence")},
		{14*time.Hour + 2*time.Minute, record(journal.KindWakeRejected, "blob://wake/kitchen-dishwasher", "reason", "no_speech", "second_audio_ref", "blob://wake/kitchen-dishwasher-second")},
		{19*time.Hour + 2*time.Minute, record(journal.KindWakeRejected, "blob://wake/kitchen-guest", "reason", "unknown_speaker")},
	} {
		clk.now = day.Add(r.at)
		if _, err := j.Append(context.Background(), "device:kitchen", r.rec); err != nil {
			t.Fatalf("append %s: %v", r.rec.Kind, err)
		}
	}
	return store
}

type stepClock struct{ now time.Time }

func (c *stepClock) Now() time.Time { return c.now }

// Every stage-two rejection in a satellite's log is a hard negative with its
// audio on both channels, why it was rejected, where and when (SPEC §9.3).
//
// verifies SPEC §9.3
func TestEveryRejectedWakeIsAHardNegative(t *testing.T) {
	ns, err := harvest.Negatives(context.Background(), kitchenLog(t), "device:kitchen")
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 {
		t.Fatalf("got %d negatives, want the dishwasher and the guest", len(ns))
	}
	dish := ns[0]
	want := harvest.Negative{
		ID: "device:kitchen/2", ConversationID: "device:kitchen", Seq: 2, Satellite: "kitchen",
		At: time.Date(2025, time.October, 9, 14, 2, 0, 0, time.UTC), Reason: "no_speech",
		Audio: "blob://wake/kitchen-dishwasher", SecondAudio: "blob://wake/kitchen-dishwasher-second",
	}
	if dish != want {
		t.Errorf("dishwasher = %+v, want %+v", dish, want)
	}
	if dish.Held() || !ns[1].Held() {
		t.Error("only the guest's wake, which passed the wake model and the speech check, waits for a reviewer")
	}
}

// The corpus file is one JSON object per reject, label 0, both channels by
// blob ref; a reject that kept one channel says so with an empty second.
//
// verifies SPEC §9.3
func TestHardNegativesExportAsOneRowEach(t *testing.T) {
	ns, err := harvest.Negatives(context.Background(), kitchenLog(t), "device:kitchen")
	if err != nil {
		t.Fatal(err)
	}
	ns[1].Confirmed = true
	var buf bytes.Buffer
	if err := harvest.ExportNegatives(&buf, ns); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d rows, want 2:\n%s", len(lines), buf.Bytes())
	}
	if got, want := string(lines[0]), `{"id":"device:kitchen/2","label":0,"source":"stage2_reject","reason":"no_speech","satellite":"kitchen","at":"2025-10-09T14:02:00Z","audio":"blob://wake/kitchen-dishwasher","second_audio":"blob://wake/kitchen-dishwasher-second","confirmed":false,"conversation_id":"device:kitchen","seq":2}`; got != want {
		t.Errorf("row =\n%s\nwant\n%s", got, want)
	}
	var guest map[string]any
	if err := json.Unmarshal(lines[1], &guest); err != nil {
		t.Fatal(err)
	}
	if guest["second_audio"] != "" || guest["confirmed"] != true || guest["reason"] != "unknown_speaker" {
		t.Errorf("guest row = %v", guest)
	}
}
