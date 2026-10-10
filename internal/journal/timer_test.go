package journal_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
)

// dinnertime is a Thursday evening in the kitchen.
var dinnertime = time.Date(2026, 10, 8, 18, 40, 0, 0, time.UTC)

// houseTimers is what the house log holds after Alan set the oven for twelve
// minutes and Teagan set the pasta for nine, then thought better of it, and
// the oven went off on the kitchen satellite.
func houseTimers() []journal.Record {
	return []journal.Record{
		{Kind: journal.KindTimerStarted, Fields: map[string]string{
			"timer_id": "t_0a7e11c3", "seconds": "720", "fires_at": "2026-10-08T18:52:00Z",
			"satellite": "kitchen", "label": "oven", "announcement": "The oven timer is done.",
			"person": "alan", "conversation_id": "conv-1840-kitchen", "call_id": "call_t1",
		}},
		{Kind: journal.KindTimerStarted, Fields: map[string]string{
			"timer_id": "t_5b9d2e40", "seconds": "540", "fires_at": "2026-10-08T18:50:30Z",
			"satellite": "kitchen", "label": "pasta", "person": "teagan",
			"conversation_id": "conv-1841-kitchen", "call_id": "call_t2",
		}},
		{Kind: journal.KindTimerCancelled, Fields: map[string]string{
			"timer_id": "t_5b9d2e40", "conversation_id": "conv-1843-kitchen", "call_id": "call_t3",
		}},
		{Kind: journal.KindTimerFinished, Fields: map[string]string{
			"timer_id": "t_0a7e11c3", "outcome": "announced", "conversation_id": "conv-1852-kitchen",
		}},
	}
}

