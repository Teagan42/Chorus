//go:build hardware

// Hardware tier: needs a real satellite from devices.yaml running
// chorus_bridge firmware. Run with `task test:hardware`.
//
// These are smoke tests against the physical device -- "the real thing still
// behaves how we think it behaves" (CONTRIBUTING §1). Behavioural coverage of
// the link is the hermetic fake's job in link_test.go.
package bridge_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/config"
	"github.com/teaganglenn/chorus/internal/esphome"
)

var (
	inventory  = flag.String("inventory", "../../devices.yaml", "satellite inventory path")
	deviceName = flag.String("device", "", "satellite name (default: the only one)")
	listenAddr = flag.String("bridge-listen", ":6055", "audio port the satellite dials")
)

// dialIn bounds the wait for the device's outbound connection. The firmware's
// default reconnect_interval is 5s, so this allows several attempts.
const dialIn = 45 * time.Second

// micGapExcess is how much longer an uplink gap may be during playback than
// the same link's own gap while idle.
//
// Measured as an excess over a baseline, not against a fixed budget, because
// the uplink's idle jitter is set by the radio, not by this component: on a
// contended 2.4 GHz band the device goes over a second between frames with the
// speaker switched off entirely. A fixed budget charges that weather to
// playback and fails a link that is genuinely duplex. The baseline is taken on
// this same connection seconds earlier, so it is the closest available control.
const micGapExcess = 400 * time.Millisecond

// micBaselineWindow is how long the idle uplink is watched before playback, to
// learn what this link's jitter looks like with nothing else happening.
const micBaselineWindow = 4 * time.Second

// requireDevice skips unless the satellite answers its native API. A device
// that is simply absent is not a test failure (CONTRIBUTING §1).
//
// Probed once per run, not once per test: the device accepts a small number of
// API connections and is slow to answer while it is busy with audio, so a
// probe per test is both wasteful and a source of spurious skips.
var probe = sync.OnceValues(func() (*config.Satellite, error) {
	cfg, err := config.Load(*inventory)
	if err != nil {
		return nil, fmt.Errorf("no usable inventory at %s: %w", *inventory, err)
	}
	sat, err := cfg.Find(*deviceName)
	if err != nil {
		return nil, fmt.Errorf("no satellite selected: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c, err := esphome.Dial(ctx, sat.Address, sat.PSK)
	if err != nil {
		return nil, fmt.Errorf("satellite %s (%s) did not answer: %w", sat.Name, sat.Address, err)
	}
	_ = c.Close()
	return sat, nil
})

func requireDevice(t *testing.T) *config.Satellite {
	t.Helper()
	sat, err := probe()
	if err != nil {
		t.Skip(err.Error())
	}
	return sat
}

// acceptDevice waits for the satellite to dial the audio port. Reaching this
// with a live native API but no inbound link means the firmware is not
// chorus_bridge, or its orchestrator_host does not point here.
func acceptDevice(t *testing.T, sat *config.Satellite) *bridge.Link {
	t.Helper()
	ln, err := bridge.Listen(*listenAddr)
	if err != nil {
		t.Fatalf("listen %s: %v", *listenAddr, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), dialIn)
	defer cancel()

	l, err := ln.Accept(ctx)
	if err != nil {
		t.Fatalf("satellite %s answered its native API but never dialled %s within %v: %v\n"+
			"check that chorus_bridge firmware is flashed and its orchestrator_host is this machine",
			sat.Name, *listenAddr, dialIn, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// recorder timestamps every uplink frame so a gap in capture is measurable.
type recorder struct {
	mu     sync.Mutex
	mic    []time.Time
	bytes  int
	played []bridge.Played
	mutes  []bridge.Mute
	wakes  []string
	notify chan struct{}
}

func newRecorder() *recorder { return &recorder{notify: make(chan struct{}, 4096)} }

func (r *recorder) OnMic(_ uint8, pcm []byte) error {
	r.mu.Lock()
	r.mic = append(r.mic, time.Now())
	r.bytes += len(pcm)
	r.mu.Unlock()
	r.ping()
	return nil
}

func (r *recorder) OnWake(word string) error {
	r.mu.Lock()
	r.wakes = append(r.wakes, word)
	r.mu.Unlock()
	r.ping()
	return nil
}

func (r *recorder) OnPlayed(p bridge.Played) error {
	r.mu.Lock()
	r.played = append(r.played, p)
	r.mu.Unlock()
	r.ping()
	return nil
}

func (r *recorder) OnMute(m bridge.Mute) error {
	r.mu.Lock()
	r.mutes = append(r.mutes, m)
	r.mu.Unlock()
	r.ping()
	return nil
}

func (r *recorder) ping() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *recorder) await(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.After(within)
	for {
		r.mu.Lock()
		ok := cond()
		r.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-r.notify:
		case <-deadline:
			t.Fatalf("timed out after %v waiting for %s\n%s", within, what, r.state())
		}
	}
}

// state summarises what arrived, so a hardware timeout names its own cause:
// no mic means no capture, no played means no DAC output.
func (r *recorder) state() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	pos := time.Duration(0)
	if n := len(r.played); n > 0 {
		pos = r.played[n-1].Position(bridge.SampleRate)
	}
	return fmt.Sprintf("uplink: %d mic frames / %d bytes; downlink: %d played frames, "+
		"position %v; wakes %v; mutes %+v", len(r.mic), r.bytes, len(r.played), pos, r.wakes, r.mutes)
}

// awaitSettled waits until the reported position stops advancing, i.e. the DAC
// has drained. Needed before asserting a final position: the test learns the
// utterance finished from a report that is itself one DMA buffer behind.
func (r *recorder) awaitSettled(t *testing.T, within time.Duration) time.Duration {
	t.Helper()
	deadline := time.Now().Add(within)
	last := r.playedPosition()
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		pos := r.playedPosition()
		if pos == last {
			return pos
		}
		last = pos
	}
	t.Fatalf("played position still advancing after %v\n%s", within, r.state())
	return 0
}

// playedPosition is the audio the DAC has actually emitted.
func (r *recorder) playedPosition() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.played) == 0 {
		return 0
	}
	return r.played[len(r.played)-1].Position(bridge.SampleRate)
}

