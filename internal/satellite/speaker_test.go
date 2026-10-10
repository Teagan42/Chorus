package satellite_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/bridge/bridgetest"
	"github.com/teagan42/chorus/internal/satellite"
	"github.com/teagan42/chorus/internal/session"
)

// framesPerByte scales the fake synthesiser so a delta's audio is long enough
// to be cut partway through.
const framesPerByte = 100

// fakeSynth renders each byte of text as framesPerByte frames of silence, so a
// frame count maps back to a text offset arithmetically. Blank text renders as
// no frames at all, as the Kokoro provider does (internal/provider/kokoro).
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
	if strings.TrimSpace(text) == "" {
		return nil, nil
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

// applied reports the playback positions the satellite has recorded, which is
// later than the report reaching the wire: Device.Play returns once the host
// has read the frame, and the Serve loop applies it after that.
type applied struct {
	bridge.Handler

	mu     sync.Mutex
	at     uint64
	notify chan struct{}
}

func (a *applied) OnPlayed(p bridge.Played) error {
	err := a.Handler.OnPlayed(p)
	a.mu.Lock()
	a.at = max(a.at, p.Frames)
	close(a.notify)
	a.notify = make(chan struct{})
	a.mu.Unlock()
	return err
}

type rig struct {
	sat     *satellite.Satellite
	dev     *bridgetest.Device
	synth   *fakeSynth
	blobs   *blob.Memory
	paced   *pacing
	applied *applied
	played  uint64 // frames the test has had the device emit so far
	drain   chan time.Time
	settle  chan time.Time
	served  chan error
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
		// Buffered: firing a deadline must not need the code to be listening.
		// Both deadline waits return early when the position already answers
		// them, without ever reading the channel, and an unbuffered send would
		// then wedge the test instead of expiring the wait.
		drain: make(chan time.Time, 1), settle: make(chan time.Time, 1),
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
	r.applied = &applied{Handler: sat, notify: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { r.served <- link.Serve(ctx, r.applied) }()
	return r
}

// awaitPlayed blocks until the satellite has recorded a position of at least
// frames.
func (r *rig) awaitPlayed(t *testing.T, frames uint64) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		r.applied.mu.Lock()
		at, changed := r.applied.at, r.applied.notify
		r.applied.mu.Unlock()
		if at >= frames {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("the satellite recorded position %d, want at least %d", at, frames)
		}
	}
}

// play advances the DAC and waits for the satellite to record it. With the
// settle expiring at once, a barge-in before the report applies cuts at a
// stale position.
func (r *rig) play(t *testing.T, frames uint64) uint64 {
	t.Helper()
	return r.recorded(t, r.dev.Play(t, frames))
}

// playAll emits everything the device holds, recorded like play.
func (r *rig) playAll(t *testing.T) uint64 {
	t.Helper()
	return r.recorded(t, r.dev.PlayAll(t))
}

