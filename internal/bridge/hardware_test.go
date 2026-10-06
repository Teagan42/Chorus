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

// micGapTolerance is the largest silence allowed in the uplink while the
// speaker is playing. Chunks are 32 ms, so a healthy link stays far below
// this; half duplex shows up as a gap the length of the whole utterance.
const micGapTolerance = 400 * time.Millisecond

// requireDevice skips unless the satellite answers its native API. A device
// that is simply absent is not a test failure (CONTRIBUTING §1).
func requireDevice(t *testing.T) *config.Satellite {
	t.Helper()
	cfg, err := config.Load(*inventory)
	if err != nil {
		t.Skipf("no usable inventory at %s: %v", *inventory, err)
	}
	sat, err := cfg.Find(*deviceName)
	if err != nil {
		t.Skipf("no satellite selected: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := esphome.Dial(ctx, sat.Address, sat.PSK)
	if err != nil {
		t.Skipf("satellite %s (%s) did not answer: %v", sat.Name, sat.Address, err)
	}
	_ = c.Close()
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
			t.Fatalf("timed out after %v waiting for %s", within, what)
		}
	}
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
// that fall inside [from, to].
func longestGap(stamps []time.Time, from, to time.Time) (time.Duration, int) {
	var worst time.Duration
	count := 0
	prev := from
	for _, s := range stamps {
		if s.Before(from) || s.After(to) {
			continue
		}
		count++
		if g := s.Sub(prev); g > worst {
			worst = g
		}
		prev = s
	}
	if g := to.Sub(prev); g > worst {
		worst = g
	}
	return worst, count
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

	// Six seconds, so the device's 16 KB speaker buffer and the kernel socket
	// buffer cannot swallow the whole utterance and hide a capture stall.
	const utterance = 6 * time.Second
	pcm := tone(utterance, 440)

	start := time.Now()
	if err := l.SendTTS(pcm); err != nil {
		t.Fatalf("SendTTS: %v", err)
	}
	if err := l.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// Wait on the DAC's own position rather than a wall-clock guess.
	r.await(t, "the DAC to report the whole utterance played", utterance+20*time.Second,
		func() bool {
			if len(r.played) == 0 {
				return false
			}
			return r.played[len(r.played)-1].Position(bridge.SampleRate) >= utterance-100*time.Millisecond
		})
	end := time.Now()

	r.mu.Lock()
	stamps := append([]time.Time(nil), r.mic...)
	r.mu.Unlock()

	gap, during := longestGap(stamps, start, end)
	t.Logf("playback window %v: %d mic frames, longest uplink gap %v", end.Sub(start), during, gap)

	if during == 0 {
		t.Fatal("no uplink audio at all during playback: the link is half duplex")
	}
	if gap > micGapTolerance {
		t.Errorf("longest uplink gap during playback = %v, want < %v: "+
			"the microphone stalled while the speaker was playing, which is the "+
			"half-duplex behaviour chorus_bridge exists to escape",
			gap, micGapTolerance)
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
	if err := l.SendTTS(tone(utterance, 440)); err != nil {
		t.Fatalf("SendTTS: %v", err)
	}
	if err := l.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	r.await(t, "played position to reach the utterance length", utterance+20*time.Second,
		func() bool {
			if len(r.played) == 0 {
				return false
			}
			return r.played[len(r.played)-1].Position(bridge.SampleRate) >= utterance-100*time.Millisecond
		})

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

	final := played[len(played)-1].Position(bridge.SampleRate)
	if d := final - utterance; d < -50*time.Millisecond || d > 50*time.Millisecond {
		t.Errorf("final played position = %v for a %v utterance (error %v)", final, utterance, d)
	}

	// The device stamps each report with esp_timer micros. Position advance and
	// timestamp advance must agree, which a byte-counting proxy cannot fake.
	first, last := played[0], played[len(played)-1]
	byFrames := last.Position(bridge.SampleRate) - first.Position(bridge.SampleRate)
	byClock := time.Duration(last.TimestampMicros-first.TimestampMicros) * time.Microsecond
	skew := byFrames - byClock
	t.Logf("advance by DAC frames %v, by esp_timer %v, skew %v", byFrames, byClock, skew)
	if skew < -100*time.Millisecond || skew > 100*time.Millisecond {
		t.Errorf("DAC frame advance and esp_timer advance disagree by %v", skew)
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
	sendErr := make(chan error, 1)
	go func() { sendErr <- l.SendTTS(tone(10*time.Second, 440)) }()

	r.await(t, "playback to start", 20*time.Second, func() bool {
		return len(r.played) > 0 && r.played[len(r.played)-1].Frames > 0
	})

	at := r.playedPosition()
	if err := l.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	t.Logf("barge-in at %v", at)

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
