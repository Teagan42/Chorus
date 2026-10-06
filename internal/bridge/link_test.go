package bridge_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/bridge"
)

// patience bounds a wait on an observable the implementation is committed to
// producing, so a timeout means the implementation is wrong, not slow.
const patience = 5 * time.Second

func newPipe(t *testing.T) (host, device net.Conn) {
	t.Helper()
	host, device = net.Pipe()
	t.Cleanup(func() {
		_ = host.Close()
		_ = device.Close()
	})
	return host, device
}

func deviceHello(channels uint8) bridge.Frame {
	return bridge.Hello{
		Version:       bridge.ProtocolVersion,
		SampleRate:    bridge.SampleRate,
		BitsPerSample: bridge.BitsPerSample,
		MicChannels:   channels,
	}.Frame()
}

// collector records what Serve dispatched. Guarded because Serve's read loop
// and the test goroutine both touch it.
type collector struct {
	mu     sync.Mutex
	mic    []micChunk
	wakes  []string
	played []bridge.Played
	mutes  []bridge.Mute
	notify chan struct{}
}

type micChunk struct {
	channel uint8
	pcm     []byte
}

func newCollector() *collector {
	return &collector{notify: make(chan struct{}, 1024)}
}

func (c *collector) OnMic(channel uint8, pcm []byte) error {
	c.mu.Lock()
	c.mic = append(c.mic, micChunk{channel, pcm})
	c.mu.Unlock()
	c.ping()
	return nil
}

func (c *collector) OnWake(word string) error {
	c.mu.Lock()
	c.wakes = append(c.wakes, word)
	c.mu.Unlock()
	c.ping()
	return nil
}

func (c *collector) OnPlayed(p bridge.Played) error {
	c.mu.Lock()
	c.played = append(c.played, p)
	c.mu.Unlock()
	c.ping()
	return nil
}

func (c *collector) OnMute(m bridge.Mute) error {
	c.mu.Lock()
	c.mutes = append(c.mutes, m)
	c.mu.Unlock()
	c.ping()
	return nil
}

func (c *collector) ping() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// await blocks until cond holds, driven by handler dispatch rather than by
// polling a clock.
func (c *collector) await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(patience)
	for {
		c.mu.Lock()
		ok := cond()
		c.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-c.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// serve runs the link in the background and returns its error on a channel.
func serve(t *testing.T, l *bridge.Link, h bridge.Handler) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errc := make(chan error, 1)
	go func() { errc <- l.Serve(ctx, h) }()
	return errc
}

// verifies SPEC §3.1
func TestNewLinkReadsHello(t *testing.T) {
	host, device := newPipe(t)
	go func() { _ = bridge.NewWriter(device).WriteFrame(deviceHello(2)) }()

	l, err := bridge.NewLink(host)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}
	got := l.Hello()
	want := bridge.Hello{
		Version:       bridge.ProtocolVersion,
		SampleRate:    bridge.SampleRate,
		BitsPerSample: bridge.BitsPerSample,
		MicChannels:   2,
	}
	if got != want {
		t.Errorf("Hello() = %+v, want %+v", got, want)
	}
}

// A mismatched pair must fail at connect, not drift into misparsed audio.
func TestNewLinkRejectsBadProtocolVersion(t *testing.T) {
	host, device := newPipe(t)
	bad := bridge.Hello{
		Version:       bridge.ProtocolVersion + 1,
		SampleRate:    bridge.SampleRate,
		BitsPerSample: bridge.BitsPerSample,
		MicChannels:   1,
	}
	go func() { _ = bridge.NewWriter(device).WriteFrame(bad.Frame()) }()

	if _, err := bridge.NewLink(host); err == nil {
		t.Fatal("NewLink accepted a mismatched protocol version")
	}
}

// Hello opens every connection; anything else first means a desynchronised peer.
// verifies SPEC §3.1
//
// The declared format is the point of the handshake: everything downstream is
// hardcoded to 16 kHz/16-bit, so a device that announces anything else must be
// refused rather than silently misread.
func TestNewLinkRejectsIncompatibleAudioFormat(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hello bridge.Hello
	}{
		{"sample rate", bridge.Hello{
			Version:       bridge.ProtocolVersion,
			SampleRate:    48000,
			BitsPerSample: bridge.BitsPerSample,
			MicChannels:   2,
		}},
		{"bit depth", bridge.Hello{
			Version:       bridge.ProtocolVersion,
			SampleRate:    bridge.SampleRate,
			BitsPerSample: 32,
			MicChannels:   2,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, device := newPipe(t)
			go func() { _ = bridge.NewWriter(device).WriteFrame(tc.hello.Frame()) }()

			if _, err := bridge.NewLink(host); err == nil {
				t.Fatal("NewLink accepted an incompatible audio format")
			}
		})
	}
}

