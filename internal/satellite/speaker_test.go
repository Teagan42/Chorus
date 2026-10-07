package satellite_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/blob"
	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/bridge/bridgetest"
	"github.com/teaganglenn/chorus/internal/satellite"
	"github.com/teaganglenn/chorus/internal/session"
)

// framesPerByte scales the fake synthesiser so a delta's audio is long enough
// to be cut partway through.
const framesPerByte = 100

// fakeSynth renders each byte of text as framesPerByte frames of silence, so a
// frame count maps back to a text offset arithmetically.
type fakeSynth struct {
	mu      sync.Mutex
	err     error
	release chan struct{} // nil never blocks; otherwise held until closed
	calls   []string
	// entered reports each call as it starts, so a test can be sure the feeder
	// is inside the synthesiser before it provokes a barge-in.
	entered chan string
	// deaf models a synthesiser that does not honour cancellation, such as an
	// HTTP call already in flight. Barge-in latency may not depend on it.
	deaf bool
}

func (f *fakeSynth) Synthesize(ctx context.Context, text string) ([]byte, error) {
	f.mu.Lock()
	err, release, deaf, entered := f.err, f.release, f.deaf, f.entered
	f.calls = append(f.calls, text)
	f.mu.Unlock()
	if entered != nil {
		entered <- text
	}
	if release != nil {
		done := ctx.Done()
		if deaf {
			done = nil
		}
		select {
		case <-release:
		case <-done:
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return make([]byte, len(text)*framesPerByte*2), nil
}

// Deadlines the rig configures, chosen far apart so fakeTimers can tell them
// from the sub-second pacing waits and from each other.
const (
	rigDrain  = 30 * time.Second
	rigSettle = 5 * time.Second
)

// fakeTimers fires every pacing wait at once and hands the two deadlines a
// test may care about to the test itself, which reaches them deliberately
// rather than by waiting. Pacing waits are recorded as they are asked for,
// because how long they are is the only evidence of how the downlink is paced.
type fakeTimers struct {
	drain, settle chan time.Time
	paced         *pacing
}

func (f fakeTimers) After(d time.Duration) <-chan time.Time {
	switch {
	case d >= rigDrain:
		return f.drain
	case d >= rigSettle:
		return f.settle
	default:
		if f.paced != nil {
			f.paced.add(d)
		}
		fired := make(chan time.Time, 1)
		fired <- time.Now()
		return fired
	}
}

// pacing collects the pacing waits the feeder asked for, in order.
type pacing struct {
	mu  sync.Mutex
	saw []time.Duration
}

func (p *pacing) add(d time.Duration) {
	p.mu.Lock()
	p.saw = append(p.saw, d)
	p.mu.Unlock()
}

func (p *pacing) total() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	var sum time.Duration
	for _, d := range p.saw {
		sum += d
	}
	return sum
}

type rig struct {
	sat    *satellite.Satellite
	dev    *bridgetest.Device
	synth  *fakeSynth
	blobs  *blob.Memory
	paced  *pacing
	drain  chan time.Time
	settle chan time.Time
	served chan error
}

// newRig wires a satellite to an in-process device. By default the post-stop
// settle expires at once; a tweak can lengthen it to assert what the wait is
// for.
func newRig(t *testing.T, tweak ...func(*satellite.Config)) *rig {
	t.Helper()
	link, dev := bridgetest.Dial(t, 2)
	synth := &fakeSynth{}
	r := &rig{
		dev: dev, synth: synth, blobs: blob.NewMemory(), served: make(chan error, 1),
		paced: &pacing{},
		drain: make(chan time.Time), settle: make(chan time.Time),
	}

	cfg := satellite.Config{
		Link:   link,
		Synth:  synth,
		Timers: fakeTimers{drain: r.drain, settle: r.settle, paced: r.paced},
		Blobs:  r.blobs,
		Drain:  rigDrain,
	}
	for _, f := range tweak {
		f(&cfg)
	}
	sat, err := satellite.New(cfg)
	if err != nil {
		t.Fatalf("new satellite: %v", err)
	}
	r.sat = sat

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { r.served <- link.Serve(ctx, sat) }()
	return r
}

// open starts an utterance and returns its stream and the cancel that a
// barge-in would call.
func (r *rig) open(t *testing.T, callID string) (session.Stream, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := r.sat.Open(ctx, callID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return st, cancel
}

// close runs Close off the test goroutine, because on the normal path it waits
// for the DAC and the test is what advances it.
func closeAsync(st session.Stream) <-chan session.Playback {
	out := make(chan session.Playback, 1)
	go func() { out <- st.Close() }()
	return out
}

func await(t *testing.T, out <-chan session.Playback) session.Playback {
	t.Helper()
	select {
	case pb := <-out:
		return pb
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
		return session.Playback{}
	}
}

// verifies SPEC §3.2.1
func TestCompletedUtteranceReportsTheDeviceFrameCount(t *testing.T) {
	r := newRig(t)
	st, _ := r.open(t, "call-1")

	if err := st.Write("hello"); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := uint64(len("hello") * framesPerByte)
	r.dev.AwaitTTS(t, int(want)*2)

	done := closeAsync(st)
	r.dev.AwaitFinish(t, 1)
	r.dev.PlayAll(t)

	pb := await(t, done)
	if pb.Truncated {
		t.Errorf("Truncated = true, want false for a fully played utterance")
	}
	if pb.Spoken != "hello" || pb.Unspoken != "" {
		t.Errorf("split = (%q, %q), want (%q, %q)", pb.Spoken, pb.Unspoken, "hello", "")
	}
	// The point of the whole adapter: the count came from the device, not from
	// len(text) arithmetic on the host.
	if pb.Frames != int64(want) {
		t.Errorf("Frames = %d, want %d", pb.Frames, want)
	}
}

// verifies SPEC §4.4
func TestBargeInTruncatesAtTheDACPosition(t *testing.T) {
	r := newRig(t)
	st, cancel := r.open(t, "call-1")

	for _, delta := range []string{"Hello ", "world."} {
		if err := st.Write(delta); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	total := len("Hello world.") * framesPerByte
	r.dev.AwaitTTS(t, total*2)

	// Cut partway through the first delta.
	const heard = 3 * framesPerByte
	if got := r.dev.Play(t, heard); got != heard {
		t.Fatalf("played %d frames, want %d", got, heard)
	}

	cancel()
	pb := await(t, closeAsync(st))

	if !pb.Truncated {
		t.Error("Truncated = false, want true")
	}
	if pb.Frames != heard {
		t.Errorf("Frames = %d, want %d", pb.Frames, heard)
	}
	// The cut fell inside "Hello ", which the user did partly hear, so it is
	// spoken. Only the delta that never reached the DAC is unspoken.
	if pb.Spoken != "Hello " || pb.Unspoken != "world." {
		t.Errorf("split = (%q, %q), want (%q, %q)", pb.Spoken, pb.Unspoken, "Hello ", "world.")
	}
	// Whatever the device still holds has to be discarded, or it plays over
	// the next utterance.
	r.dev.AwaitStop(t, 1)
	if n := r.dev.Pending(); n != 0 {
		t.Errorf("device still holds %d frames after the stop", n)
	}
}

// verifies SPEC §4.4
func TestBargeInBeforeAnyAudioPlayedSpeaksNothing(t *testing.T) {
	r := newRig(t)
	st, cancel := r.open(t, "call-1")

	if err := st.Write("never heard"); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, 2)

	cancel()
	pb := await(t, closeAsync(st))

	if pb.Spoken != "" {
		t.Errorf("Spoken = %q, want empty: the DAC never reported a frame", pb.Spoken)
	}
	if pb.Unspoken != "never heard" {
		t.Errorf("Unspoken = %q, want %q", pb.Unspoken, "never heard")
	}
	if pb.Frames != 0 || !pb.Truncated {
		t.Errorf("Frames = %d, Truncated = %v, want 0, true", pb.Frames, pb.Truncated)
	}
}

// speakToolDelta is how a sentence actually arrives: the speak tool hands the
// whole thing over in one call (internal/session/session.go).
const speakToolDelta = "The weather today is sunny with a high of twenty degrees."

// verifies SPEC §4.4
func TestOneDeltaIsCutInsideItself(t *testing.T) {
	r := newRig(t)
	st, cancel := r.open(t, "call-1")

	if err := st.Write(speakToolDelta); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len(speakToolDelta)*framesPerByte*2)
	// Two characters in. The user heard "Th".
	r.dev.Play(t, 2*framesPerByte)

	cancel()
	pb := await(t, closeAsync(st))

	if !pb.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	// A whole delta credited as spoken teaches the corpus that the user heard a
	// sentence they were two characters into (SPEC §15).
	if pb.Spoken == speakToolDelta {
		t.Error("Spoken is the entire utterance after a cut two characters in")
	}
	// speech_truncated requires unspoken_text and journal.Append rejects an
	// empty required field, so nothing-unspoken records nothing at all.
	if pb.Unspoken == "" {
		t.Error("Unspoken is empty: journal.Append would reject the event")
	}
	if pb.Spoken+pb.Unspoken != speakToolDelta {
		t.Errorf("split = (%q, %q), does not rejoin to the utterance", pb.Spoken, pb.Unspoken)
	}
}

// verifies SPEC §4.2
func TestTheCutFallsOnAWordBoundary(t *testing.T) {
	r := newRig(t)
	st, cancel := r.open(t, "call-1")

	const text = "Sure, I can do that, but the kitchen light is already off."
	if err := st.Write(text); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len(text)*framesPerByte*2)
	// Inside the second clause.
	r.dev.Play(t, uint64((len("Sure, ")+3)*framesPerByte))

	cancel()
	pb := await(t, closeAsync(st))

	// Half a word recorded as heard is a token the user never got, and the
	// unspoken half then starts mid-word.
	if pb.Spoken == "" || !strings.HasSuffix(pb.Spoken, " ") {
		t.Errorf("Spoken = %q, want it to end on a word boundary", pb.Spoken)
	}
	if !strings.HasPrefix(text, pb.Spoken) {
		t.Errorf("Spoken = %q, want a prefix of the utterance", pb.Spoken)
	}
	if pb.Spoken+pb.Unspoken != text {
		t.Errorf("split = (%q, %q), does not rejoin to the utterance", pb.Spoken, pb.Unspoken)
	}
}

