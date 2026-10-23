package listen_test

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/session"
)

var errSpeakerIDDown = errors.New(`speakerid embed: Post "http://10.0.0.22:8890/v1/embed": connect: connection refused`)

func (r *rig) embedderDown(err error) {
	r.emb.mu.Lock()
	r.emb.down = err
	r.emb.mu.Unlock()
}

// faultLog is the kitchen's panel, and every fault the listener raised or
// cleared on it, in order.
type faultLog struct {
	*session.Panel
	mu  sync.Mutex
	log []string
}

func (f *faultLog) Fault(x session.Fault) {
	f.mu.Lock()
	f.log = append(f.log, "fault "+string(x))
	f.mu.Unlock()
	f.Panel.Fault(x)
}

func (f *faultLog) Recover(x session.Fault) {
	f.mu.Lock()
	// Recovered on every answer; only a change is worth a line.
	if n := len(f.log); n == 0 || f.log[n-1] != "recover "+string(x) {
		f.log = append(f.log, "recover "+string(x))
	}
	f.mu.Unlock()
	f.Panel.Recover(x)
}

func (f *faultLog) saw(x string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.log, x)
}

// Alan wakes the kitchen while the speaker-ID sidecar is down. Stage three
// cannot confirm him, so the wake fails closed -- but as speaker_id_failed,
// with its audio, and not as a guest's unknown_speaker: nothing judged his
// voice. The kitchen says nothing, since it cannot tell him from the
// television, and the ring shows the fault. Once the sidecar answers, his
// next wake opens a session and the fault clears.
//
// verifies SPEC §5, §7, §9.3
func TestAWakeTheEmbedderFailedOnIsRejectedAsAFailure(t *testing.T) {
	ring := &lights{}
	panel := &faultLog{Panel: session.NewPanel(ring)}
	r := newRigSession(t, silent(), func(c *session.Config) { c.Ring = panel.Panel },
		func(c *listen.Config) { c.Indicator = panel })
	r.embedderDown(errSpeakerIDDown)

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("turn on the porch light", alan), 4*chunkBytes)

	log := listen.DeviceConversation("kitchen")
	rej := r.awaitKind(t, log, journal.KindWakeRejected, 1)
	if rej.Fields["reason"] != "speaker_id_failed" || rej.Fields["wake_word"] != "hey_eddie" || rej.Fields["canned_call_id"] != "" {
		t.Errorf("wake_rejected = %v, want speaker_id_failed from hey_eddie, unapologised", rej.Fields)
	}
	if _, ok := r.blobs.Bytes(rej.AudioRef); !ok {
		t.Errorf("the rejected wake's audio %q is not stored", rej.AudioRef)
	}
	if r.l.Session() != nil {
		t.Error("a wake nothing attributed opened a session")
	}
	ring.await(t, session.RingError)
	if n := r.count(t, log, journal.KindSpeechSpoken); n != 0 {
		t.Errorf("the kitchen spoke %d times to a voice it could not attribute", n)
	}

	r.embedderDown(nil)
	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("turn on the porch light", alan), 4*chunkBytes)
	if s := r.session(t); s.Speaker() != "alan" {
		t.Errorf("the session is %q's, want alan's", s.Speaker())
	}
	if !panel.saw("fault speaker_id") || !panel.saw("recover speaker_id") {
		t.Errorf("faults = %v, want speaker_id raised and then cleared", panel.log)
	}
}

// Alan is mid-conversation when the sidecar goes down. His next request is
// still heard and still his: the utterance records that the embedder
// failed, so replay keeps him the speaker, and the model is asked as
// before -- an outage makes nobody a guest (ADR-0049, ADR-0097).
//
// verifies SPEC §5
func TestAnUtteranceTheEmbedderFailedOnStaysTheSpeakers(t *testing.T) {
	r := newRig(t, silent())

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("turn on the porch light", alan), 4*chunkBytes)
	s := r.session(t)
	conv := s.ConversationID()
	r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)

	r.embedderDown(errSpeakerIDDown)
	r.utter(t, r.line("and the garage light too", alan), 4*chunkBytes)

	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2)
	if heard.Fields["speaker_match"] != "failed" || heard.Fields["speaker_id"] != "" {
		t.Errorf("utterance_transcribed = %v, want speaker_match failed and no id", heard.Fields)
	}
	if s.Speaker() != "alan" {
		t.Errorf("the session became %q's, want it still alan's", s.Speaker())
	}
	st, err := journal.Replay(t.Context(), r.store, conv, journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if st.Speaker != "alan" {
		t.Errorf("replay attributes the conversation to %q, want alan", st.Speaker)
	}
}

// Alan says "no the other one" over the answer while the sidecar is down.
// Only a recognised voice stops speech, so the answer plays on, and the
// refusal is speaker_id_failed: an outage, not the television, in the gate's
// tuning corpus (ADR-0097).
//
// verifies SPEC §4.3
func TestABargeInTheEmbedderFailedOnIsRefusedAsAFailure(t *testing.T) {
	r := newRig(t, talking())
	r.speaker.hold = true

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)
	s := r.session(t)
	r.speaker.wrote(t)
	await(t, "the speaking child", func() bool { return slices.Contains(s.Children(), "speaking") })
	if err := r.link.SendTTS(quiet(2 * 16000)); err != nil {
		t.Fatal(err)
	}
	r.dev.AwaitTTS(t, 2*16000)
	r.dev.Play(t, 16000)

	r.embedderDown(errSpeakerIDDown)
	r.speak(t, r.line("no the other one", alan), 2*rigPartials)

	conv := s.ConversationID()
	rej := r.awaitKind(t, conv, journal.KindBargeInRejected, 1)
	if rej.Fields["stage"] != "speaker_id_failed" {
		t.Errorf("barge_in_rejected = %v, want speaker_id_failed", rej.Fields)
	}
	if n := r.count(t, conv, journal.KindBargeInDetected); n != 0 {
		t.Errorf("%d barge-ins detected from a voice nothing judged", n)
	}
}