func TestNewLinkRejectsNonHelloFirstFrame(t *testing.T) {
	host, device := newPipe(t)
	// A payload that would parse as a valid hello body, so only the frame type
	// can reject it. A short payload would fail ParseHello's length check and
	// pass this test even with the type check gone.
	body := deviceHello(1).Payload
	go func() {
		_ = bridge.NewWriter(device).WriteFrame(bridge.Frame{Type: bridge.TypeMic, Payload: body})
	}()

	if _, err := bridge.NewLink(host); err == nil {
		t.Fatal("NewLink accepted a mic frame before hello")
	}
}

// verifies SPEC §3.2
func TestServeDispatchesDeviceFrames(t *testing.T) {
	host, device := newPipe(t)
	w := bridge.NewWriter(device)
	go func() {
		_ = w.WriteFrame(deviceHello(2))
	}()
	l, err := bridge.NewLink(host)
	if err != nil {
		t.Fatal(err)
	}
	c := newCollector()
	serve(t, l, c)

	go func() {
		_ = w.WriteFrame(bridge.Frame{Type: bridge.TypeMic, Flags: bridge.ChannelAEC, Payload: []byte{1, 2}})
		_ = w.WriteFrame(bridge.Frame{Type: bridge.TypeMic, Flags: bridge.ChannelRaw, Payload: []byte{3, 4}})
		_ = w.WriteFrame(bridge.Frame{Type: bridge.TypeWake, Payload: []byte("hey_eddie")})
		_ = w.WriteFrame(bridge.Played{Frames: 16000, TimestampMicros: 42}.Frame())
		_ = w.WriteFrame(bridge.Mute{Hardware: true}.Frame())
	}()

	c.await(t, "all five device frames", func() bool {
		return len(c.mic) == 2 && len(c.wakes) == 1 && len(c.played) == 1 && len(c.mutes) == 1
	})

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mic[0].channel != bridge.ChannelAEC || c.mic[1].channel != bridge.ChannelRaw {
		t.Errorf("mic channels = %d,%d, want %d,%d",
			c.mic[0].channel, c.mic[1].channel, bridge.ChannelAEC, bridge.ChannelRaw)
	}
	if string(c.mic[1].pcm) != "\x03\x04" {
		t.Errorf("raw mic pcm = % x, want 03 04", c.mic[1].pcm)
	}
	if c.wakes[0] != "hey_eddie" {
		t.Errorf("wake = %q, want %q", c.wakes[0], "hey_eddie")
	}
	if got := c.played[0].Position(bridge.SampleRate); got != time.Second {
		t.Errorf("played position = %v, want 1s", got)
	}
	if !c.mutes[0].Hardware {
		t.Error("mute hardware bit lost")
	}
}

// fakeDevice is the firmware's observable behaviour: it streams numbered mic
// chunks uplink while reading the downlink, on separate goroutines, exactly as
// chorus_bridge's loop() interleaves pump_uplink_ and pump_downlink_.
type fakeDevice struct {
	conn net.Conn

	// sent counts mic chunks the host has actually taken delivery of. net.Pipe
	// is unbuffered, so it only advances when the host reads.
	sent   atomic.Uint32
	closed atomic.Bool

	mu       sync.Mutex
	ttsBytes int
	// Mic delivery count at the first and last TTS frame of an utterance. If
	// these are equal the host stopped reading the uplink for the whole of
	// playback, which is the half-duplex behaviour this component exists to
	// escape.
	uplinkAtFirstTTS int64
	uplinkAtLastTTS  int64
	stops            int
	finishes         int
	ducks            []bridge.Duck
	micEnables       []bool
	notify           chan struct{}
}

func newFakeDevice(conn net.Conn) *fakeDevice {
	return &fakeDevice{conn: conn, uplinkAtFirstTTS: -1, notify: make(chan struct{}, 1024)}
}