func (r *rig) recorded(t *testing.T, emitted uint64) uint64 {
	t.Helper()
	r.played += emitted
	r.awaitPlayed(t, r.played)
	return emitted
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
	r.playAll(t)

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
	if got := r.play(t, heard); got != heard {
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
	r.play(t, 2*framesPerByte)

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
	r.play(t, uint64((len("Sure, ")+3)*framesPerByte))

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
	r.playAll(t)
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
	r.playAll(t)
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
	r.play(t, heard)
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
		r.playAll(t)
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
	r.play(t, before)
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
//
// The DAC reports at its own cadence and a report takes time to arrive, so a
// routine report can already be on the wire when the stop goes out. It
// predates the stop and says nothing about where the device cut; the report
// the stop provokes does, and it lands behind it. A cut taken from the first
// change after the stop is the DAC's position a round trip ago (SPEC §15.1).
func TestTheCutIsTheReportTheStopProvokedNotTheFirstChange(t *testing.T) {
	// A long settle, so the cut has to come from the device, not the deadline.
	r := newRig(t, func(c *satellite.Config) { c.Settle = rigSettle })
	st, cancel := r.open(t, "call-1")

	for _, delta := range []string{"Hello ", "there ", "world."} {
		if err := st.Write(delta); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	r.dev.AwaitTTS(t, len("Hello there world.")*framesPerByte*2)

	// The host knows of two characters. The DAC then gets through three more
	// and reports them, but that report is still in flight at the barge-in,
	// and the device plays two more before the stop reaches it.
	r.play(t, 2*framesPerByte)
	hold := r.dev.HoldUplink(t)
	r.dev.Play(t, 3*framesPerByte)
	r.dev.PlayDuringStop(2 * framesPerByte)

	cancel()
	done := closeAsync(st)
	r.dev.AwaitStop(t, 1)

	// The stale report lands first and is applied before the answer to the
	// stop is even on the wire. A Close that takes it for the cut is given
	// the processor to return on it before the answer is let through, so
	// the answer cannot rescue it: yielding is what keeps this check from
	// racing the code it checks, and a Close that is waiting yields nothing.
	hold.Release(1)
	r.awaitPlayed(t, 5*framesPerByte)
	for range 1024 {
		select {
		case pb := <-done:
			t.Fatalf("Close returned on a report that predates the stop: Frames = %d, split = (%q, %q)",
				pb.Frames, pb.Spoken, pb.Unspoken)
		default:
			runtime.Gosched()
		}
	}
	hold.Lift()

	pb := await(t, done)
	const cut = 7 * framesPerByte
	if pb.Frames != cut {
		t.Errorf("Frames = %d, want %d: the cut is where the device gated at the stop, "+
			"not the first report to arrive after it", pb.Frames, cut)
	}
	// Seven characters in is inside "there ", which the user began to hear.
	if pb.Spoken != "Hello there " || pb.Unspoken != "world." {
		t.Errorf("split = (%q, %q), want (%q, %q)", pb.Spoken, pb.Unspoken, "Hello there ", "world.")
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
	r.play(t, heard)

	// The device answers every stop, so the only way to have no answer is a
	// wire that never delivers it: a satellite that drops off mid-stop.
	// Expiring must degrade to the position already known, not wedge the
	// session.
	r.dev.HoldUplink(t)
	cancel()
	done := closeAsync(st)
	r.dev.AwaitStop(t, 1)
	r.settle <- time.Now()

	pb := await(t, done)
	if pb.Frames != heard || !pb.Truncated {
		t.Errorf("Frames = %d, Truncated = %v, want %d, true", pb.Frames, pb.Truncated, heard)
	}
}

// The daemon shuts down while the kitchen is reading the weekend forecast:
// the stop goes out, then the link is no longer read, so the device's answer
// can never arrive. The cut is the position already known, at once, rather
// than after a settle deadline nothing is left to wait for.
//
// verifies SPEC §4.4
func TestAStopOnALinkNobodyReadsDoesNotWaitForItsAnswer(t *testing.T) {
	r := newRig(t, func(c *satellite.Config) { c.Settle = rigSettle })
	st, cancel := r.open(t, "call-w1")

	if err := st.Write("Sunny on Saturday, rain on Sunday."); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len("Sunny on Saturday, rain on Sunday.")*framesPerByte*2)
	const heard = 5 * framesPerByte
	r.play(t, heard)

	r.dev.HoldUplink(t)
	cancel()
	done := closeAsync(st)
	r.dev.AwaitStop(t, 1)
	r.sat.Hangup()

	pb := await(t, done)
	if pb.Frames != heard || !pb.Truncated {
		t.Errorf("Frames = %d, Truncated = %v, want %d, true", pb.Frames, pb.Truncated, heard)
	}
}

// The office satellite drops off the network as it finishes saying the
// garage is shut. Nothing will confirm the tail now, so the utterance is
// reported unconfirmed at once rather than after the drain deadline.
//
// verifies SPEC §4.4
func TestALinkGoneBeforeTheDrainIsConfirmedIsUnconfirmedAtOnce(t *testing.T) {
	r := newRig(t)
	st, _ := r.open(t, "call-g1")

	if err := st.Write("The garage door is shut."); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len("The garage door is shut.")*framesPerByte*2)
	const heard = 6 * framesPerByte
	r.play(t, heard)

	done := closeAsync(st)
	r.dev.AwaitFinish(t, 1)
	r.sat.Hangup()

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
	r.playAll(t)

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

// verifies SPEC §4.4
func TestABlankDeltaThatRendersNoFramesIsNotATruncation(t *testing.T) {
	r := newRig(t)
	st, _ := r.open(t, "call-1")

	// Streamed speech can open with whitespace, and blank text renders as
	// silence: zero frames, but synthesised all the same.
	const lead, text = " ", "The light is off."
	for _, d := range []string{lead, text} {
		if err := st.Write(d); err != nil {
			t.Fatalf("write %q: %v", d, err)
		}
	}
	r.dev.AwaitTTS(t, len(text)*framesPerByte*2)
	r.playAll(t)

	pb := await(t, closeAsync(st))

	// A false truncation records a speech_truncated event and cancels a turn
	// the user heard in full.
	if pb.Truncated {
		t.Errorf("Truncated = true, Spoken = %q, Unspoken = %q; want the whole utterance spoken",
			pb.Spoken, pb.Unspoken)
	}
	if pb.Spoken != lead+text {
		t.Errorf("Spoken = %q, want %q", pb.Spoken, lead+text)
	}
}

// verifies SPEC §4.4
func TestACutAfterABlankLeadingDeltaKeepsTheUtteranceInOrder(t *testing.T) {
	r := newRig(t)
	st, cancel := r.open(t, "call-1")

	const lead, text = " ", "Sure, I can do that, but the kitchen light is already off."
	for _, d := range []string{lead, text} {
		if err := st.Write(d); err != nil {
			t.Fatalf("write %q: %v", d, err)
		}
	}
	r.dev.AwaitTTS(t, len(text)*framesPerByte*2)
	r.play(t, 3*framesPerByte)

	cancel()
	pb := await(t, closeAsync(st))

	// The blank delta was heard as much as anything was: filing it as unspoken
	// would put it after words the user heard, and the pair would not rejoin.
	if pb.Spoken+pb.Unspoken != lead+text {
		t.Errorf("split = (%q, %q), does not rejoin to the utterance", pb.Spoken, pb.Unspoken)
	}
	if !strings.HasPrefix(pb.Spoken, lead+"Sure,") {
		t.Errorf("Spoken = %q, want it to start with the clause the DAC entered", pb.Spoken)
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
	r.playAll(t)
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
	r.playAll(t)

	pb := await(t, done)
	if pb.Frames != want {
		t.Errorf("second utterance Frames = %d, want %d", pb.Frames, want)
	}
	if pb.Truncated {
		t.Error("Truncated = true, want false")
	}
}

// The Listening child measures a barge-in from the same origin this Speaker
// truncates on, and the only way to share it is to read it: SpeechBase plus a
// Playback's Frames is the cumulative position the device reported (ADR-0030).
//
// verifies SPEC §3.2.1, §8
func TestSpeechBaseIsWhereThisUtterancesFramesStart(t *testing.T) {
	r := newRig(t)

	first, _ := r.open(t, "call-1")
	if base := r.sat.SpeechBase(); base != 0 {
		t.Errorf("SpeechBase = %d before anything played, want 0", base)
	}
	if err := first.Write("one"); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len("one")*framesPerByte*2)
	done := closeAsync(first)
	r.dev.AwaitFinish(t, 1)
	r.playAll(t)
	await(t, done)

	second, _ := r.open(t, "call-2")
	base := r.sat.SpeechBase()
	if want := uint64(len("one") * framesPerByte); base != want {
		t.Fatalf("SpeechBase = %d for the second utterance, want %d", base, want)
	}
	if err := second.Write("two"); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, (len("one")+len("two"))*framesPerByte*2)
	done = closeAsync(second)
	r.dev.AwaitFinish(t, 2)
	r.playAll(t)

	pb := await(t, done)
	if got, want := base+uint64(pb.Frames), r.played; got != want {
		t.Errorf("base + Frames = %d, want the device's cumulative %d", got, want)
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
	r.playAll(t)
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
	r.play(t, 4*framesPerByte)

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

// The model sends a speak call whole, so its stream is closed while the
// kitchen is still playing it, and a cut lands during the drain. It is a
// cut all the same: the stop is answered, and the position on that answer
// is where the ear stopped, not the last routine report (ADR-0033).
//
// verifies SPEC §4.4
func TestACutWhileTheDeviceDrainsIsTheStopsAnswer(t *testing.T) {
	r := newRig(t, func(c *satellite.Config) { c.Settle = rigSettle })
	st, cancel := r.open(t, "call-1")

	for _, delta := range []string{"Hello ", "there ", "world."} {
		if err := st.Write(delta); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	r.dev.AwaitTTS(t, len("Hello there world.")*framesPerByte*2)
	done := closeAsync(st)
	r.dev.AwaitFinish(t, 1)
	r.play(t, 2*framesPerByte)
	// The DAC gets into "there " while the stop is in flight.
	r.dev.PlayDuringStop(5 * framesPerByte)

	cancel()
	pb := await(t, done)

	if want := int64(7 * framesPerByte); pb.Frames != want {
		t.Errorf("Frames = %d, want %d: the stop's answer is the cut", pb.Frames, want)
	}
	if pb.Spoken != "Hello there " || pb.Unspoken != "world." {
		t.Errorf("split = (%q, %q), want (%q, %q)", pb.Spoken, pb.Unspoken, "Hello there ", "world.")
	}
	if !pb.Truncated || pb.Failure != nil {
		t.Errorf("Truncated = %v, Failure = %v: want a cut, which is no failure", pb.Truncated, pb.Failure)
	}
	if n := r.dev.Stops(); n != 1 {
		t.Errorf("%d stops, want 1", n)
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

func started(t *testing.T, st session.Stream) <-chan struct{} {
	t.Helper()
	s, ok := st.(session.Starter)
	if !ok {
		t.Fatal("a satellite stream must report when its DAC starts")
	}
	return s.Started()
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// The answer to "turn off the kitchen lights" has reached the device but
// the DAC has not played it yet: that is not a start. The first report past
// where the utterance began is.
//
// verifies SPEC §11, §3.2.1
func TestTheFirstReportPastTheBaseIsTheStart(t *testing.T) {
	r := newRig(t)
	st, _ := r.open(t, "call-1")
	start := started(t, st)

	text := "Turning off the kitchen lights."
	if err := st.Write(text); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len(text)*framesPerByte*2)
	if isClosed(start) {
		t.Fatal("started before the DAC reported a frame")
	}

	r.play(t, 160)
	if !isClosed(start) {
		t.Fatal("not started after the DAC reported playing the utterance")
	}
	done := closeAsync(st)
	r.dev.AwaitFinish(t, 1)
	r.playAll(t)
	await(t, done)
}

// PLAYED counts the whole connection, so "One sec" having played must not
// read as the search result behind it having started.
//
// verifies SPEC §11, §3.2.1
func TestASecondUtteranceStartsOnItsOwnFrames(t *testing.T) {
	r := newRig(t)

	first, _ := r.open(t, "call-1")
	if err := first.Write("One sec."); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len("One sec.")*framesPerByte*2)
	done := closeAsync(first)
	r.dev.AwaitFinish(t, 1)
	r.playAll(t)
	await(t, done)

	second, _ := r.open(t, "call-2")
	start := started(t, second)
	if isClosed(start) {
		t.Fatal("the second utterance started on the first one's frames")
	}
	text := "I found three albums by Led Zeppelin."
	if err := second.Write(text); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, (len("One sec.")+len(text))*framesPerByte*2)
	r.play(t, 160)
	if !isClosed(start) {
		t.Fatal("the second utterance never started")
	}
	done = closeAsync(second)
	r.dev.AwaitFinish(t, 2)
	r.playAll(t)
	await(t, done)
}

// A barge-in before the DAC reached the answer: it never started.
//
// verifies SPEC §11, §4.4
func TestAnUtteranceCutBeforeItPlayedNeverStarts(t *testing.T) {
	r := newRig(t)
	st, cancel := r.open(t, "call-1")
	start := started(t, st)

	text := "Here is the forecast for Saturday."
	if err := st.Write(text); err != nil {
		t.Fatalf("write: %v", err)
	}
	r.dev.AwaitTTS(t, len(text)*framesPerByte*2)
	cancel()
	pb := await(t, closeAsync(st))
	if pb.Spoken != "" {
		t.Fatalf("Spoken = %q before any frame played", pb.Spoken)
	}
	if isClosed(start) {
		t.Error("an utterance nobody heard reported a start")
	}
}
