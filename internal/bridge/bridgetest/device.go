// Package bridgetest provides an in-process satellite for host-side tests.
//
// It speaks the real framing of internal/bridge, so the whole host stack runs
// against it with no device present. Playback advances only when a test says it
// did: a barge-in's truncation point is the thing under test in most of these
// tests, and a fake that advanced it on a timer would make that point a
// question about scheduling (CONTRIBUTING §1).
package bridgetest

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/bridge"
)

// patience bounds a wait on something the implementation is committed to
// producing, so a timeout means it is wrong rather than slow.
const patience = 5 * time.Second

// bytesPerFrame is one mono 16-bit sample.
const bytesPerFrame = bridge.BitsPerSample / 8

// Device is an in-process satellite. Its DAC model holds audio the host sent
// until Play emits it, and discards it on a stop, which is what makes the
// heard/unheard split assertable.
type Device struct {
	conn net.Conn

	// wmu serialises the test goroutine's frames against the read loop's own
	// reports; ready holds every frame behind the hello.
	wmu   sync.Mutex
	w     *bridge.Writer
	ready chan struct{}

	mu         sync.Mutex
	tts        []byte
	available  uint64 // frames handed over and not yet played
	played     uint64 // cumulative frames emitted, as PLAYED reports them
	duringStop uint64 // frames the DAC gets through as a stop arrives
	stops      int
	finishes   int
	ducks      []bridge.Duck
	micEnable  []bool
	notify     chan struct{}
	gone       chan struct{}
}

// Dial returns a host Link joined to a device that has completed the
// handshake. Both ends close with the test.
func Dial(t *testing.T, channels uint8) (*bridge.Link, *Device) {
	t.Helper()
	host, device := net.Pipe()
	t.Cleanup(func() {
		_ = host.Close()
		_ = device.Close()
	})

	d, hello := connect(device, channels)
	l, err := bridge.NewLink(host)
	if err != nil {
		t.Fatalf("host link: %v", err)
	}
	if err := <-hello; err != nil {
		t.Fatalf("device hello: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, d
}

// Connect joins a device to a connection the host side already holds, the
// way a satellite reaches a daemon that accepted it: the host completes the
// handshake on its own schedule, and a host that refuses the connection
// first is observed through Gone rather than reported here.
func Connect(conn net.Conn, channels uint8) *Device {
	d, _ := connect(conn, channels)
	return d
}

// connect starts the device on conn. The hello is written from a goroutine
// because net.Pipe is an unbuffered rendezvous: the host's read of it would
// otherwise deadlock against this write. Reading starts right after, so the
// host's downlink never blocks on the pipe and the pacing under test is the
// host's own, not the fake's.
func connect(conn net.Conn, channels uint8) (*Device, <-chan error) {
	d := &Device{
		conn: conn, w: bridge.NewWriter(conn),
		ready: make(chan struct{}), notify: make(chan struct{}), gone: make(chan struct{}),
	}
	hello := make(chan error, 1)
	go func() {
		err := d.w.WriteFrame(bridge.Hello{
			Version:       bridge.ProtocolVersion,
			SampleRate:    bridge.SampleRate,
			BitsPerSample: bridge.BitsPerSample,
			MicChannels:   channels,
		}.Frame())
		close(d.ready)
		hello <- err
		d.read()
	}()
	return d, hello
}

// write emits one frame after the hello, one at a time. A test that sends
// before the host has read the hello must not overtake it on the wire.
func (d *Device) write(f bridge.Frame) error {
	<-d.ready
	d.wmu.Lock()
	defer d.wmu.Unlock()
	return d.w.WriteFrame(f)
}

// Gone closes once the host has hung up: the downlink read failed, which is
// also what a host refusing the connection before the hello looks like.
func (d *Device) Gone() <-chan struct{} { return d.gone }

func (d *Device) read() {
	defer close(d.gone)
	r := bridge.NewReader(d.conn)
	for {
		f, err := r.ReadFrame()
		if err != nil {
			return
		}
		// Reported after the lock is released: writing a frame while holding mu
		// would deadlock against a concurrent Play on an unbuffered pipe.
		var report bool
		d.mu.Lock()
		switch f.Type {
		case bridge.TypeTTS:
			d.tts = append(d.tts, f.Payload...)
			d.available += uint64(len(f.Payload) / bytesPerFrame)
		case bridge.TypeStop:
			// A stop discards the buffer, which is the whole point of one: the
			// frames held here are never emitted and never reported played.
			d.stops++
			if n := min(d.duringStop, d.available); n > 0 {
				d.played += n
				report = true
			}
			d.duringStop = 0
			d.available = 0
		case bridge.TypeFinish:
			d.finishes++
		case bridge.TypeDuck:
			if dk, err := bridge.ParseDuck(f.Payload); err == nil {
				d.ducks = append(d.ducks, dk)
			}
		case bridge.TypeMicEnable:
			d.micEnable = append(d.micEnable, f.Flags&1 != 0)
		}
		d.wakeLocked()
		played := d.played
		d.mu.Unlock()

		if report {
			_ = d.write(bridge.Played{
				Frames:          played,
				TimestampMicros: int64(played) * int64(time.Second/time.Microsecond) / bridge.SampleRate,
			}.Frame())
		}
	}
}

// PlayDuringStop arms the device to emit this many more frames when it next
// processes a stop, modelling audio the DAC gets through while the stop is in
// flight. The real firmware gates its frame counter at the stop and discards
// the rest, so that last report is the authoritative truncation point
// (chorus_bridge.cpp, FrameType::STOP). Capped by what the host has sent.
func (d *Device) PlayDuringStop(frames uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.duringStop = frames
}

// wakeLocked releases everything waiting on a change. The caller holds mu.
func (d *Device) wakeLocked() {
	close(d.notify)
	d.notify = make(chan struct{})
}

// Play emits up to frames of buffered audio and reports the new cumulative
// position, as the device's DAC callback does (SPEC §3.2.1). It returns how
// many frames were actually emitted, which is bounded by what the host sent.
func (d *Device) Play(t *testing.T, frames uint64) uint64 {
	t.Helper()
	d.mu.Lock()
	if frames > d.available {
		frames = d.available
	}
	d.available -= frames
	d.played += frames
	played := d.played
	d.wakeLocked()
	d.mu.Unlock()

	if frames == 0 {
		return 0
	}
	// Timestamp tracks the audio, so position advance and clock advance agree
	// the way the firmware's esp_timer stamp does.
	micros := int64(played) * int64(time.Second/time.Microsecond) / bridge.SampleRate
	if err := d.write(bridge.Played{Frames: played, TimestampMicros: micros}.Frame()); err != nil {
		t.Fatalf("device played report: %v", err)
	}
	return frames
}

// PlayAll emits everything the host has sent and not yet had played.
func (d *Device) PlayAll(t *testing.T) uint64 {
	t.Helper()
	d.mu.Lock()
	n := d.available
	d.mu.Unlock()
	return d.Play(t, n)
}

// SendMic delivers an uplink chunk.
func (d *Device) SendMic(t *testing.T, channel uint8, pcm []byte) {
	t.Helper()
	if err := d.write(bridge.Frame{Type: bridge.TypeMic, Flags: channel, Payload: pcm}); err != nil {
		t.Fatalf("device mic: %v", err)
	}
}

// SendWake delivers a wake-word activation.
func (d *Device) SendWake(t *testing.T, word string) {
	t.Helper()
	if err := d.write(bridge.Frame{Type: bridge.TypeWake, Payload: []byte(word)}); err != nil {
		t.Fatalf("device wake: %v", err)
	}
}

// SendMute reports the mute state.
func (d *Device) SendMute(t *testing.T, m bridge.Mute) {
	t.Helper()
	if err := d.write(m.Frame()); err != nil {
		t.Fatalf("device mute: %v", err)
	}
}

// TTS is every byte of audio the host has sent, including audio a stop later
// discarded.
func (d *Device) TTS() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]byte(nil), d.tts...)
}