// streamMic writes numbered chunks until the connection dies. The counter is
// stored only after the write completes, so it measures delivery rather than
// intent: a blocked write leaves it frozen.
func (d *fakeDevice) streamMic() {
	w := bridge.NewWriter(d.conn)
	if err := w.WriteFrame(deviceHello(1)); err != nil {
		d.closed.Store(true)
		return
	}
	for n := uint32(1); ; n++ {
		pcm := make([]byte, 4)
		binary.BigEndian.PutUint32(pcm, n)
		if err := w.WriteFrame(bridge.Frame{Type: bridge.TypeMic, Flags: bridge.ChannelAEC, Payload: pcm}); err != nil {
			d.closed.Store(true)
			return
		}
		d.sent.Store(n)
	}
}

// await blocks until the device has observed what the host already sent.
// Asserting without this races the device's reader goroutine.
func (d *fakeDevice) await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(patience)
	for {
		d.mu.Lock()
		ok := cond()
		d.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-d.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for the device to see %s", what)
		}
	}
}

// awaitTTSBytes waits until the device has consumed a whole utterance.
func (d *fakeDevice) awaitTTSBytes(t *testing.T, want int) {
	t.Helper()
	d.await(t, "a whole utterance", func() bool { return d.ttsBytes >= want })
}

// readDownlink consumes host frames, but will not take another one until the
// uplink has made progress since the last. That turns full duplex from
// something the test hopes the scheduler provides into something the utterance
// cannot complete without: a host that stops reading the uplink blocks here,
// its TTS writes back up, and SendTTS never returns. Without this gate,
// net.Pipe's synchronous rendezvous lets the TTS writer and this reader run hot
// and starve the uplink entirely, which made the assertion flaky.
func (d *fakeDevice) readDownlink() {
	r := bridge.NewReader(d.conn)
	lastMic := uint32(0)
	for {
		for d.sent.Load() == lastMic {
			if d.closed.Load() {
				return
			}
			runtime.Gosched()
		}
		lastMic = d.sent.Load()

		f, err := r.ReadFrame()
		if err != nil {
			return
		}
		d.mu.Lock()
		switch f.Type {
		case bridge.TypeTTS:
			d.ttsBytes += len(f.Payload)
			if d.uplinkAtFirstTTS < 0 {
				d.uplinkAtFirstTTS = int64(d.sent.Load())
			}
			d.uplinkAtLastTTS = int64(d.sent.Load())
		case bridge.TypeStop:
			d.stops++
		case bridge.TypeFinish:
			d.finishes++
		case bridge.TypeDuck:
			dk, err := bridge.ParseDuck(f.Payload)
			if err == nil {
				d.ducks = append(d.ducks, dk)
			}
		case bridge.TypeMicEnable:
			d.micEnables = append(d.micEnables, f.Flags&1 != 0)
		}
		d.mu.Unlock()
		select {
		case d.notify <- struct{}{}:
		default:
		}
	}
}

// verifies SPEC §3.1
//
// The whole reason chorus_bridge exists: the mic keeps delivering while the
// speaker plays. net.Pipe is unbuffered, so a host that stopped reading the
// uplink to write TTS would deadlock here rather than quietly pass.
func TestLinkIsFullDuplex(t *testing.T) {
	host, device := newPipe(t)
	d := newFakeDevice(device)
	go d.streamMic()
	go d.readDownlink()

	l, err := bridge.NewLink(host)
	if err != nil {
		t.Fatal(err)
	}
	c := newCollector()
	serve(t, l, c)

	// Four seconds of 16 kHz mono PCM: well past MaxPayload, so this is a long
	// multi-frame write that a half-duplex host could not survive.
	tts := make([]byte, 4*bridge.SampleRate*bridge.BitsPerSample/8)
	if err := l.SendTTS(tts); err != nil {
		t.Fatalf("SendTTS: %v", err)
	}
	if err := l.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	d.awaitTTSBytes(t, len(tts))

	d.mu.Lock()
	gotBytes, first, last := d.ttsBytes, d.uplinkAtFirstTTS, d.uplinkAtLastTTS
	d.mu.Unlock()
	if gotBytes != len(tts) {
		t.Errorf("device received %d TTS bytes, want %d", gotBytes, len(tts))
	}
	if first < 0 {
		t.Fatal("device never saw a TTS frame")
	}

	// The assertion that bites: mic chunks were delivered *between* the first
	// and last frame of the utterance. Ordering is not enough — a host that
	// stalls the uplink for all of playback and drains it afterwards still
	// delivers chunks "after playback started", and that is half duplex.
	const overlap = 10
	if last-first < overlap {
		t.Errorf("mic delivery advanced %d chunks during playback (%d -> %d), want at least %d: "+
			"the uplink stalled while the speaker was fed", last-first, first, last, overlap)
	}

	// And the handler really saw chunks from the playback window, not just the
	// device's own counter moving.
	c.await(t, "a mic chunk delivered during playback", func() bool {
		for _, m := range c.mic {
			if n := int64(binary.BigEndian.Uint32(m.pcm)); n > first && n <= last {
				return true
			}
		}
		return false
	})
}