func writeAll(t *testing.T, store journal.Store, conv string, rs []journal.Record) {
	t.Helper()
	j := journal.New(store, journal.FixedClock(dinnertime), journal.Versions{})
	for _, r := range rs {
		if _, err := j.Append(context.Background(), conv, r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
}

// The house log reduces to every timer it set and what became of each, so a
// daemon that restarts knows what is still running from the log alone.
//
// verifies SPEC §8
func TestTheHouseLogReducesToItsTimers(t *testing.T) {
	store := journal.NewMemStore()
	writeAll(t, store, journal.HouseTimers, houseTimers()[:2])

	st, err := journal.Replay(context.Background(), store, journal.HouseTimers, journal.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	running := st.Running()
	if len(running) != 2 || running[0].Label != "pasta" || running[1].Label != "oven" {
		t.Fatalf("running = %+v, want the pasta first, as it goes off first", running)
	}
	oven := running[1]
	if oven.Seconds != 720 || !oven.FiresAt.Equal(time.Date(2026, 10, 8, 18, 52, 0, 0, time.UTC)) ||
		oven.Satellite != "kitchen" || oven.Person != "alan" || oven.CallID != "call_t1" ||
		oven.Announcement != "The oven timer is done." {
		t.Errorf("oven = %+v", oven)
	}

	writeAll(t, store, journal.HouseTimers, houseTimers()[2:])
	st, err = journal.Replay(context.Background(), store, journal.HouseTimers, journal.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Running(); len(got) != 0 {
		t.Errorf("still running: %+v", got)
	}
	pasta, _ := st.Timer("t_5b9d2e40")
	oven, _ = st.Timer("t_0a7e11c3")
	if pasta.Status != journal.TimerCancelled {
		t.Errorf("pasta is %s, want cancelled", pasta.Status)
	}
	if oven.Status != journal.TimerFinished || oven.Outcome != "announced" || oven.AnnouncedIn != "conv-1852-kitchen" {
		t.Errorf("oven = %+v, want finished, announced in the kitchen", oven)
	}
}

// A log that ends a timer twice, or ends one never set, says something the
// house did not do. Replay fails rather than guessing which half is true.
//
// verifies SPEC §8
func TestATimerLogThatContradictsItselfFailsReplay(t *testing.T) {
	recs := houseTimers()
	cases := map[string][]journal.Record{
		"set twice": {recs[0], recs[0]},
		"cancelled after it went off": {recs[0], recs[3], {Kind: journal.KindTimerCancelled, Fields: map[string]string{
			"timer_id": "t_0a7e11c3", "conversation_id": "conv-1853-kitchen", "call_id": "call_t4",
		}}},
		"went off without being set": {recs[3]},
		"a fire time that is not a time": {{Kind: journal.KindTimerStarted, Fields: map[string]string{
			"timer_id": "t_1c2d3e4f", "seconds": "600", "fires_at": "ten to seven",
			"satellite": "kitchen", "conversation_id": "conv-1840-kitchen", "call_id": "call_t1",
		}}},
	}
	for name, rs := range cases {
		t.Run(name, func(t *testing.T) {
			store := journal.NewMemStore()
			writeAll(t, store, journal.HouseTimers, rs)
			if _, err := journal.Replay(context.Background(), store, journal.HouseTimers, journal.Overrides{}); err == nil {
				t.Error("replayed without complaint")
			}
		})
	}
}

// An announcement session's log, in the order the session writes it: opened
// with no wake word, the announcement, the speak call that says it, what
// the kitchen heard, then Alan answering it.
func dinnerAnnouncement() []journal.Record {
	return []journal.Record{
		{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen", "announced": "true"}},
		{Kind: journal.KindAnnouncementMade, Fields: map[string]string{
			"text": "Dinner is ready. Does anyone want wine?", "call_id": "an_4c1f", "source": "request",
			"requested_by": "teagan", "from_satellite": "office", "from_conversation": "conv-1838-office",
			"start_conversation": "true",
		}},
		{Kind: journal.KindToolCalled, Fields: map[string]string{
			"tool": "speak", "call_id": "an_4c1f", "args_json": `{"text":"Dinner is ready. Does anyone want wine?","mode":"queue"}`,
		}},
		{Kind: journal.KindSpeechSpoken, AudioRef: "blob://tts/an_4c1f", Fields: map[string]string{
			"text": "Dinner is ready. Does anyone want wine?", "frames_played": "41600", "call_id": "an_4c1f",
		}},
		{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "an_4c1f", "outcome": "ok"}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/wine", Fields: map[string]string{
			"text": "yes a glass of red please", "speaker_id": "alan",
		}},
	}
}

// The model is told what it announced and why, where it said it, so Alan's
// "yes, a glass of red" answers a question it can see was asked for Teagan
// from the office (SPEC §4).
//
// verifies SPEC §4, §4.4
func TestAnAnnouncementIsInTheDialogueWithWhyItWasSaid(t *testing.T) {
	store := journal.NewMemStore()
	writeAll(t, store, "conv-1845-kitchen", dinnerAnnouncement())

	st, err := journal.Replay(context.Background(), store, "conv-1845-kitchen", journal.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Announced || len(st.Announcements) != 1 {
		t.Fatalf("announced = %v, announcements = %+v", st.Announced, st.Announcements)
	}
	if len(st.Dialogue) != 2 {
		t.Fatalf("dialogue = %+v, want the announcement then Alan", st.Dialogue)
	}
	said, answer := st.Dialogue[0], st.Dialogue[1]
	if said.Kind != journal.EntrySaid || said.Text != "Dinner is ready. Does anyone want wine?" || said.Pending {
		t.Errorf("said = %+v", said)
	}
	a := said.Announces
	if a == nil || a.Source != "request" || a.RequestedBy != "teagan" || a.FromSatellite != "office" || !a.StartConversation {
		t.Errorf("announces = %+v, want Teagan's request from the office, answerable", a)
	}
	if answer.Kind != journal.EntryHeard || answer.Speaker != "alan" || answer.Announces != nil {
		t.Errorf("answer = %+v", answer)
	}
	if !slices.Equal(st.Participants, []string{"alan"}) {
		t.Errorf("participants = %v: the announcement opened with nobody, Alan answered", st.Participants)
	}
}