// serveLink runs the read side for the duration of a test.
func serveLink(t *testing.T, l *bridge.Link, h bridge.Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		err := l.Serve(ctx, h)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
			t.Logf("Serve ended: %v", err)
		}
	}()
}

// feedTTS streams pcm slightly ahead of real time and returns when the feed
// ends. A real TTS stream arrives as it is synthesised; dumping a whole
// utterance at line rate saturates the device's radio and starves the uplink
// that shares it, which looks like a duplex failure but is not one.
func feedTTS(ctx context.Context, l *bridge.Link, pcm []byte) <-chan error {
	const slice = 200 * time.Millisecond
	n := int(bridge.SampleRate) * int(slice/time.Millisecond) / 1000 * 2

	// Primed two slices deep before pacing starts, so a loop() hiccup cannot
	// underrun the device's mixer source and make the i2s speaker emit silence it
	// counts as played. Not deeper: five slices is a one-second burst at line
	// rate, which is the radio saturation the pacing above exists to avoid.
	lead := min(2*n, len(pcm))

	done := make(chan error, 1)
	go func() {
		if err := l.SendTTS(pcm[:lead]); err != nil {
			done <- err
			return
		}
		for off := lead; off < len(pcm); off += n {
			if ctx.Err() != nil {
				done <- ctx.Err()
				return
			}
			if err := l.SendTTS(pcm[off:min(off+n, len(pcm))]); err != nil {
				done <- err
				return
			}
			// Three quarters of a slice, so the device keeps a small lead and
			// the speaker never underruns waiting on the next write.
			time.Sleep(slice * 3 / 4)
		}
		done <- nil
	}()
	return done
}