// Barge-in and ducking are control frames that must reach the device while
// audio is in flight.
func TestHostControlFrames(t *testing.T) {
	host, device := newPipe(t)
	d := newFakeDevice(device)
	go d.streamMic()
	go d.readDownlink()

	l, err := bridge.NewLink(host)
	if err != nil {
		t.Fatal(err)
	}
	c := newCollector()
	serve(t, l, c)

	if err := l.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := l.SendDuck(bridge.Duck{Decibels: 20, DurationMillis: 250}); err != nil {
		t.Fatalf("SendDuck: %v", err)
	}
	if err := l.SetMicEnabled(false); err != nil {
		t.Fatalf("SetMicEnabled: %v", err)
	}
	if err := l.SetMicEnabled(true); err != nil {
		t.Fatalf("SetMicEnabled: %v", err)
	}

	// The device's reader is a separate goroutine; a bare read here races it.
	d.await(t, "all four control frames", func() bool {
		return d.stops == 1 && len(d.ducks) == 1 && len(d.micEnables) == 2
	})

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stops != 1 {
		t.Errorf("stops = %d, want 1", d.stops)
	}
	if len(d.ducks) != 1 || d.ducks[0] != (bridge.Duck{Decibels: 20, DurationMillis: 250}) {
		t.Errorf("ducks = %+v, want one 20 dB / 250 ms", d.ducks)
	}
	if len(d.micEnables) != 2 || d.micEnables[0] || !d.micEnables[1] {
		t.Errorf("micEnables = %v, want [false true]", d.micEnables)
	}
}

// A TTS utterance exceeds the uint16 length field, so SendTTS must split it and
// every chunk must stay frame-aligned: a half sample desynchronises the device.
func TestSendTTSChunksOnSampleBoundaries(t *testing.T) {
	host, device := newPipe(t)
	go func() { _ = bridge.NewWriter(device).WriteFrame(deviceHello(1)) }()
	l, err := bridge.NewLink(host)
	if err != nil {
		t.Fatal(err)
	}

	pcm := make([]byte, 3*bridge.MaxPayload+7*2)
	for i := range pcm {
		pcm[i] = byte(i)
	}

	type frame struct {
		typ bridge.Type
		n   int
	}
	// Accumulated in the goroutine and read only after it closes done: an
	// unbuffered hand-off here would deadlock against SendTTS.
	var frames []frame
	var got []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bridge.NewReader(device)
		for {
			f, err := r.ReadFrame()
			if err != nil {
				return
			}
			got = append(got, f.Payload...)
			frames = append(frames, frame{f.Type, len(f.Payload)})
		}
	}()

	if err := l.SendTTS(pcm); err != nil {
		t.Fatalf("SendTTS: %v", err)
	}
	_ = l.Close()
	<-done

	total := 0
	for _, f := range frames {
		if f.typ != bridge.TypeTTS {
			t.Fatalf("frame type = %v, want tts", f.typ)
		}
		if f.n >= bridge.MaxPayload {
			t.Errorf("chunk of %d bytes exceeds the length field", f.n)
		}
		if f.n%2 != 0 {
			t.Errorf("chunk of %d bytes splits a 16-bit sample", f.n)
		}
		total += f.n
	}
	if total != len(pcm) {
		t.Errorf("device received %d bytes, want %d", total, len(pcm))
	}
	if string(got) != string(pcm) {
		t.Error("reassembled PCM differs from what was sent")
	}
}

