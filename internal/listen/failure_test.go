package listen_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/session"
)

var errSpeachesDown = errors.New(`speaches: Post "http://10.0.0.22:8000/v1/audio/transcriptions": connect: connection refused`)

// lights is the kitchen's LED ring behind a panel: every state shown.
type lights struct {
	mu    sync.Mutex
	shown []session.RingState
}

func (l *lights) Show(s session.RingState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.shown = append(l.shown, s)
}

func (l *lights) now() session.RingState {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.shown) == 0 {
		return session.RingOff
	}
	return l.shown[len(l.shown)-1]
}

func (l *lights) await(t *testing.T, s session.RingState) {
	t.Helper()
	await(t, "the ring to show "+string(s), func() bool { return l.now() == s })
}

// newPanelRig is the rig with one panel shown by both the sessions and the
// listener, as chorusd wires each satellite's ring.
func newPanelRig(t *testing.T, steps []step, tweak ...func(*listen.Config)) (*rig, *lights) {
	t.Helper()
	ring := &lights{}
	panel := session.NewPanel(ring)
	r := newRigSession(t, steps, func(c *session.Config) { c.Ring = panel },
		append([]func(*listen.Config){func(c *listen.Config) { c.Indicator = panel }}, tweak...)...)
	return r, ring
}

func (r *rig) down(err error) {
	r.stt.mu.Lock()
	r.stt.down = err
	r.stt.mu.Unlock()
}

// Alan wakes the kitchen with the speech-to-text sidecar down. His voice
// would have confirmed the wake, so the kitchen tells him it cannot hear
// rather than going silent. The wake stays rejected as transcription_failed
// in the kitchen's log, naming the apology, and no session opens.
//
// verifies SPEC §7, §9.3
func TestAWakeTheEarFailedOnIsApologisedFor(t *testing.T) {
	r, ring := newPanelRig(t, silent())
	r.down(errSpeachesDown)

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("turn on the porch light", alan), 4*chunkBytes)

	if got := r.speaker.wrote(t); got != session.DefaultCanned.Ear {
		t.Errorf("the kitchen said %q, want the ear's canned line", got)
	}
	log := listen.DeviceConversation("kitchen")
	rej := r.awaitKind(t, log, journal.KindWakeRejected, 1)
	if rej.Fields["reason"] != "transcription_failed" || rej.Fields["canned_call_id"] == "" {
		t.Errorf("wake_rejected = %v, want transcription_failed naming its apology", rej.Fields)
	}
	spoken := r.awaitKind(t, log, journal.KindSpeechSpoken, 1)
	if spoken.Fields["call_id"] != rej.Fields["canned_call_id"] {
		t.Errorf("speech_spoken %v is not the apology the rejection names", spoken.Fields)
	}
	if r.l.Session() != nil {
		t.Error("a wake nothing decoded opened a session")
	}
	ring.await(t, session.RingError)
}

// The kitchen's link drops while it is still apologising. The apology is cut
// as any speech is when its device goes, the cut is in the kitchen's log
// with the rejection, and nothing of the listener outlives the link.
//
// verifies SPEC §7, §4.4
func TestAnApologyCutByTheLinkDroppingEndsWithIt(t *testing.T) {
	r, _ := newPanelRig(t, silent())
	r.speaker.hold = true
	r.down(errSpeachesDown)

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("turn on the porch light", alan), 4*chunkBytes)
	r.speaker.wrote(t)
	r.cancel()
	select {
	case <-r.l.Done():
	case <-time.After(patience):
		t.Fatal("the listener outlived its link while apologising")
	}
	log := listen.DeviceConversation("kitchen")
	cut := r.awaitKind(t, log, journal.KindSpeechTruncated, 1)
	if cut.Fields["reason"] != "session_closed" {
		t.Errorf("speech_truncated = %v, want the apology cut by the link's end", cut.Fields)
	}
	if n := r.count(t, log, journal.KindWakeRejected); n != 1 {
		t.Errorf("%d rejections, want the one the apology answered", n)
	}
}

// The television says the wake word while the sidecar is down. Its voice
// would have failed stage three, so the wake is rejected without a word:
// an apology to the television is a false accept the household hears.
//
// verifies SPEC §7, §9.3
func TestAWakeFromAnUnknownVoiceTheEarFailedOnIsNotApologisedFor(t *testing.T) {
	r, _ := newPanelRig(t, silent())
	r.down(errSpeachesDown)

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("and now the weather", stranger), 4*chunkBytes)

	rej := r.awaitKind(t, listen.DeviceConversation("kitchen"), journal.KindWakeRejected, 1)
	if rej.Fields["reason"] != "transcription_failed" || rej.Fields["canned_call_id"] != "" {
		t.Errorf("wake_rejected = %v, want transcription_failed with no apology", rej.Fields)
	}
	r.settled(t)
	if n := r.count(t, listen.DeviceConversation("kitchen"), journal.KindToolCalled); n != 0 {
		t.Errorf("%d speak calls answering the television", n)
	}
}