// tone generates a synthetic sine wave. Synthetic by policy: a test fixture
// must never be a real household recording.
func tone(d time.Duration, hz float64) []byte {
	n := int(float64(bridge.SampleRate) * d.Seconds())
	pcm := make([]byte, 2*n)
	for i := range n {
		v := int16(8000 * math.Sin(2*math.Pi*hz*float64(i)/float64(bridge.SampleRate)))
		// Little-endian: that is what the device's speaker reads.
		pcm[2*i] = byte(uint16(v))
		pcm[2*i+1] = byte(uint16(v) >> 8)
	}
	return pcm
}

// longestGap reports the largest interval between consecutive uplink frames
// that fall inside [from, to], where in the window it started, and how many
// frames arrived. The offset distinguishes a one-off stall as playback spins
// up from capture actually stopping while the speaker runs.
func longestGap(stamps []time.Time, from, to time.Time) (worst, at time.Duration, count int) {
	prev := from
	for _, s := range stamps {
		if s.Before(from) || s.After(to) {
			continue
		}
		count++
		if g := s.Sub(prev); g > worst {
			worst, at = g, prev.Sub(from)
		}
		prev = s
	}
	if g := to.Sub(prev); g > worst {
		worst, at = g, prev.Sub(from)
	}
	return worst, at, count
}

// verifies SPEC §3.3.1
//
// The phase-1 blocker: the microphone keeps delivering while the speaker
// plays, on the actual device, with no Home Assistant involved (SPEC §14).
func TestHardwareFullDuplex(t *testing.T) {
	sat := requireDevice(t)
	l := acceptDevice(t, sat)
	t.Logf("satellite %s dialled in: %+v", sat.Name, l.Hello())

	if l.Hello().SampleRate != bridge.SampleRate {
		t.Errorf("device sample rate = %d, want %d", l.Hello().SampleRate, bridge.SampleRate)
	}

	r := newRecorder()
	serveLink(t, l, r)

	// Capture must be live before playback, or the duplex claim is vacuous.
	r.await(t, "uplink audio before playback", 15*time.Second, func() bool { return len(r.mic) > 5 })

	r.mu.Lock()
	if len(r.mutes) > 0 && r.mutes[0].Hardware {
		r.mu.Unlock()
		t.Skip("hardware mute is engaged; it is authoritative and this test needs the mic")
	}
	r.mu.Unlock()

	// The control: what this link's uplink does with the speaker idle.
	idleFrom := time.Now()
	time.Sleep(micBaselineWindow)
	idleTo := time.Now()
	r.mu.Lock()
	idleStamps := append([]time.Time(nil), r.mic...)
	r.mu.Unlock()
	baseline, _, idleCount := longestGap(idleStamps, idleFrom, idleTo)
	t.Logf("idle window %v: %d mic frames, longest uplink gap %v",
		idleTo.Sub(idleFrom), idleCount, baseline)
	if idleCount == 0 {
		t.Fatal("no uplink audio while idle: the mic is not capturing at all")
	}

	// Six seconds, so the device's 16 KB speaker buffer and the kernel socket
	// buffer cannot swallow the whole utterance and hide a capture stall.
	const utterance = 6 * time.Second
	pcm := tone(utterance, 440)

	// A baseline, because frames are cumulative per connection: the device may
	// already have played audio on this link, and "position >= 6s" would then
	// be satisfied before a single byte of this utterance reached the DAC.
	base := r.playedPosition()

	start := time.Now()
	feed := feedTTS(context.Background(), l, pcm)
	go func() {
		if err := <-feed; err == nil {
			_ = l.Finish()
		}
	}()

	// Wait on the DAC's own position rather than a wall-clock guess.
	r.await(t, "the DAC to report the whole utterance played", utterance+20*time.Second,
		func() bool {
			if len(r.played) == 0 {
				return false
			}
			return r.played[len(r.played)-1].Position(bridge.SampleRate)-base >= utterance-100*time.Millisecond
		})
	end := time.Now()

	r.mu.Lock()
	stamps := append([]time.Time(nil), r.mic...)
	r.mu.Unlock()

	// Measured to half a second short of the end. Stopping the writer on a
	// duplex I2S bus disturbs the reader for a few hundred ms, which is a real
	// artifact of stream teardown and not the mic going deaf during playback.
	gap, at, during := longestGap(stamps, start, end.Add(-500*time.Millisecond))
	t.Logf("playback window %v: %d mic frames, longest uplink gap %v at +%v",
		end.Sub(start), during, gap, at)

	if during == 0 {
		t.Fatal("no uplink audio at all during playback: the link is half duplex")
	}
	if gap > baseline+micGapExcess {
		t.Errorf("longest uplink gap during playback = %v, want < %v "+
			"(idle baseline %v + %v): the microphone stalled while the speaker was "+
			"playing, which is the half-duplex behaviour chorus_bridge exists to escape",
			gap, baseline+micGapExcess, baseline, micGapExcess)
		t.Logf("gap began %v into the playback window", at)
	}
	// A hard ceiling regardless of the baseline, so a radio bad enough to hide a
	// real stall cannot buy a pass. Half duplex gaps the whole utterance; a third
	// of it is already far more silence than duplex capture can explain.
	if gap > utterance/3 {
		t.Errorf("longest uplink gap during playback = %v for a %v utterance: "+
			"too much of the utterance passed with no capture to call this duplex",
			gap, utterance)
	}
}

