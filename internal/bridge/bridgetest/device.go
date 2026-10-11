// Package bridgetest provides an in-process satellite for host-side tests.
//
// It speaks the real framing of internal/bridge, so the whole host stack runs
// against it with no device present. Playback advances only when a test says it
// did: a barge-in's truncation point is the thing under test in most of these
// tests, and a fake that advanced it on a timer would make that point a
// question about scheduling (CONTRIBUTING §1).
package bridgetest

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
)

// patience bounds a wait on something the implementation is committed to
// producing, so a timeout means it is wrong rather than slow.
const patience = 5 * time.Second

// Name is the satellite a device proves it is unless told otherwise: the
// living-room Satellite1, named as ESPHome names it, MAC suffix and all.
const Name = "satellite1-4b2c10"

// pskText is Name's key. Thirty-two bytes, as api.encryption.key decodes to.
const pskText = "kitchen satellite pre-shared key"

// PSK is Name's key, a fresh copy each call.
func PSK() []byte { return []byte(pskText) }

// Keys is an inventory of one: Name, under PSK. Dial's host checks with it.
func Keys() bridge.Keys {
	return func(name string) ([]byte, bool) {
		if name != Name {
			return nil, false
		}
		return PSK(), true
	}
}

// Option changes what a device claims in the handshake.
type Option func(*Device)

// As makes the device answer the challenge as name, under psk. A psk that is
// not the inventory's is how a test plays a device that is not who it says.
func As(name string, psk []byte) Option {
	return func(d *Device) { d.name, d.psk = name, psk }
}

// bytesPerFrame is one mono 16-bit sample.
const bytesPerFrame = bridge.BitsPerSample / 8

