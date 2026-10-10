package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// refusingEngine is Ollama with nothing listening on its port.
type refusingEngine struct{}

func (refusingEngine) Turn(context.Context, session.Input) (<-chan session.Action, error) {
	return nil, errors.New(`ollama chat: Post "http://10.0.0.20:11434/api/chat": dial tcp 10.0.0.20:11434: connect: connection refused`)
}

// fallingKokoro renders silence like silentSynth until it is asked for text
// containing dieAt, and fails that and everything after, as a Kokoro whose
// container is killed mid-answer does.
type fallingKokoro struct {
	dieAt string

	mu    sync.Mutex
	dead  bool
	calls []string
}

func (k *fallingKokoro) Synthesize(ctx context.Context, text string) ([]byte, error) {
	k.mu.Lock()
	k.calls = append(k.calls, text)
	k.dead = k.dead || strings.Contains(text, k.dieAt)
	dead := k.dead
	k.mu.Unlock()
	if dead {
		return nil, errors.New(`kokoro: Post "http://10.0.0.21:8880/v1/audio/speech": EOF`)
	}
	return silentSynth{}.Synthesize(ctx, text)
}

// rendered is everything Kokoro was asked for, in order.
func (k *fallingKokoro) rendered() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return strings.Join(k.calls, "")
}

// pacingOnly fires the downlink's sub-second pacing and the stop's settle at
// once, and nothing else: the canned lines run past the two slices the
// satellite primes without waiting, and no deadline or backstop may end
// anything behind the test's back.
type pacingOnly struct{}

func (pacingOnly) After(d time.Duration) <-chan time.Time {
	if d >= time.Second {
		return neverTimers{}.After(d)
	}
	fired := make(chan time.Time, 1)
	fired <- epoch
	return fired
}

func cannedOf(t *testing.T, r *rig, id string) journal.Event {
	t.Helper()
	for _, e := range r.store.ofKind(journal.KindToolCalled) {
		if e.Fields["call_id"] == id {
			return e
		}
	}
	t.Fatalf("no speak call %q for the canned line", id)
	return journal.Event{}
}

// Alan wakes the kitchen and asks for the lights with Ollama down. The
// kitchen apologises out loud instead of going silent, and the failure is in
// the log with Ollama's own error (SPEC §7).
//
// verifies SPEC §7
func TestTheKitchenApologisesWhenOllamaIsDown(t *testing.T) {
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = refusingEngine{}
		d.Timers = pacingOnly{}
	})
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("turn off the kitchen lights", alan))

	dev.AwaitTTS(t, 2*len(session.DefaultCanned.Model))
	dev.PlayAll(t)
	spoken := r.store.awaitKind(t, journal.KindSpeechSpoken, 1)
	failed := r.store.awaitKind(t, journal.KindModelFailed, 1)

	if failed.Fields["reason"] != "unavailable" || !strings.Contains(failed.Fields["error"], "connection refused") {
		t.Errorf("model_failed = %v, want Ollama unreachable", failed.Fields)
	}
	if spoken.Fields["text"] != session.DefaultCanned.Model || spoken.Fields["call_id"] != failed.Fields["canned_call_id"] {
		t.Errorf("speech_spoken = %v, want the canned line model_failed names", spoken.Fields)
	}
	if !strings.Contains(cannedOf(t, r, failed.Fields["canned_call_id"]).Fields["args_json"], `"canned":true`) {
		t.Error("the apology's speak call is not marked canned")
	}
	r.logs.await(t, "model failed")
}

// Kokoro answers the daemon's startup render and the first clause of the
// forecast, then its container dies. The kitchen played "Tomorrow will be
// sunny," and then says it lost its voice, from audio rendered at startup;
// the log says the voice failed, and nowhere that Alan cut it off.
//
// verifies SPEC §7, §9.1
func TestTheKitchenSaysItLostItsVoiceWhenKokoroDiesMidAnswer(t *testing.T) {
	const first, rest = "Tomorrow will be sunny, ", "with a high of nineteen."
	kokoro := &fallingKokoro{dieAt: "nineteen"}
	eng := &scriptEngine{acts: []session.Action{
		session.SpeechDelta{CallID: "call_s1", Text: first + rest, Last: true},
		session.TurnEnd{FinishReason: "stop", Completion: "{}"},
	}}
	r := newRig(t, inventory(), func(d *deps) {
		d.synth = kokoro
		d.engine = eng
		d.Timers = pacingOnly{}
	})
	// The apologies are rendered while Kokoro still answers, the voice's last.
	voice := session.DefaultCanned.Voice
	last := voice[strings.LastIndex(voice, " ")+1:]
	await(t, "the canned lines to be rendered", func() bool { return strings.HasSuffix(kokoro.rendered(), last) })

	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("what's the weather tomorrow", alan))

	dev.AwaitTTS(t, 2*len(first))
	dev.PlayAll(t)
	dev.AwaitTTS(t, 2*len(first)+2*len(session.DefaultCanned.Voice))
	dev.PlayAll(t)
	spoken := r.store.awaitKind(t, journal.KindSpeechSpoken, 1)

	failed := r.store.awaitKind(t, journal.KindSpeechFailed, 1)
	if failed.Fields["call_id"] != "call_s1" || failed.Fields["reason"] != "tts_unavailable" || !strings.Contains(failed.Fields["error"], "kokoro") {
		t.Errorf("speech_failed = %v, want call_s1's voice failing with Kokoro's error", failed.Fields)
	}
	cut := r.store.awaitKind(t, journal.KindSpeechTruncated, 1)
	if cut.Fields["reason"] != "tts_unavailable" || cut.Fields["spoken_text"] != first || cut.Fields["unspoken_text"] != rest {
		t.Errorf("speech_truncated = %v, want %q heard, cut by the voice", cut.Fields, first)
	}
	if spoken.Fields["text"] != session.DefaultCanned.Voice || spoken.Fields["call_id"] != failed.Fields["canned_call_id"] {
		t.Errorf("speech_spoken = %v, want the voice's apology", spoken.Fields)
	}
	for _, e := range r.store.events() {
		if e.Kind == journal.KindBargeInDetected || e.Fields["reason"] == "barge_in" {
			t.Errorf("seq %d %s %v reads as Alan interrupting", e.Seq, e.Kind, e.Fields)
		}
	}
}
