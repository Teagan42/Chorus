package session_test

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// Teagan's forecast, and the garage door the model found open while it
// played.
const (
	forecast     = "Tomorrow will be cloudy in the morning, with rain from three."
	forecastHalf = "Tomorrow will be cloudy in the morning,"
	garageOpen   = "Sorry to cut in, but the garage door is open."
)

func (f *fakeSpeaker) opens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opened...)
}

// results lists the outcomes journalled for one call, in order.
func (r *rig) results(t *testing.T, convID, callID string) []string {
	t.Helper()
	events, err := r.store.Events(t.Context(), convID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var out []string
	for _, e := range events {
		if e.Kind == journal.KindToolResult && e.Fields["call_id"] == callID {
			out = append(out, e.Fields["outcome"])
		}
	}
	return out
}

// interjecting is the model speaking the forecast, then interjecting the
// garage door while it plays.
func interjecting() []step {
	return []step{
		{act: session.SpeechDelta{CallID: "s1", Text: forecast, Last: true}},
		{act: session.SpeechDelta{CallID: "s2", Text: garageOpen, Mode: session.ModeInterject, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
}

// An interjection pauses the forecast where the DAC stopped, says the
// garage door, and resumes the forecast from there under the same call.
// The pause is recorded as interjected, never as anyone cutting it off.
//
// verifies SPEC §4.2
func TestInterjectPausesWhatIsPlayingAndResumesIt(t *testing.T) {
	r := newRig(t, interjecting(), nil)
	r.speaker.hold = true
	r.speaker.cut = len(forecastHalf)

	s := r.open(t, "teagan")
	errc := heard(s, "what's the weather tomorrow and is the garage shut")
	if got := r.speaker.wrote(t); got != forecast {
		t.Fatalf("first utterance = %q, want the forecast", got)
	}
	if got := r.speaker.wrote(t); got != garageOpen {
		t.Fatalf("second utterance = %q; the interjection never cut in", got)
	}
	close(r.speaker.release)
	if got := r.speaker.wrote(t); got != " with rain from three." {
		t.Fatalf("third utterance = %q, want the rest of the forecast", got)
	}
	wait(t, errc)

	if got := r.speaker.opens(); !slices.Equal(got, []string{"s1", "s2", "s1"}) {
		t.Errorf("opened %q, want the forecast, the interjection, then the forecast again", got)
	}
	pause := r.eventOf(t, s.ConversationID(), journal.KindSpeechTruncated)
	want := map[string]string{
		"spoken_text": forecastHalf, "unspoken_text": " with rain from three.",
		"frames_played": "6240", "call_id": "s1", "reason": "interjected",
	}
	if !maps.Equal(pause.Fields, want) {
		t.Errorf("pause = %v, want %v", pause.Fields, want)
	}
	st := r.state(t, s.ConversationID())
	if want := []string{forecastHalf, garageOpen, " with rain from three."}; !reflect.DeepEqual(st.Spoken, want) {
		t.Errorf("spoken = %q, want %q", st.Spoken, want)
	}
	if st.Interrupted || len(st.Unspoken) != 0 {
		t.Errorf("interrupted=%v unspoken=%q: nothing was cut off", st.Interrupted, st.Unspoken)
	}
	if got := r.results(t, s.ConversationID(), "s1"); !slices.Equal(got, []string{"ok"}) {
		t.Errorf("forecast results = %q, want one ok once it resumed", got)
	}
	if got := st.Dialogue[1]; got.CallID != "s1" || got.Text != forecast || got.Cut || got.Pending {
		t.Errorf("forecast in the dialogue = %+v, want all of it, heard", got)
	}
}

// The model is still generating the forecast when it interjects, and the
// kitchen has played all of it so far. What it generates after the
// interjection is not refused: it plays once the interjection has been said.
//
// verifies SPEC §4.1, §4.2
func TestATrailingDeltaForAnInterjectedCallPlaysAfterTheInterjection(t *testing.T) {
	paused := make(chan struct{})
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: forecastHalf}},
		// Held until the pause is recorded, so nothing generated is unheard yet.
		{act: session.SpeechDelta{CallID: "s2", Text: garageOpen, Mode: session.ModeInterject, Last: true}, gate: paused},
		{act: session.SpeechDelta{CallID: "s1", Text: " with rain from three.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	r.store.recorded(journal.KindSpeechSpoken, "s1", paused)
	r.speaker.hold = true
	r.speaker.cut = len(forecastHalf)

	s := r.open(t, "teagan")
	errc := heard(s, "what's the weather tomorrow and is the garage shut")
	r.speaker.wrote(t)
	if got := r.speaker.wrote(t); got != garageOpen {
		t.Fatalf("second utterance = %q; the interjection never cut in", got)
	}
	close(r.speaker.release)
	if got := r.speaker.wrote(t); got != " with rain from three." {
		t.Fatalf("third utterance = %q, want the delta generated during the interjection", got)
	}
	wait(t, errc)

	st := r.state(t, s.ConversationID())
	if len(st.Unspoken) != 0 {
		t.Errorf("unspoken = %q: a delta for a paused call was refused", st.Unspoken)
	}
	if got := st.Dialogue[1]; got.Text != forecast || got.Cut {
		t.Errorf("forecast in the dialogue = %+v, want all of it", got)
	}
	if want := []string{forecastHalf, garageOpen, " with rain from three."}; !reflect.DeepEqual(st.Spoken, want) {
		t.Errorf("spoken = %q, want %q", st.Spoken, want)
	}
	if got := r.results(t, s.ConversationID(), "s1"); !slices.Equal(got, []string{"ok"}) {
		t.Errorf("forecast results = %q, want one ok once it resumed", got)
	}
}

// Teagan cuts off the interjection. The rest of the forecast it paused is
// discarded with it, as the barge-in, and the forecast keeps the half that
// was heard: one result, cut by Teagan.
//
// verifies SPEC §4.2, §4.4
func TestABargeInDuringAnInterjectionDiscardsWhatItPaused(t *testing.T) {
	r := newRig(t, interjecting(), nil)
	r.speaker.hold = true
	r.speaker.cut = len(forecastHalf)

	s := r.open(t, "teagan")
	errc := heard(s, "what's the weather tomorrow and is the garage shut")
	r.speaker.wrote(t)
	r.speaker.wrote(t)
	if ok, err := s.BargeIn(t.Context(), interruption(380)); err != nil || !ok {
		t.Fatalf("barge-in: ok=%v err=%v", ok, err)
	}
	wait(t, errc)

	if got := r.speaker.opens(); !slices.Equal(got, []string{"s1", "s2"}) {
		t.Errorf("opened %q: the forecast resumed after the barge-in", got)
	}
	dropped := r.eventOf(t, s.ConversationID(), journal.KindSpeechDiscarded)
	if dropped.Fields["call_id"] != "s1" || dropped.Fields["unspoken_text"] != " with rain from three." || dropped.Fields["reason"] != "barge_in" {
		t.Errorf("discarded = %v, want the rest of the forecast, by the barge-in", dropped.Fields)
	}
	if got := r.results(t, s.ConversationID(), "s1"); !slices.Equal(got, []string{"cancelled"}) {
		t.Errorf("forecast results = %q, want one cancelled", got)
	}
	st := r.state(t, s.ConversationID())
	if got := st.Dialogue[1]; got.Text != forecastHalf || !got.Cut || got.CutBy != "barge_in" {
		t.Errorf("forecast in the dialogue = %+v, want the heard half, cut by Teagan", got)
	}
}