// verifies SPEC §4.4
func TestAFullyPlayedMultiSegmentDeltaIsWhollySpoken(t *testing.T) {
	r := newRig(t)
	st, _ := r.open(t, "call-1")

	if err := st.Write(speakToolDelta); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len(speakToolDelta)*framesPerByte*2)
	done := closeAsync(st)
	r.dev.AwaitFinish(t, 1)
	r.dev.PlayAll(t)
	pb := await(t, done)

	if pb.Truncated {
		t.Error("Truncated = true, want false: the DAC played all of it")
	}
	if pb.Spoken != speakToolDelta || pb.Unspoken != "" {
		t.Errorf("split = (%q, %q), want the whole utterance spoken", pb.Spoken, pb.Unspoken)
	}
	// Segmenting only moves where a cut may land. What the device was sent, what
	// the journal counts and what the blob holds are all unchanged.
	if want := int64(len(speakToolDelta) * framesPerByte); pb.Frames != want {
		t.Errorf("Frames = %d, want %d", pb.Frames, want)
	}
	stored, ok := r.blobs.Bytes(pb.AudioRef)
	if !ok {
		t.Fatalf("AudioRef %q points at no blob", pb.AudioRef)
	}
	if want := len(speakToolDelta) * framesPerByte * 2; len(stored) != want {
		t.Errorf("blob is %d bytes, want %d", len(stored), want)
	}
	if !bytes.Equal(stored, r.dev.TTS()) {
		t.Error("the stored blob is not the audio the device was sent")
	}
}