// verifies SPEC §3.2.1
//
// Playback position comes from the DAC callback, not from bytes sent. A proxy
// estimate on the stock path is ~±128 ms, so anything near that means the
// position is being guessed and the truncation point is unusable for the DPO
// corpus (ADR-0005).
func TestHardwarePlaybackPositionIsDACAccurate(t *testing.T) {
	sat := requireDevice(t)
	l := acceptDevice(t, sat)

	r := newRecorder()
	serveLink(t, l, r)

	const utterance = 3 * time.Second
	base := r.playedPosition()
	feed := feedTTS(context.Background(), l, tone(utterance, 440))
	go func() {
		if err := <-feed; err == nil {
			_ = l.Finish()
		}
	}()

	r.await(t, "played position to reach the utterance length", utterance+20*time.Second,
		func() bool {
			if len(r.played) == 0 {
				return false
			}
			return r.played[len(r.played)-1].Position(bridge.SampleRate)-base >= utterance-500*time.Millisecond
		})
	r.awaitSettled(t, 20*time.Second)

	r.mu.Lock()
	played := append([]bridge.Played(nil), r.played...)
	r.mu.Unlock()

	if len(played) < 2 {
		t.Fatalf("got %d played reports, want a stream of them", len(played))
	}

	// Cumulative, so frames and timestamps must both only ever advance.
	for i := 1; i < len(played); i++ {
		if played[i].Frames < played[i-1].Frames {
			t.Errorf("played frames went backwards at %d: %d then %d",
				i, played[i-1].Frames, played[i].Frames)
		}
		if played[i].TimestampMicros < played[i-1].TimestampMicros {
			t.Errorf("played timestamp went backwards at %d: %d then %d",
				i, played[i-1].TimestampMicros, played[i].TimestampMicros)
		}
	}

	final := played[len(played)-1].Position(bridge.SampleRate) - base
	// Asymmetric, and the asymmetry is the point. Undershoot is the failure that
	// matters: a position short of the audio the DAC emitted puts the truncation
	// point before what the user heard, and the DPO corpus would record words as
	// unheard that were not (ADR-0005). Overshoot means the stream stayed open
	// across an underrun and the i2s speaker emitted silence -- which the DAC
	// really did play, so reporting it is correct. The skew check below is what
	// separates that from a byte-counting proxy; it is bounded here only so an
	// unbounded runaway still fails.
	if d := final - utterance; d < -50*time.Millisecond {
		t.Errorf("final played position = %v for a %v utterance (short by %v): the "+
			"position does not account for everything the DAC emitted", final, utterance, -d)
	} else if d > 250*time.Millisecond {
		t.Errorf("final played position = %v for a %v utterance (over by %v): more "+
			"underrun silence than a duplex bus should produce", final, utterance, d)
	}

	// The device stamps each report with esp_timer micros. Position advance and
	// timestamp advance must agree, which a byte-counting proxy cannot fake.
	first, last := played[0], played[len(played)-1]
	byFrames := last.Position(bridge.SampleRate) - first.Position(bridge.SampleRate)
	byClock := time.Duration(last.TimestampMicros-first.TimestampMicros) * time.Microsecond
	skew := byFrames - byClock
	t.Logf("advance by DAC frames %v, by esp_timer %v, skew %v", byFrames, byClock, skew)

	// One-sided on purpose. Frames running *ahead* of the clock is the failure
	// that matters: it means the position is counting bytes handed to the
	// speaker rather than frames the DAC emitted, which is exactly the proxy
	// SPEC §3.2.1 rejects. Frames running behind means playback stalled -- a
	// performance problem, not an accuracy one, and not this test's claim.
	if skew > 50*time.Millisecond {
		t.Errorf("DAC frames advanced %v more than esp_timer: the position is a "+
			"byte-counting proxy, not the DAC's own", skew)
	}
}

