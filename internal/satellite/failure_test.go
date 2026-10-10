package satellite_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/satellite"
	"github.com/teagan42/chorus/internal/session"
)

// dyingKokoro renders like fakeSynth until it is asked for text containing
// dieAt, and fails that and everything after it, as a Kokoro that falls over
// mid-answer does.
type dyingKokoro struct {
	fakeSynth
	dieAt string

	mu   sync.Mutex
	dead bool
}

var errKokoroDown = errors.New("kokoro: 503 Service Unavailable")

func (k *dyingKokoro) Synthesize(ctx context.Context, text string) ([]byte, error) {
	k.mu.Lock()
	k.dead = k.dead || (k.dieAt != "" && strings.Contains(text, k.dieAt))
	dead := k.dead
	k.mu.Unlock()
	if dead {
		return nil, errKokoroDown
	}
	return k.fakeSynth.Synthesize(ctx, text)
}

func (k *dyingKokoro) die() {
	k.mu.Lock()
	k.dead = true
	k.mu.Unlock()
}

// Kokoro answers the first clause of the forecast and falls over on the
// second. The device played what it was sent; nobody cut it, so the stream
// reports the voice failing with Kokoro's own error, and the session will
// never take it for a barge-in.
//
// verifies SPEC §7, §9.1
func TestKokoroFallingOverMidAnswerIsReportedAsTheVoiceFailing(t *testing.T) {
	kokoro := &dyingKokoro{dieAt: "nineteen"}
	r := newRig(t, func(c *satellite.Config) { c.Synth = kokoro })
	st, _ := r.open(t, "call_s1")

	const first, rest = "Tomorrow will be sunny, ", "with a high of nineteen."
	if err := st.Write(first + rest); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len(first)*framesPerByte*2)
	done := closeAsync(st)
	r.dev.AwaitFinish(t, 1)
	r.playAll(t)

	pb := await(t, done)
	if !pb.Truncated || pb.Spoken != first || pb.Unspoken != rest {
		t.Errorf("playback = %+v, want %q heard and %q not", pb, first, rest)
	}
	if !errors.Is(pb.Failure, session.ErrVoiceUnavailable) || !strings.Contains(pb.Failure.Error(), "503") {
		t.Errorf("Failure = %v, want the voice failing with Kokoro's error", pb.Failure)
	}
	if pb.Frames != int64(len(first)*framesPerByte) {
		t.Errorf("Frames = %d, want the clause the DAC played", pb.Frames)
	}
}

// The device goes quiet partway through and never confirms the rest. That
// is not the voice and not the person: it is named as unconfirmed playback.
//
// verifies SPEC §3.2.1, §7
func TestADeviceThatStopsReportingIsUnconfirmedPlayback(t *testing.T) {
	r := newRig(t)
	st, _ := r.open(t, "call_s1")

	if err := st.Write("The dishwasher finished at nine."); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len("The dishwasher finished at nine.")*framesPerByte*2)
	r.play(t, 4*framesPerByte)
	done := closeAsync(st)
	r.dev.AwaitFinish(t, 1)
	r.drain <- time.Now()

	pb := await(t, done)
	if !pb.Truncated || !errors.Is(pb.Failure, session.ErrPlaybackUnconfirmed) {
		t.Errorf("playback = %+v, want truncated as unconfirmed playback", pb)
	}
}

// A barge-in is the person, not a failure, even when the synthesiser was
// cancelled under it.
//
// verifies SPEC §4.4
func TestABargeInIsNoFailure(t *testing.T) {
	r := newRig(t)
	r.synth.release = make(chan struct{})
	r.synth.entered = make(chan string, 4)
	st, cancel := r.open(t, "call_s1")

	if err := st.Write("I found three albums by that artist."); err != nil {
		t.Fatalf("write: %v", err)
	}
	<-r.synth.entered
	cancel()
	pb := await(t, closeAsync(st))
	if !pb.Truncated || pb.Failure != nil {
		t.Errorf("playback = %+v, want a cut with no failure", pb)
	}
}

// The voice's apology is rendered while Kokoro answers, at startup. When
// Kokoro later falls over, the apology still plays, from that audio; any
// other text still goes to Kokoro, and fails there.
//
// verifies SPEC §7
func TestTheApologyRenderedAtStartupPlaysWithKokoroDown(t *testing.T) {
	kokoro := &dyingKokoro{}
	canned := satellite.NewCanned(kokoro)
	if err := canned.Render(t.Context(), session.DefaultCanned.Lines()...); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !canned.Rendered(session.DefaultCanned.Lines()...) {
		t.Fatal("Rendered = false after a render that succeeded")
	}
	kokoro.die()

	r := newRig(t, func(c *satellite.Config) { c.Synth = canned })
	st, _ := r.open(t, "cn_3f9c2a10")
	line := session.DefaultCanned.Voice
	if err := st.Write(line); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len(line)*framesPerByte*2)
	done := closeAsync(st)
	r.dev.AwaitFinish(t, 1)
	r.playAll(t)

	if pb := await(t, done); pb.Truncated || pb.Spoken != line || pb.Failure != nil {
		t.Errorf("playback = %+v, want the apology played whole", pb)
	}
	if _, err := canned.Synthesize(t.Context(), "The front door is locked."); !errors.Is(err, errKokoroDown) {
		t.Errorf("unrendered text = %v, want Kokoro's own failure", err)
	}
}

// Kokoro is still starting when the daemon is. The render fails, keeps what
// it managed, and a later try finishes the rest without rendering anything
// twice.
//
// verifies SPEC §7
func TestRenderingTheApologyFinishesOnALaterTry(t *testing.T) {
	kokoro := &dyingKokoro{dieAt: "voice"}
	canned := satellite.NewCanned(kokoro)
	lines := session.DefaultCanned.Lines()

	if err := canned.Render(t.Context(), lines...); !errors.Is(err, errKokoroDown) {
		t.Fatalf("render = %v, want Kokoro's failure", err)
	}
	if canned.Rendered(lines...) {
		t.Fatal("Rendered = true with a segment missing")
	}

	kokoro.mu.Lock()
	kokoro.dead, kokoro.dieAt = false, ""
	kokoro.mu.Unlock()
	if err := canned.Render(t.Context(), lines...); err != nil {
		t.Fatalf("second render: %v", err)
	}
	if !canned.Rendered(lines...) {
		t.Fatal("Rendered = false after the second render")
	}
	seen := map[string]int{}
	kokoro.fakeSynth.mu.Lock()
	for _, c := range kokoro.calls {
		seen[c]++
	}
	kokoro.fakeSynth.mu.Unlock()
	for text, n := range seen {
		if n > 1 {
			t.Errorf("%q rendered %d times, want once", text, n)
		}
	}
}