// verifies SPEC §8
func TestSpeechCarriesABlobReferenceTheJournalWillAccept(t *testing.T) {
	r := newRig(t)
	st, _ := r.open(t, "call-1")

	if err := st.Write("hello"); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len("hello")*framesPerByte*2)
	done := closeAsync(st)
	r.dev.AwaitFinish(t, 1)
	r.dev.PlayAll(t)
	pb := await(t, done)

	// journal.Append rejects an audio-bearing event without a reference, and
	// speech.spoken is audio-bearing, so an empty ref here loses the record.
	if pb.AudioRef == "" {
		t.Fatal("AudioRef is empty: journal.Append would reject this event")
	}
	stored, ok := r.blobs.Bytes(pb.AudioRef)
	if !ok {
		t.Fatalf("AudioRef %q points at no blob", pb.AudioRef)
	}
	// The reference has to resolve to the audio actually rendered, or the
	// corpus is annotated against the wrong sound.
	if want := len("hello") * framesPerByte * 2; len(stored) != want {
		t.Errorf("blob is %d bytes, want %d", len(stored), want)
	}
	if !bytes.Equal(stored, r.dev.TTS()) {
		t.Error("the stored blob is not the audio the device was sent")
	}
}

// verifies SPEC §9.1
func TestACutKeepsTheUnheardAudioItHadAlreadyRendered(t *testing.T) {
	r := newRig(t)
	st, cancel := r.open(t, "call-1")

	for _, delta := range []string{"Hello ", "world."} {
		if err := st.Write(delta); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	rendered := len("Hello world.") * framesPerByte * 2
	r.dev.AwaitTTS(t, rendered)

	const heard = 3 * framesPerByte
	r.dev.Play(t, heard)
	cancel()
	pb := await(t, closeAsync(st))

	stored, ok := r.blobs.Bytes(pb.AudioRef)
	if !ok {
		t.Fatalf("AudioRef %q points at no blob", pb.AudioRef)
	}
	// Everything rendered is kept, not just the heard prefix: Frames marks
	// where the ear stopped so a reader can trim, and the tail beyond it is the
	// unspoken half of the preference pair. Trimming here would destroy it.
	if len(stored) != rendered {
		t.Errorf("blob is %d bytes, want all %d rendered", len(stored), rendered)
	}
	if pb.Frames != heard {
		t.Errorf("Frames = %d, want %d to mark the cut inside the blob", pb.Frames, heard)
	}
}

// verifies SPEC §8
func TestAnUtteranceNothingWasHeardOfLeavesNoBlob(t *testing.T) {
	r := newRig(t)
	st, cancel := r.open(t, "call-1")

	if err := st.Write("never heard"); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, 2)
	cancel()
	pb := await(t, closeAsync(st))

	// The session journals this as speech_discarded, which carries no reference
	// (schema/event.cue), so audio committed here is audio no event can name:
	// unreachable by replay or export, and growing on disk.
	if pb.AudioRef != "" {
		t.Errorf("AudioRef = %q, want empty: no event will carry it", pb.AudioRef)
	}
	if refs := r.blobs.Refs(); len(refs) != 0 {
		t.Errorf("store holds %v, want nothing", refs)
	}
}