// Send is called from the turn engine while Serve runs; concurrent writes must
// not interleave into an unparseable stream.
func TestConcurrentSendsDoNotInterleave(t *testing.T) {
	host, device := newPipe(t)
	go func() { _ = bridge.NewWriter(device).WriteFrame(deviceHello(1)) }()
	l, err := bridge.NewLink(host)
	if err != nil {
		t.Fatal(err)
	}

	const senders, each = 8, 25
	chunk := make([]byte, 512)

	bad := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bridge.NewReader(device)
		for n := 0; n < senders*each; n++ {
			f, err := r.ReadFrame()
			if err != nil {
				select {
				case bad <- "read: " + err.Error():
				default:
				}
				return
			}
			if f.Type != bridge.TypeTTS || len(f.Payload) != len(chunk) {
				select {
				case bad <- "corrupt frame, stream desynchronised":
				default:
				}
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				if err := l.SendTTS(chunk); err != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	<-done
	select {
	case msg := <-bad:
		t.Fatal(msg)
	default:
	}
}

// Serve returns when the device hangs up, so the supervisor can reconnect
// rather than hold a dead link (SPEC §7).
func TestServeReturnsOnDeviceClose(t *testing.T) {
	host, device := newPipe(t)
	go func() { _ = bridge.NewWriter(device).WriteFrame(deviceHello(1)) }()
	l, err := bridge.NewLink(host)
	if err != nil {
		t.Fatal(err)
	}
	errc := serve(t, l, newCollector())

	_ = device.Close()

	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("Serve err = %v, want nil or EOF", err)
		}
	case <-time.After(patience):
		t.Fatal("Serve did not return after the device closed")
	}
}

// Cancelling the context tears the link down; a blocked socket read must not
// outlive it.
func TestServeStopsOnContextCancel(t *testing.T) {
	host, device := newPipe(t)
	go func() { _ = bridge.NewWriter(device).WriteFrame(deviceHello(1)) }()
	l, err := bridge.NewLink(host)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- l.Serve(ctx, newCollector()) }()

	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Serve err = %v, want context.Canceled", err)
		}
	case <-time.After(patience):
		t.Fatal("Serve ignored context cancellation")
	}
}

// A handler error is the host's own failure; it must surface rather than be
// swallowed into a silently deaf link.
func TestServeReturnsHandlerError(t *testing.T) {
	host, device := newPipe(t)
	w := bridge.NewWriter(device)
	go func() {
		_ = w.WriteFrame(deviceHello(1))
		_ = w.WriteFrame(bridge.Frame{Type: bridge.TypeMic, Payload: []byte{0, 0}})
	}()
	l, err := bridge.NewLink(host)
	if err != nil {
		t.Fatal(err)
	}
	errc := serve(t, l, failingHandler{})

	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("Serve swallowed a handler error")
		}
	case <-time.After(patience):
		t.Fatal("Serve did not return the handler error")
	}
}

type failingHandler struct{}

func (failingHandler) OnMic(uint8, []byte) error    { return errors.New("handler exploded") }
func (failingHandler) OnWake(string) error          { return nil }
func (failingHandler) OnPlayed(bridge.Played) error { return nil }
func (failingHandler) OnMute(bridge.Mute) error     { return nil }

// An unknown frame type is skippable by design, so newer firmware does not
// break an older host.
func TestServeIgnoresUnknownFrameTypes(t *testing.T) {
	host, device := newPipe(t)
	w := bridge.NewWriter(device)
	go func() {
		_ = w.WriteFrame(deviceHello(1))
		_ = w.WriteFrame(bridge.Frame{Type: 0x7f, Payload: []byte("from the future")})
		_ = w.WriteFrame(bridge.Frame{Type: bridge.TypeWake, Payload: []byte("hey_eddie")})
	}()
	l, err := bridge.NewLink(host)
	if err != nil {
		t.Fatal(err)
	}
	c := newCollector()
	serve(t, l, c)

	c.await(t, "the wake frame after an unknown type", func() bool { return len(c.wakes) == 1 })
}

// The device dials out, so the orchestrator is the TCP server for audio while
// being the client for the native API (SPEC §13). Loopback only: no external
// network, so this stays in the hermetic tier.
func TestListenerAcceptsADialingDevice(t *testing.T) {
	ln, err := bridge.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	dialed := make(chan error, 1)
	go func() {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			dialed <- err
			return
		}
		defer func() { _ = conn.Close() }()
		dialed <- bridge.NewWriter(conn).WriteFrame(deviceHello(2))
		<-time.After(50 * time.Millisecond)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	l, err := ln.Accept(ctx)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer func() { _ = l.Close() }()

	if err := <-dialed; err != nil {
		t.Fatalf("device side: %v", err)
	}
	if got := l.Hello().MicChannels; got != 2 {
		t.Errorf("MicChannels = %d, want 2", got)
	}
}

// Accept must not block forever when no device ever dials.
func TestListenerAcceptHonoursContext(t *testing.T) {
	ln, err := bridge.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := ln.Accept(ctx)
		errc <- err
	}()
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Accept err = %v, want context.Canceled", err)
		}
	case <-time.After(patience):
		t.Fatal("Accept ignored context cancellation")
	}
}