// Device is an in-process satellite. Its DAC model holds audio the host sent
// until Play emits it, and discards it on a stop, which is what makes the
// heard/unheard split assertable.
type Device struct {
	conn  net.Conn
	hello bridge.Hello
	name  string
	psk   []byte

	// wmu serialises the test goroutine's frames against the read loop's own
	// reports; ready holds every frame behind the auth answer.
	wmu      sync.Mutex
	w        *bridge.Writer
	ready    chan struct{}
	answered chan error // the auth answer was written, or why it never was
	answer   sync.Once
	holding  bool           // HoldUplink is in force
	held     []bridge.Frame // frames in flight, in order, until released

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
// handshake, as Name under PSK. Both ends close with the test.
func Dial(t *testing.T, channels uint8) (*bridge.Link, *Device) {
	t.Helper()
	host, device := net.Pipe()
	t.Cleanup(func() {
		_ = host.Close()
		_ = device.Close()
	})

	d := connect(device, channels)
	l, err := bridge.NewLink(host, Keys())
	if err != nil {
		t.Fatalf("host link: %v", err)
	}
	if err := <-d.answered; err != nil {
		t.Fatalf("device handshake: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, d
}

// Connect joins a device to a connection the host side already holds, the
// way a satellite reaches a daemon that accepted it: the host completes the
// handshake on its own schedule, and a host that refuses the connection
// is observed through Gone rather than reported here. The device is Name
// under PSK unless an option says otherwise.
func Connect(conn net.Conn, channels uint8, opts ...Option) *Device {
	return connect(conn, channels, opts...)
}

// connect starts the device on conn. The hello is written from a goroutine
// because net.Pipe is an unbuffered rendezvous: the host's read of it would
// otherwise deadlock against this write. Reading starts right after, so the
// challenge is answered there, and the host's downlink never blocks on the
// pipe: the pacing under test is the host's own, not the fake's.
func connect(conn net.Conn, channels uint8, opts ...Option) *Device {
	d := &Device{
		conn: conn, w: bridge.NewWriter(conn),
		hello: bridge.Hello{
			Version:       bridge.ProtocolVersion,
			SampleRate:    bridge.SampleRate,
			BitsPerSample: bridge.BitsPerSample,
			MicChannels:   channels,
		},
		name: Name, psk: PSK(),
		ready: make(chan struct{}), answered: make(chan error, 1),
		notify: make(chan struct{}), gone: make(chan struct{}),
	}
	for _, o := range opts {
		o(d)
	}
	go func() {
		if err := d.w.WriteFrame(d.hello.Frame()); err != nil {
			d.answer.Do(func() { d.answered <- fmt.Errorf("hello: %w", err) })
		}
		d.read()
	}()
	return d
}

// answerChallenge sends the auth frame the firmware would, then lets every
// frame queued behind it through. Called from the read loop only.
func (d *Device) answerChallenge(p []byte) {
	d.answer.Do(func() {
		err := d.writeAuth(p)
		if err == nil {
			close(d.ready)
		}
		d.answered <- err
	})
}

func (d *Device) writeAuth(p []byte) error {
	c, err := bridge.ParseChallenge(p)
	if err != nil {
		return err
	}
	key, err := bridge.LinkKey(d.psk)
	if err != nil {
		return err
	}
	d.wmu.Lock()
	defer d.wmu.Unlock()
	return d.w.WriteFrame(bridge.AnswerChallenge(key, c, d.name, d.hello).Frame())
}

// write emits one frame after the auth answer, one at a time. A test that
// sends before the host has challenged must not overtake the handshake.
func (d *Device) write(f bridge.Frame) error {
	select {
	case <-d.ready:
	case <-d.gone:
		return fmt.Errorf("write %s: the host hung up before the handshake finished", f.Type)
	}
	d.wmu.Lock()
	defer d.wmu.Unlock()
	if d.holding {
		d.held = append(d.held, f)
		return nil
	}
	return d.w.WriteFrame(f)
}

// Hold is a run of uplink frames parked in flight. See Device.HoldUplink.
type Hold struct {
	t *testing.T
	d *Device
}

// HoldUplink parks every frame the device would send, in order, until the
// hold releases it. It models the wire's latency: a report the DAC has
// emitted but the host has not yet read, which is what lets a test put one
// report on each side of a stop and see which the host believes.
func (d *Device) HoldUplink(t *testing.T) *Hold {
	t.Helper()
	d.wmu.Lock()
	d.holding = true
	d.wmu.Unlock()
	return &Hold{t: t, d: d}
}

// Release lets the oldest n held frames reach the host, in order, and keeps
// holding the rest. Fewer than n held is a failure: a test releasing a frame
// it never staged is asserting on nothing.
func (h *Hold) Release(n int) {
	h.t.Helper()
	h.d.wmu.Lock()
	defer h.d.wmu.Unlock()
	if len(h.d.held) < n {
		h.t.Fatalf("release %d held frames, only %d are held", n, len(h.d.held))
	}
	h.d.writeHeldLocked(n)
}

// Lift releases everything held and lets later frames through as they come.
func (h *Hold) Lift() {
	h.t.Helper()
	h.d.wmu.Lock()
	defer h.d.wmu.Unlock()
	h.d.holding = false
	h.d.writeHeldLocked(len(h.d.held))
}

// writeHeldLocked writes the oldest n held frames. The caller holds wmu.
func (d *Device) writeHeldLocked(n int) {
	for _, f := range d.held[:n] {
		if err := d.w.WriteFrame(f); err != nil {
			panic(fmt.Sprintf("bridgetest: release held %s: %v", f.Type, err))
		}
	}
	d.held = d.held[n:]
}

// Gone closes once the host has hung up: the downlink read failed, which is
// also what a host refusing the connection or its answer looks like.
func (d *Device) Gone() <-chan struct{} { return d.gone }

func (d *Device) read() {
	defer close(d.gone)
	defer d.answer.Do(func() { d.answered <- errors.New("the host hung up before it challenged") })
	r := bridge.NewReader(d.conn)
	for {
		f, err := r.ReadFrame()
		if err != nil {
			return
		}
		if f.Type == bridge.TypeChallenge {
			d.answerChallenge(f.Payload)
			continue
		}
		// Reported after the lock is released: writing a frame while holding mu
		// would deadlock against a concurrent Play on an unbuffered pipe.
		var report bool
		var ack uint8
		d.mu.Lock()
		switch f.Type {
		case bridge.TypeTTS:
			d.tts = append(d.tts, f.Payload...)
			d.available += uint64(len(f.Payload) / bytesPerFrame)
		case bridge.TypeStop:
			// A stop discards the buffer, which is the whole point of one: the
			// frames held here are never emitted and never reported played.
			d.stops++
			d.played += min(d.duringStop, d.available)
			d.duringStop = 0
			d.available = 0
			// Answered whether or not the position moved, with the stop's tag,
			// as the firmware does: the host waits for this report and no other.
			report, ack = true, f.Flags
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
				Stop:            ack,
			}.Frame())
		}
	}
}

// PlayDuringStop arms the device to emit this many more frames when it next
// processes a stop, modelling audio the DAC gets through while the stop is in
// flight. The real firmware gates its frame counter at the stop and discards
// the rest, so the report answering the stop is the authoritative truncation
// point (chorus_bridge.cpp, FrameType::STOP). Capped by what the host has sent.
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