// verifies SPEC §8
func TestAReusedCallIDDoesNotOverwriteEarlierAudio(t *testing.T) {
	r := newRig(t)

	// Call ids come from the engine, which only promises they group an
	// utterance's deltas. A second turn may reuse one, and a key that trusted
	// it would repoint every earlier reference at the new audio.
	var refs []string
	var sent int
	for i, text := range []string{"first", "second"} {
		st, _ := r.open(t, "call-reused")
		if err := st.Write(text); err != nil {
			t.Fatalf("write: %v", err)
		}
		sent += len(text) * framesPerByte * 2
		r.dev.AwaitTTS(t, sent)
		done := closeAsync(st)
		r.dev.AwaitFinish(t, i+1)
		r.dev.PlayAll(t)
		refs = append(refs, await(t, done).AudioRef)
	}

	if refs[0] == refs[1] {
		t.Fatalf("both utterances stored at %q: the second overwrote the first", refs[0])
	}
	for i, want := range []string{"first", "second"} {
		stored, ok := r.blobs.Bytes(refs[i])
		if !ok {
			t.Fatalf("AudioRef %q points at no blob", refs[i])
		}
		if n := len(want) * framesPerByte * 2; len(stored) != n {
			t.Errorf("blob %d is %d bytes, want %d for %q", i, len(stored), n, want)
		}
	}
}

