package session_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// postgres is the journal's database, which can go away and come back.
type postgres struct {
	journal.Store

	mu   sync.Mutex
	down bool
}

var errPostgresDown = errors.New(`journal: dial tcp 10.0.0.30:5432: connect: connection refused`)

func (p *postgres) Append(ctx context.Context, e journal.Event) error {
	p.mu.Lock()
	down := p.down
	p.mu.Unlock()
	if down {
		return errPostgresDown
	}
	return p.Store.Append(ctx, e)
}

func (p *postgres) set(down bool) {
	p.mu.Lock()
	p.down = down
	p.mu.Unlock()
}

// Postgres is down when Alice wakes the kitchen. There is no log to record
// a word in, and the log is the runtime (ADR-0007), so nothing is said: the
// open fails with the database's error and the ring shows the fault until
// the journal takes a write again.
//
// verifies SPEC §7, §8, §3.3.1
func TestAJournalThatRefusesTheOpenShowsAnErrorAndSaysNothing(t *testing.T) {
	lights := &ring{}
	db := &postgres{down: true}
	voice := &kokoro{}
	r := newRigWith(t, nil, nil, nil, func(c *session.Config) {
		db.Store = c.Store
		c.Store = db
		c.Journal = journal.New(db, c.Clock, versions())
		c.Speaker = voice
		c.Ring = session.NewPanel(lights)
	})

	_, err := r.sup.Open(context.Background(), session.Wake{Satellite: "kitchen", PersonID: "alice"})
	if !errors.Is(err, errPostgresDown) {
		t.Fatalf("open = %v, want the journal's refusal", err)
	}
	lights.await(t, session.RingError)
	if got := voice.heard(); len(got) != 0 {
		t.Errorf("the kitchen said %q with no journal to record it in", got)
	}

	db.set(false)
	s := r.open(t, "alice")
	lights.await(t, session.RingListening)
	if err := s.Close(t.Context(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Dark for the moment between the journal answering and the session
	// listening: the open's own write is what clears the fault.
	want := []session.RingState{session.RingError, session.RingOff, session.RingListening, session.RingOff}
	if got := lights.states(); !slices.Equal(got, want) {
		t.Errorf("the ring showed %v, want %v: the fault cleared by the next write", got, want)
	}
}

// Alice asks the kitchen something mid-conversation and the speech-to-text
// sidecar fails on it. The kitchen says it could not hear rather than
// going quiet; the line is a speak call marked canned and unheard, and the
// model is never shown it as its own words.
//
// verifies SPEC §7
func TestAnUtteranceTheEarFailedOnIsAnsweredWithTheCannedLine(t *testing.T) {
	voice := &kokoro{}
	lights := &ring{}
	r := failRig(t, &downEngine{}, voice, func(c *session.Config) { c.Ring = session.NewPanel(lights) })

	s := r.open(t, "alice")
	if err := s.Unheard(context.Background(), epoch); err != nil {
		t.Fatalf("Unheard = %v", err)
	}

	events := r.all(t, s.ConversationID())
	spoken := ofKind(events, journal.KindSpeechSpoken)
	if len(spoken) != 1 || spoken[0].Fields["text"] != session.DefaultCanned.Ear {
		t.Fatalf("spoken = %v, want the ear's canned line", spokenTexts(events))
	}
	call := cannedCall(t, events, spoken[0].Fields["call_id"])
	if !strings.Contains(call.Fields["args_json"], `"unheard":true`) {
		t.Errorf("args = %s, want the line marked as answering something unheard", call.Fields["args_json"])
	}
	if got := voice.heard(); len(got) != 1 || got[0] != session.DefaultCanned.Ear {
		t.Errorf("the kitchen played %q, want the ear's line", got)
	}
	if n := len(ofKind(events, journal.KindUtteranceTranscribed)); n != 0 {
		t.Errorf("%d utterances transcribed from words nothing decoded", n)
	}
	for _, e := range r.state(t, s.ConversationID()).Dialogue {
		if e.Kind == journal.EntrySaid && !e.Canned {
			t.Errorf("dialogue entry %+v is the apology shown to the model as its own words", e)
		}
	}
	if got := s.Children(); len(got) != 1 || got[0] != "listening" {
		t.Errorf("children = %v, want the session listening for the next try", got)
	}
	lights.await(t, session.RingListening)
}

// Alan wakes the kitchen and the speech-to-text sidecar fails on his first
// words. The wake stays rejected in the kitchen's own log, naming the
// apology, and the apology follows it there: no conversation was opened
// for a wake nothing confirmed.
//
// verifies SPEC §7, §9.3
func TestAWakeTheEarFailedOnIsApologisedForInTheDevicesLog(t *testing.T) {
	voice := &kokoro{}
	lights := &ring{}
	r := failRig(t, &downEngine{}, voice, func(c *session.Config) { c.Ring = session.NewPanel(lights) })

	const log = "device:kitchen"
	err := r.sup.Misheard(context.Background(), "kitchen", log, journal.Record{
		Kind: journal.KindWakeRejected, AudioRef: "blob://wake/porch",
		Fields: map[string]string{"reason": "transcription_failed"},
	})
	if err != nil {
		t.Fatalf("Misheard = %v", err)
	}

	events := r.all(t, log)
	kinds := make([]journal.Kind, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}
	want := []journal.Kind{journal.KindWakeRejected, journal.KindToolCalled, journal.KindSpeechSpoken, journal.KindToolResult}
	if !slices.Equal(kinds, want) {
		t.Fatalf("the kitchen's log holds %v, want %v", kinds, want)
	}
	rej := events[0]
	if rej.Fields["reason"] != "transcription_failed" || rej.Fields["canned_call_id"] == "" {
		t.Errorf("wake_rejected = %v, want transcription_failed naming its apology", rej.Fields)
	}
	call := cannedCall(t, events, rej.Fields["canned_call_id"])
	if !strings.Contains(call.Fields["args_json"], `"unheard":true`) {
		t.Errorf("args = %s, want the line marked unheard", call.Fields["args_json"])
	}
	if events[2].Fields["text"] != session.DefaultCanned.Ear || events[2].Fields["call_id"] != rej.Fields["canned_call_id"] {
		t.Errorf("speech_spoken = %v, want the ear's line under its call", events[2].Fields)
	}
	wantRing := []session.RingState{session.RingSpeaking, session.RingOff}
	if got := lights.states(); !slices.Equal(got, wantRing) {
		t.Errorf("the ring showed %v, want %v", got, wantRing)
	}
}

// With the journal down the rejection cannot be written, so nothing is said
// for it either: an apology the log does not hold is a word the house said
// that no reader can ever find.
//
// verifies SPEC §7, §8
func TestAWakeWhoseRejectionCannotBeJournalledIsNotApologisedFor(t *testing.T) {
	voice := &kokoro{}
	lights := &ring{}
	db := &postgres{down: true}
	r := failRig(t, &downEngine{}, voice, func(c *session.Config) {
		db.Store = c.Store
		c.Store = db
		c.Journal = journal.New(db, c.Clock, versions())
		c.Ring = session.NewPanel(lights)
	})

	err := r.sup.Misheard(context.Background(), "kitchen", "device:kitchen", journal.Record{
		Kind: journal.KindWakeRejected, AudioRef: "blob://wake/porch",
		Fields: map[string]string{"reason": "transcription_failed"},
	})
	if !errors.Is(err, errPostgresDown) {
		t.Errorf("Misheard = %v, want the journal's refusal", err)
	}
	if got := voice.heard(); len(got) != 0 {
		t.Errorf("the kitchen said %q with nothing in the log", got)
	}
	lights.await(t, session.RingError)
}

// Ollama refuses Teagan's ask. Once the session has ended the ring shows the
// failure, and the next turn the model answers clears it.
//
// verifies SPEC §3.3.1, §7
func TestAFailedModelLeavesTheRingShowingAnError(t *testing.T) {
	lights := &ring{}
	eng := &hungEngine{}
	eng.answer(session.TurnEnd{FinishReason: session.FinishError, Completion: `{"error":"EOF"}`, Error: "ollama: EOF"})
	r := failRig(t, eng, &kokoro{}, func(c *session.Config) { c.Ring = session.NewPanel(lights) })

	s := r.open(t, "teagan")
	wait(t, heard(s, "turn off the kitchen lights"))
	if err := s.Close(t.Context(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	lights.await(t, session.RingError)

	eng.answer(session.TurnEnd{FinishReason: "stop", Completion: "{}"})
	s = r.open(t, "alan")
	wait(t, heard(s, "turn off the kitchen lights"))
	if err := s.Close(t.Context(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	lights.await(t, session.RingOff)
}

// Kokoro dies mid-answer. The canned line plays from audio rendered ahead of
// time, which says nothing about Kokoro, so the fault stays until a line the
// model chose plays out.
//
// verifies SPEC §3.3.1, §7
func TestAFailedVoiceLeavesTheRingShowingAnError(t *testing.T) {
	lights := &ring{}
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "Tomorrow will be sunny, with a high of nineteen.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	voice := &kokoro{dies: map[string]int{"call_s1": len("Tomorrow will be sunny, ")}}
	r := newRigWith(t, steps, nil, nil, func(c *session.Config) {
		c.Speaker = voice
		c.Ring = session.NewPanel(lights)
	})

	s := r.open(t, "alan")
	wait(t, heard(s, "what's the weather tomorrow"))
	if err := s.Close(t.Context(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	lights.await(t, session.RingError)

	voice.dies = nil
	s = r.open(t, "teagan")
	wait(t, heard(s, "what's the weather tomorrow"))
	if err := s.Close(t.Context(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	lights.await(t, session.RingOff)
}

// The blob store fails the write of Teagan's utterance. What she said was
// decoded, so it is still a turn: the log names the clip and says at once
// that it was never written, and the house answers her as it would have.
//
// verifies SPEC §8
func TestAnUtteranceWhoseAudioWasNotWrittenIsStillHeard(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "The kitchen lights are off.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	s := r.open(t, "teagan")

	err := s.Heard(context.Background(), session.Transcript{
		Text: "turn off the kitchen lights", SpeakerID: "teagan", AudioRef: "blob://mic/lights",
		AudioLost: errors.New("write mic/lights: input/output error"),
	})
	if err != nil {
		t.Fatalf("Heard = %v", err)
	}
	events := r.all(t, s.ConversationID())
	heardAt := slices.IndexFunc(events, func(e journal.Event) bool { return e.Kind == journal.KindUtteranceTranscribed })
	if heardAt < 0 || heardAt+1 >= len(events) {
		t.Fatalf("no utterance followed by its drop in %v", events)
	}
	dropped := events[heardAt+1]
	if dropped.Kind != journal.KindAudioDropped || dropped.Fields["audio_ref"] != "blob://mic/lights" || dropped.Fields["reason"] != "write_failed" {
		t.Errorf("after the utterance: %s %v, want audio_dropped write_failed for its clip", dropped.Kind, dropped.Fields)
	}
	if got := spokenTexts(events); len(got) != 1 || got[0] != "The kitchen lights are off." {
		t.Errorf("spoken = %q, want the answer", got)
	}
}
