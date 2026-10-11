package satellite_test

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/satellite"
	"github.com/teagan42/chorus/internal/session"
)

// Every canned line can be said from the binary alone: each segment the
// satellite cuts it into has audio built in, and nothing built in is left
// over from a line since reworded. A wording change without `task
// canned:voice` fails here.
//
// verifies SPEC §7
func TestEveryCannedLineHasAudioBuiltIn(t *testing.T) {
	named := map[string]bool{}
	for _, line := range session.DefaultCanned.Lines() {
		for _, part := range satellite.Segments(line) {
			named[satellite.FallbackName(part)] = true
			pcm, ok := satellite.Fallback(part)
			if !ok {
				t.Errorf("%q of %q has no audio built in; run task canned:voice", part, line)
				continue
			}
			if len(pcm) == 0 || len(pcm)%2 != 0 || bytes.Count(pcm, []byte{0}) == len(pcm) {
				t.Errorf("%q has %d bytes of audio, want whole 16-bit samples that are not silence", part, len(pcm))
			}
		}
	}
	files, err := os.ReadDir("fallback")
	if err != nil {
		t.Fatalf("read the built-in audio: %v", err)
	}
	for _, f := range files {
		if !named[f.Name()] {
			t.Errorf("fallback/%s is no canned segment's; run task canned:voice", f.Name())
		}
	}
	if _, ok := satellite.Fallback("The front door is locked."); ok {
		t.Error("text no canned line says has audio built in")
	}
}

// Kokoro is not up yet when the daemon starts, and the kitchen's voice
// fails before the startup render ever succeeded. The apology still plays,
// whole, from the audio built into the binary.
//
// verifies SPEC §7
func TestTheApologyPlaysFromBuiltInAudioBeforeKokoroAnswers(t *testing.T) {
	kokoro := &dyingKokoro{}
	kokoro.die()
	canned := satellite.NewCanned(kokoro)

	r := newRig(t, func(c *satellite.Config) { c.Synth = canned })
	st, _ := r.open(t, "cn_3f9c2a10")
	line := session.DefaultCanned.Voice
	if err := st.Write(line); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := 0
	for _, part := range satellite.Segments(line) {
		pcm, _ := satellite.Fallback(part)
		want += len(pcm)
	}
	r.dev.AwaitTTS(t, want)
	done := closeAsync(st)
	r.dev.AwaitFinish(t, 1)
	r.playAll(t)

	if pb := await(t, done); pb.Truncated || pb.Spoken != line || pb.Failure != nil {
		t.Errorf("playback = %+v, want the apology played whole", pb)
	}
	if _, err := canned.Synthesize(t.Context(), "The front door is locked."); !errors.Is(err, errKokoroDown) {
		t.Errorf("text no canned line says = %v, want Kokoro's own failure", err)
	}
}

// Once Kokoro answers the startup render, the apology is said in the voice
// it rendered, not the one built in.
//
// verifies SPEC §7
func TestARenderedApologyOutranksTheBuiltInOne(t *testing.T) {
	kokoro := &dyingKokoro{}
	canned := satellite.NewCanned(kokoro)
	lines := session.DefaultCanned.Lines()
	if err := canned.Render(t.Context(), lines...); err != nil {
		t.Fatalf("render: %v", err)
	}
	kokoro.die()
	for _, part := range satellite.Segments(session.DefaultCanned.Ear) {
		pcm, err := canned.Synthesize(t.Context(), part)
		if err != nil || len(pcm) != len(part)*framesPerByte*2 {
			t.Errorf("%q = %d bytes, %v; want Kokoro's rendering", strings.TrimSpace(part), len(pcm), err)
		}
	}
}