// verifies SPEC §8
func TestOpenFailsWhenTheAudioCannotBeStored(t *testing.T) {
	r := newRig(t, func(c *satellite.Config) { c.Blobs = failingStore{} })
	// A session that cannot keep the audio must not speak: the event recording
	// the speech would be rejected afterwards anyway, so failing at Open
	// surfaces it before the device makes a sound.
	if _, err := r.sat.Open(context.Background(), "call-1"); err == nil {
		t.Error("Open succeeded with nowhere to store audio, want an error")
	}
}

// failingStore has nowhere to put audio, like a full or unmounted disk.
type failingStore struct{}

func (failingStore) Create(context.Context, string) (blob.Writer, error) {
	return nil, errors.New("no space left on device")
}

func (failingStore) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("no space left on device")
}

// verifies SPEC §4.4
func TestBargeInStopsTheDeviceBeforeWaitingOnSynthesis(t *testing.T) {
	r := newRig(t)
	held := make(chan struct{})
	r.synth.mu.Lock()
	// Deaf to cancellation, like an HTTP request already in flight. The user's
	// ears may not wait on it.
	r.synth.release, r.synth.deaf, r.synth.entered = held, true, make(chan string, 1)
	r.synth.mu.Unlock()

	st, cancel := r.open(t, "call-1")
	if err := st.Write("held in synthesis"); err != nil {
		t.Fatalf("write: %v", err)
	}
	<-r.synth.entered // the feeder is now parked inside Synthesize

	cancel()
	done := closeAsync(st)

	// The stop has to reach the device while the synthesiser is still stuck,
	// because the device is playing buffered audio the whole time.
	r.dev.AwaitStop(t, 1)

	close(held)
	await(t, done)
}

// verifies SPEC §4.4
func TestTheCutUsesTheFinalPositionAfterTheStop(t *testing.T) {
	// A long settle, so the split must come from the device's post-stop report
	// rather than from the deadline expiring.
	r := newRig(t, func(c *satellite.Config) { c.Settle = rigSettle })
	st, cancel := r.open(t, "call-1")

	for _, delta := range []string{"Hello ", "world."} {
		if err := st.Write(delta); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	r.dev.AwaitTTS(t, len("Hello world.")*framesPerByte*2)

	// Two frames reported before the barge-in, and the DAC gets through the
	// rest of the first delta while the stop is in flight. Those frames were
	// heard: a split sampled before the stop would call them unspoken.
	const before = 2 * framesPerByte
	r.dev.Play(t, before)
	r.dev.PlayDuringStop(4 * framesPerByte)

	cancel()
	pb := await(t, closeAsync(st))

	if want := int64(len("Hello ") * framesPerByte); pb.Frames != want {
		t.Errorf("Frames = %d, want %d: the position after the stop is the cut", pb.Frames, want)
	}
	if pb.Spoken != "Hello " || pb.Unspoken != "world." {
		t.Errorf("split = (%q, %q), want (%q, %q)", pb.Spoken, pb.Unspoken, "Hello ", "world.")
	}
	if !pb.Truncated {
		t.Error("Truncated = false, want true")
	}
}

// verifies SPEC §4.4
func TestADeviceThatGoesQuietAfterTheStopStillReportsACut(t *testing.T) {
	r := newRig(t, func(c *satellite.Config) { c.Settle = rigSettle })
	st, cancel := r.open(t, "call-1")

	if err := st.Write("Hello world."); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len("Hello world.")*framesPerByte*2)
	const heard = 3 * framesPerByte
	r.dev.Play(t, heard)

	cancel()
	done := closeAsync(st)
	r.dev.AwaitStop(t, 1)

	// The firmware only reports when the position moved, so a stop that lands
	// between reports is answered with silence. Expiring must degrade to the
	// position already known, not wedge the session.
	r.settle <- time.Now()

	pb := await(t, done)
	if pb.Frames != heard || !pb.Truncated {
		t.Errorf("Frames = %d, Truncated = %v, want %d, true", pb.Frames, pb.Truncated, heard)
	}
}