// verifies SPEC §3.2
//
// Barge-in: stop() must take effect promptly and the last reported position is
// the truncation point.
func TestHardwareBargeInStopsPlayback(t *testing.T) {
	sat := requireDevice(t)
	l := acceptDevice(t, sat)

	r := newRecorder()
	serveLink(t, l, r)

	// Fed from a goroutine so the barge-in lands mid-utterance, which is the
	// only case that tells us anything: Stop() after the last byte is sent
	// would prove nothing about discarding a buffer.
	// Cancelled with the barge-in: a feeder that keeps writing TTS after Stop
	// legitimately restarts playback, and the position would climb for that
	// reason rather than because the buffer survived.
	feedCtx, stopFeed := context.WithCancel(context.Background())
	defer stopFeed()
	sendErr := feedTTS(feedCtx, l, tone(10*time.Second, 440))

	r.await(t, "playback to start", 20*time.Second, func() bool {
		return len(r.played) > 0 && r.played[len(r.played)-1].Frames > 0
	})

	at := r.playedPosition()
	stopFeed()
	tag, err := l.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	t.Logf("barge-in at %v, stop tag %d", at, tag)

	// The firmware answers every stop with one report echoing its tag, taken
	// after it gated the counter; that report is the truncation point the
	// satellite waits for (ADR-0033). A device that never answers has
	// chorus_bridge firmware older than this host.
	r.await(t, "the device to answer the stop", 10*time.Second, func() bool {
		for _, p := range r.played {
			if p.Stop == tag {
				return true
			}
		}
		return false
	})

	// After stop the DAC drains at most what was already in its FIFO, so the
	// position settles almost immediately. A position that keeps climbing by
	// seconds means stop() did not discard the buffer. Real time is
	// unavoidable here: this tier measures the hardware, not a virtual clock.
	time.Sleep(3 * time.Second)
	after := r.playedPosition()

	if drift := after - at; drift > 500*time.Millisecond {
		t.Errorf("played position advanced %v after stop (%v -> %v): the speaker "+
			"buffer was not discarded", drift, at, after)
	}

	// The answer is the last word: the counter is gated before it is taken,
	// so nothing the device reports afterwards may place the cut later.
	r.mu.Lock()
	var answered time.Duration
	for _, p := range r.played {
		if p.Stop == tag {
			answered = p.Position(bridge.SampleRate)
		}
	}
	r.mu.Unlock()
	if answered != after {
		t.Errorf("the stop's answer put the cut at %v but the position settled at %v: "+
			"the device reported frames after gating", answered, after)
	}

	// The feed may legitimately fail once the device discards its buffer; what
	// matters is that it does not hang.
	select {
	case err := <-sendErr:
		if err != nil {
			t.Logf("TTS feed ended after barge-in: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("SendTTS still blocked 10s after barge-in")
	}

	// And capture survives a barge-in, which is the whole point of having one.
	r.mu.Lock()
	before := len(r.mic)
	r.mu.Unlock()
	r.await(t, "uplink audio after barge-in", 10*time.Second,
		func() bool { return len(r.mic) > before })
}