// Alan is mid-conversation when the sidecar falls over. What he says next is
// answered with the ear's canned line in his conversation, nothing is
// transcribed from words nothing decoded, and the session stays open for
// him to try again; the next decode that works clears the ring.
//
// verifies SPEC §7, §3.3.1
func TestAnUtteranceTheEarFailedOnMidSessionIsApologisedFor(t *testing.T) {
	r, ring := newPanelRig(t, silent())

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("is the garage door shut", alan), 4*chunkBytes)
	s := r.session(t)
	conv := s.ConversationID()
	r.awaitKind(t, conv, journal.KindModelCompleted, 1)

	r.down(errSpeachesDown)
	r.utter(t, r.line("then close it", alan), 4*chunkBytes)
	if got := r.speaker.wrote(t); got != session.DefaultCanned.Ear {
		t.Errorf("the kitchen said %q, want the ear's canned line", got)
	}
	spoken := r.awaitKind(t, conv, journal.KindSpeechSpoken, 1)
	if spoken.Fields["text"] != session.DefaultCanned.Ear {
		t.Errorf("speech_spoken = %v, want the apology in Alan's conversation", spoken.Fields)
	}
	if n := r.count(t, conv, journal.KindUtteranceTranscribed); n != 1 {
		t.Errorf("%d utterances transcribed, want only the first", n)
	}
	if r.l.Session() != s {
		t.Error("the session ended on an utterance the ear failed on")
	}

	r.down(nil)
	r.utter(t, r.line("close the garage door", alan), 4*chunkBytes)
	r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2)
	ring.await(t, session.RingListening)
	if err := s.Close(t.Context(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	ring.await(t, session.RingOff)
}

// Bob presses the kitchen's mute. The device says so, and the ring shows it
// until he unmutes, whether or not anything is being said.
//
// verifies SPEC §3.3.1
func TestTheDevicesMuteIsShownOnTheRing(t *testing.T) {
	r, ring := newPanelRig(t, silent())

	r.dev.SendMute(t, bridge.Mute{Hardware: true})
	ring.await(t, session.RingMuted)
	r.dev.SendMute(t, bridge.Mute{})
	ring.await(t, session.RingOff)
	r.dev.SendMute(t, bridge.Mute{Software: true})
	ring.await(t, session.RingMuted)
}

// brokenDisk is the blob store on a failing disk: every write under one
// prefix fails, as a filesystem returning EIO does.
type brokenDisk struct {
	blob.Store
	prefix string
}

var errEIO = errors.New("write: input/output error")

func (b brokenDisk) Create(ctx context.Context, key string) (blob.Writer, error) {
	if strings.HasPrefix(key, b.prefix) {
		return nil, errEIO
	}
	return b.Store.Create(ctx, key)
}

// The disk fails under Alan's question. His words were decoded, so the
// kitchen answers him anyway; the log names the clip and says at once that
// it was never written.
//
// verifies SPEC §8
func TestAnUtteranceWhoseAudioFailedToWriteIsStillAnswered(t *testing.T) {
	r := newRig(t, talking(), func(c *listen.Config) { c.Blobs = brokenDisk{Store: c.Blobs, prefix: "mic/"} })

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)

	s := r.session(t)
	heard := r.awaitKind(t, s.ConversationID(), journal.KindUtteranceTranscribed, 1)
	dropped := r.awaitKind(t, s.ConversationID(), journal.KindAudioDropped, 1)
	if dropped.Fields["audio_ref"] != heard.AudioRef || dropped.Fields["reason"] != "write_failed" {
		t.Errorf("audio_dropped = %v, want write_failed for %s", dropped.Fields, heard.AudioRef)
	}
	if got := r.speaker.wrote(t); got != line {
		t.Errorf("the kitchen said %q, want its answer", got)
	}
}

// The disk fails under a cough that woke the kitchen. The rejection is
// still the corpus's record of a false accept, so it is journalled, and the
// clip it names is recorded as never written.
//
// verifies SPEC §8, §9.3
func TestARejectedWakeWhoseAudioFailedToWriteIsStillJournalled(t *testing.T) {
	r := newRig(t, silent(), func(c *listen.Config) { c.Blobs = brokenDisk{Store: c.Blobs, prefix: "wake/"} })

	r.dev.SendWake(t, "hey_eddie")
	r.pause(t, rigWake)

	log := listen.DeviceConversation("kitchen")
	rej := r.awaitKind(t, log, journal.KindWakeRejected, 1)
	dropped := r.awaitKind(t, log, journal.KindAudioDropped, 1)
	if rej.Fields["reason"] != "no_speech" || dropped.Fields["audio_ref"] != rej.AudioRef || dropped.Fields["reason"] != "write_failed" {
		t.Errorf("wake_rejected %v then audio_dropped %v, want the clip named and recorded as never written", rej.Fields, dropped.Fields)
	}
	kinds := []journal.Kind{}
	for _, e := range r.events(t, log) {
		kinds = append(kinds, e.Kind)
	}
	if !slices.Equal(kinds, []journal.Kind{journal.KindWakeRejected, journal.KindAudioDropped}) {
		t.Errorf("the kitchen's log holds %v", kinds)
	}
}