// verifies SPEC §4.2
func TestADeltaCutBeforeSynthesisIsStillUnspokenText(t *testing.T) {
	r := newRig(t)
	st, cancel := r.open(t, "call-1")

	if err := st.Write("spoken "); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len("spoken ")*framesPerByte*2)
	r.dev.PlayAll(t)

	// Held in the synthesiser, so this delta has no frames of its own.
	r.synth.mu.Lock()
	r.synth.release = make(chan struct{})
	r.synth.mu.Unlock()
	if err := st.Write("stuck in synthesis"); err != nil {
		t.Fatalf("write: %v", err)
	}

	cancel()
	pb := await(t, closeAsync(st))

	if pb.Spoken != "spoken " {
		t.Errorf("Spoken = %q, want %q", pb.Spoken, "spoken ")
	}
	// Generated but never rendered is still generated: dropping it here would
	// lose the unspoken half of the preference pair.
	if pb.Unspoken != "stuck in synthesis" {
		t.Errorf("Unspoken = %q, want %q", pb.Unspoken, "stuck in synthesis")
	}
}

// verifies SPEC §4.2
func TestAnOverflowingDeltaKeepsItsWholeTailAsUnspoken(t *testing.T) {
	r := newRig(t)
	r.synth.mu.Lock()
	r.synth.release = make(chan struct{})
	r.synth.entered = make(chan string, 1)
	r.synth.mu.Unlock()

	st, cancel := r.open(t, "call-1")

	// More sentences than the queue holds, so the queue fills partway through
	// one delta. A speak call's text has no length limit.
	text := strings.Repeat("One more. ", 300)
	if err := st.Write(text); err == nil {
		t.Fatal("write: nil error, want the overflow reported")
	}
	<-r.synth.entered
	// After the hole, nothing may be queued: it would play past missing words.
	if err := st.Write("And later."); err == nil {
		t.Error("write after overflow: nil error, want it refused")
	}

	cancel()
	pb := await(t, closeAsync(st))

	if pb.Spoken != "" {
		t.Errorf("Spoken = %q, want nothing: no audio reached the DAC", pb.Spoken)
	}
	// Generated text past the overflow is still generated: losing it would
	// drop most of the unspoken half of the preference pair.
	if want := text + "And later."; pb.Unspoken != want {
		t.Errorf("Unspoken has %d bytes, want all %d generated", len(pb.Unspoken), len(want))
	}
}

// verifies SPEC §3.2.1
func TestASecondUtteranceRebasesOnTheCumulativePosition(t *testing.T) {
	r := newRig(t)

	first, _ := r.open(t, "call-1")
	if err := first.Write("one"); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len("one")*framesPerByte*2)
	done := closeAsync(first)
	r.dev.AwaitFinish(t, 1)
	r.dev.PlayAll(t)
	if pb := await(t, done); pb.Frames != int64(len("one")*framesPerByte) {
		t.Fatalf("first utterance Frames = %d", pb.Frames)
	}

	// PLAYED is cumulative for the whole connection (internal/bridge/frame.go),
	// so an utterance that did not subtract its starting position would report
	// the previous one's audio as its own.
	second, _ := r.open(t, "call-2")
	if err := second.Write("two"); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := int64(len("two") * framesPerByte)
	r.dev.AwaitTTS(t, (len("one")+len("two"))*framesPerByte*2)
	done = closeAsync(second)
	r.dev.AwaitFinish(t, 2)
	r.dev.PlayAll(t)

	pb := await(t, done)
	if pb.Frames != want {
		t.Errorf("second utterance Frames = %d, want %d", pb.Frames, want)
	}
	if pb.Truncated {
		t.Error("Truncated = true, want false")
	}
}

// verifies SPEC §3.3.2
func TestPacingNeverOutlastsTheAudioItPaced(t *testing.T) {
	r := newRig(t)
	st, _ := r.open(t, "call-1")

	// Eight short deltas, each rendering to well under one slice of audio. A
	// streaming model emits words, so this is the ordinary case.
	var chars int
	for _, delta := range []string{"a, ", "b, ", "c, ", "d, ", "e, ", "f, ", "g, ", "h."} {
		if err := st.Write(delta); err != nil {
			t.Fatalf("write: %v", err)
		}
		chars += len(delta)
	}
	r.dev.AwaitTTS(t, chars*framesPerByte*2)

	done := closeAsync(st)
	r.dev.AwaitFinish(t, 1)
	r.dev.PlayAll(t)
	await(t, done)

	// Pacing exists to stop the downlink crowding the mic uplink off the shared
	// radio. Waiting longer in total than the utterance itself lasts inverts it:
	// the radio goes idle, the mixer underruns, and every later delta is late.
	audio := time.Duration(chars*framesPerByte) * time.Second / bridge.SampleRate
	if total := r.paced.total(); total > audio {
		t.Errorf("paced %v of waits for %v of audio", total, audio)
	}
}

