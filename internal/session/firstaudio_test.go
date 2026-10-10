package session_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// dacSpeaker is a speaker whose streams can see their DAC, as the satellite's
// do. Each utterance's first write is when its audio reaches the device, and
// the device then takes the next lag off the list to report its first frame.
// A call listed in silent never reports one: it was cut before it played.
type dacSpeaker struct {
	clock  *clock
	lags   []time.Duration
	silent map[string]bool

	mu sync.Mutex
}

func (d *dacSpeaker) Open(_ context.Context, callID string) (session.Stream, error) {
	return &dacStream{sp: d, callID: callID, started: make(chan struct{})}, nil
}

type dacStream struct {
	sp      *dacSpeaker
	callID  string
	text    string
	started chan struct{}
	playing bool
}

func (s *dacStream) Started() <-chan struct{} { return s.started }

func (s *dacStream) Write(text string) error {
	s.text += text
	if s.playing || s.sp.silent[s.callID] {
		return nil
	}
	s.playing = true
	s.sp.mu.Lock()
	var lag time.Duration
	if len(s.sp.lags) > 0 {
		lag, s.sp.lags = s.sp.lags[0], s.sp.lags[1:]
	}
	s.sp.mu.Unlock()
	s.sp.clock.advance(lag)
	close(s.started)
	return nil
}

func (s *dacStream) Close() session.Playback {
	if !s.playing {
		return session.Playback{Unspoken: s.text, Truncated: true}
	}
	return session.Playback{Spoken: s.text, Frames: int64(len(s.text)) * 160, AudioRef: "blob://tts/" + s.callID}
}

func newDACRig(t *testing.T, steps []step, dac *dacSpeaker) *rig {
	t.Helper()
	return newRigWith(t, steps, nil, nil, func(c *session.Config) {
		dac.clock = c.Clock.(*clock)
		c.Speaker = dac
	})
}

// endedAt runs a turn whose speaker stopped talking at ended.
func endedAt(s *session.Session, text string, ended time.Time) <-chan error {
	out := make(chan error, 1)
	go func() {
		out <- s.Heard(context.Background(), session.Transcript{Text: text, AudioRef: "blob://mic/1", Ended: ended})
	}()
	return out
}