// Pending is audio the host has sent that has not been played or discarded.
func (d *Device) Pending() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.available
}

// Stops counts stop frames received.
func (d *Device) Stops() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stops
}

// Finishes counts finish frames received.
func (d *Device) Finishes() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.finishes
}

// Ducks is every ducking request received.
func (d *Device) Ducks() []bridge.Duck {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]bridge.Duck(nil), d.ducks...)
}

// MicEnables is every mic gate change received.
func (d *Device) MicEnables() []bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]bool(nil), d.micEnable...)
}

// Await blocks until cond holds of the device's own state. cond runs under the
// device's lock, so it must only read through the unexported fields.
func (d *Device) Await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(patience)
	for {
		d.mu.Lock()
		ok, changed := cond(), d.notify
		d.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("timed out after %v waiting for %s", patience, what)
		}
	}
}

// AwaitTTS blocks until the host has sent at least n bytes of audio.
func (d *Device) AwaitTTS(t *testing.T, n int) {
	t.Helper()
	d.Await(t, "the host to send audio", func() bool { return len(d.tts) >= n })
}

// AwaitStop blocks until the host has sent at least n stop frames.
func (d *Device) AwaitStop(t *testing.T, n int) {
	t.Helper()
	d.Await(t, "the host to stop playback", func() bool { return d.stops >= n })
}

// AwaitFinish blocks until the host has sent at least n finish frames.
func (d *Device) AwaitFinish(t *testing.T, n int) {
	t.Helper()
	d.Await(t, "the host to finish the utterance", func() bool { return d.finishes >= n })
}