// verifies SPEC §4.1
func TestWriteDoesNotBlockOnSynthesis(t *testing.T) {
	r := newRig(t)
	held := make(chan struct{})
	r.synth.mu.Lock()
	r.synth.release = held
	r.synth.mu.Unlock()

	st, _ := r.open(t, "call-1")

	// Write runs under the speech channel's lock (internal/session/speechchan.go),
	// so blocking here would stall every other child of the session.
	returned := make(chan error, 1)
	go func() {
		_ = st.Write("first")
		returned <- st.Write("second")
	}()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("write: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked while the synthesiser was busy")
	}
	close(held)
}

// verifies SPEC §4.4
func TestDrainDeadlineReportsTruncationRatherThanHanging(t *testing.T) {
	r := newRig(t)
	st, _ := r.open(t, "call-1")

	if err := st.Write("half heard text"); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len("half heard text")*framesPerByte*2)
	r.dev.Play(t, 4*framesPerByte)

	done := closeAsync(st)
	r.dev.AwaitFinish(t, 1)

	// A device that stops reporting must not wedge the session. Audio it never
	// confirmed is audio the user cannot be said to have heard.
	r.drain <- time.Now()

	pb := await(t, done)
	if !pb.Truncated {
		t.Error("Truncated = false, want true: the DAC never confirmed the tail")
	}
	if pb.Frames != 4*framesPerByte {
		t.Errorf("Frames = %d, want %d", pb.Frames, 4*framesPerByte)
	}
	if !strings.HasPrefix("half heard text", pb.Spoken) || pb.Spoken == "" {
		t.Errorf("Spoken = %q, want a prefix of the utterance", pb.Spoken)
	}
}

func TestNewRejectsIncompleteWiring(t *testing.T) {
	link, _ := bridgetest.Dial(t, 1)
	for name, cfg := range map[string]satellite.Config{
		"link":   {Synth: &fakeSynth{}, Timers: fakeTimers{}, Blobs: blob.NewMemory()},
		"synth":  {Link: link, Timers: fakeTimers{}, Blobs: blob.NewMemory()},
		"timers": {Link: link, Synth: &fakeSynth{}, Blobs: blob.NewMemory()},
		"blobs":  {Link: link, Synth: &fakeSynth{}, Timers: fakeTimers{}},
	} {
		if _, err := satellite.New(cfg); err == nil {
			t.Errorf("New without a %s succeeded, want an error", name)
		}
	}
}

// verifies SPEC §3.2
func TestUplinkFramesReachTheConfiguredHooks(t *testing.T) {
	link, dev := bridgetest.Dial(t, 1)
	mic := make(chan []byte, 1)
	wake := make(chan string, 1)
	mute := make(chan bridge.Mute, 1)

	sat, err := satellite.New(satellite.Config{
		Link: link, Synth: &fakeSynth{}, Timers: fakeTimers{}, Blobs: blob.NewMemory(),
		OnMic:  func(_ uint8, pcm []byte) error { mic <- pcm; return nil },
		OnWake: func(w string) error { wake <- w; return nil },
		OnMute: func(m bridge.Mute) error { mute <- m; return nil },
	})
	if err != nil {
		t.Fatalf("new satellite: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = link.Serve(ctx, sat) }()

	dev.SendMic(t, 0, []byte{1, 2, 3, 4})
	dev.SendWake(t, "okay_nabu")
	dev.SendMute(t, bridge.Mute{Hardware: true})

	if got := <-mic; len(got) != 4 {
		t.Errorf("mic payload = %d bytes, want 4", len(got))
	}
	if got := <-wake; got != "okay_nabu" {
		t.Errorf("wake = %q, want %q", got, "okay_nabu")
	}
	if got := <-mute; !got.Hardware {
		t.Error("mute did not report the hardware state")
	}
}