func (r *rig) events(t *testing.T, convID string) []journal.Event {
	t.Helper()
	events, err := r.store.Events(context.Background(), convID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return events
}

func starts(events []journal.Event) []map[string]string {
	var out []map[string]string
	for _, e := range events {
		if e.Kind == journal.KindSpeechStarted {
			out = append(out, e.Fields)
		}
	}
	return out
}

// Alan asks for the kitchen lights; the model answers and the satellite
// first reports playing it 1.84 s after he stopped talking.
//
// verifies SPEC §11
func TestTheFirstPlayedFrameIsJournalledWithTheWait(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_1", Text: "Turning off the kitchen lights.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	dac := &dacSpeaker{lags: []time.Duration{1840 * time.Millisecond}}
	r := newDACRig(t, steps, dac)

	s := r.open(t, "alan")
	wait(t, endedAt(s, "turn off the kitchen lights", epoch))

	events := r.events(t, s.ConversationID())
	want := []map[string]string{{"call_id": "call_1", "wait_ms": "1840"}}
	if got := starts(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("speech_started = %v, want %v", got, want)
	}
	// The start is journalled when it happened, and before the speech it
	// began is closed off.
	var startAt, spokenAt int
	for i, e := range events {
		switch e.Kind {
		case journal.KindSpeechStarted:
			startAt = i
			if !e.At.Equal(epoch.Add(1840 * time.Millisecond)) {
				t.Errorf("speech_started at %v, want the moment the DAC reported", e.At)
			}
		case journal.KindSpeechSpoken:
			spokenAt = i
		}
	}
	if startAt > spokenAt {
		t.Errorf("speech_started is event %d, after speech_spoken at %d", startAt, spokenAt)
	}
}

// "One sec" plays, then the search result lands and "I found three albums"
// plays behind it. The household's wait ended at "one sec".
//
// verifies SPEC §11
func TestOnlyTheTurnsFirstUtteranceCountsAsItsStart(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_1", Text: "One sec.", Last: true}},
		{act: session.SpeechDelta{CallID: "call_2", Text: "I found three albums by Led Zeppelin.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	dac := &dacSpeaker{lags: []time.Duration{620 * time.Millisecond, 2100 * time.Millisecond}}
	r := newDACRig(t, steps, dac)

	s := r.open(t, "teagan")
	wait(t, endedAt(s, "play something by zeppelin", epoch))

	want := []map[string]string{{"call_id": "call_1", "wait_ms": "620"}}
	if got := starts(r.events(t, s.ConversationID())); !reflect.DeepEqual(got, want) {
		t.Errorf("speech_started = %v, want only the first utterance's", got)
	}
}

// Each ask is its own wait: the follow-up records its own start.
//
// verifies SPEC §11
func TestEachTurnRecordsItsOwnStart(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_1", Text: "The garage door is open.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	dac := &dacSpeaker{lags: []time.Duration{900 * time.Millisecond, 1300 * time.Millisecond}}
	r := newDACRig(t, steps, dac)

	s := r.open(t, "alan")
	wait(t, endedAt(s, "is the garage door closed", epoch))
	r.engine.steps = []step{
		{act: session.SpeechDelta{CallID: "call_2", Text: "Closing the garage door.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r.clock.advance(4 * time.Second)
	wait(t, endedAt(s, "close it", r.clock.Now()))

	got := starts(r.events(t, s.ConversationID()))
	if len(got) != 2 || got[0]["call_id"] != "call_1" || got[1]["call_id"] != "call_2" {
		t.Fatalf("speech_started = %v, want one for each turn", got)
	}
	if got[0]["wait_ms"] != "900" || got[1]["wait_ms"] != "1300" {
		t.Errorf("waits = %s and %s ms, want 900 and 1300", got[0]["wait_ms"], got[1]["wait_ms"])
	}
}

// The answer was cut before the DAC played any of it: nobody heard it start,
// so nothing records a start.
//
// verifies SPEC §11
func TestSpeechThatNeverPlayedRecordsNoStart(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_1", Text: "Here is the forecast for Saturday.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	dac := &dacSpeaker{silent: map[string]bool{"call_1": true}}
	r := newDACRig(t, steps, dac)

	s := r.open(t, "alan")
	wait(t, endedAt(s, "what's the weather on saturday", epoch))

	if got := starts(r.events(t, s.ConversationID())); len(got) != 0 {
		t.Errorf("speech_started = %v for speech that never played", got)
	}
}

// A listener with no clock gives the ask no stop time. The start is still
// when the answer was heard; the wait is left out rather than invented.
//
// verifies SPEC §11
func TestAnAskWithNoEndpointRecordsTheStartWithoutAWait(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_1", Text: "Turning off the kitchen lights.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	dac := &dacSpeaker{lags: []time.Duration{700 * time.Millisecond}}
	r := newDACRig(t, steps, dac)

	s := r.open(t, "alan")
	wait(t, heard(s, "turn off the kitchen lights"))

	want := []map[string]string{{"call_id": "call_1"}}
	if got := starts(r.events(t, s.ConversationID())); !reflect.DeepEqual(got, want) {
		t.Errorf("speech_started = %v, want %v", got, want)
	}
}

// A speaker that cannot see its DAC is not asked to guess.
//
// verifies SPEC §11
func TestASpeakerBlindToPlaybackRecordsNoStart(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_1", Text: "Turning off the kitchen lights.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)

	s := r.open(t, "alan")
	wait(t, endedAt(s, "turn off the kitchen lights", epoch))

	if got := countKind(r.kinds(t, s.ConversationID()), journal.KindSpeechStarted); got != 0 {
		t.Errorf("%d speech_started events from a speaker that cannot see playback", got)
	}
}
