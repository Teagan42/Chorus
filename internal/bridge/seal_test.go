package bridge_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
)

// lockedBuffer is a bytes.Buffer two goroutines may share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) write(p []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.Write(p)
}

func (l *lockedBuffer) bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Clone(l.b.Bytes())
}

// tap passes writes through until it is holding, then keeps each one for the
// test to deliver, alter, repeat or drop. A sealed frame is one Write, so
// each kept write is one whole record.
type tap struct {
	net.Conn
	mu      sync.Mutex
	holding bool
	kept    [][]byte
}

func (t *tap) Write(b []byte) (int, error) {
	t.mu.Lock()
	if t.holding {
		t.kept = append(t.kept, bytes.Clone(b))
		t.mu.Unlock()
		return len(b), nil
	}
	t.mu.Unlock()
	return t.Conn.Write(b)
}

func (t *tap) hold() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.holding = true
}

func (t *tap) records() [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.kept
}

// uplinkRig is a joined kitchen whose sealed frames the test holds and then
// puts on the wire itself, as a peer on the path would.
type uplinkRig struct {
	link *bridge.Link
	dc   *bridge.DeviceConn
	wire *tap
	errc <-chan error
	c    *collector
}

// sealedUplink joins the kitchen, starts Serve, and seals frames without
// sending them.
func sealedUplink(t *testing.T, frames ...bridge.Frame) (*uplinkRig, [][]byte) {
	t.Helper()
	host, device := newPipe(t)
	wire := &tap{Conn: device}
	got := make(chan *bridge.DeviceConn, 1)
	go func() {
		dc, err := joins(wire, 1)
		if err != nil {
			t.Errorf("device side: %v", err)
		}
		got <- dc
	}()
	l, err := bridge.NewLink(host, keys)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}
	r := &uplinkRig{link: l, dc: <-got, wire: wire, c: newCollector()}
	r.errc = serve(t, l, r.c)
	wire.hold()
	for _, f := range frames {
		if err := r.dc.WriteFrame(f); err != nil {
			t.Fatalf("seal %s: %v", f.Type, err)
		}
	}
	return r, wire.records()
}

// delivered is every wake Serve handed the handler.
func (r *uplinkRig) delivered() []string {
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	return append([]string(nil), r.c.wakes...)
}

// put writes raw bytes onto the wire under the tap, as an attacker would.
// The write may fail once the host has hung up, which is the point.
func (r *uplinkRig) put(b []byte) { _, _ = r.wire.Conn.Write(b) }

// servedErr waits for Serve to end and returns why.
func (r *uplinkRig) servedErr(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.errc:
		return err
	case <-time.After(patience):
		t.Fatal("Serve kept the link open")
		return nil
	}
}

func wakes(words ...string) []bridge.Frame {
	var fs []bridge.Frame
	for _, w := range words {
		fs = append(fs, bridge.Frame{Type: bridge.TypeWake, Payload: []byte(w)})
	}
	return fs
}

// A frame altered in flight, anywhere in its type, flags, payload or tag,
// does not open, and the host drops the link rather than act on it.
//
// verifies SPEC §3.2.2
func TestATamperedFrameDropsTheLink(t *testing.T) {
	n := len("hey_eddie") + bridge.SealedOverhead
	for name, at := range map[string]int{
		"type":    bridge.RecordHeaderSize,
		"flags":   bridge.RecordHeaderSize + 1,
		"payload": bridge.RecordHeaderSize + 2,
		"tag":     n - 1,
	} {
		t.Run(name, func(t *testing.T) {
			r, recs := sealedUplink(t, wakes("hey_eddie")...)
			bad := bytes.Clone(recs[0])
			bad[at] ^= 0x01
			r.put(bad)
			if err := r.servedErr(t); !errors.Is(err, bridge.ErrBadRecord) {
				t.Errorf("Serve err = %v, want ErrBadRecord", err)
			}
			if got := r.delivered(); len(got) != 0 {
				t.Errorf("a tampered frame was delivered: %v", got)
			}
		})
	}
}

// A shortened length field splits the record where the sender did not, so
// the tag no longer covers what is read.
//
// verifies SPEC §3.2.2
func TestATamperedLengthDropsTheLink(t *testing.T) {
	r, recs := sealedUplink(t, wakes("hey_eddie")...)
	bad := bytes.Clone(recs[0])
	bad[1]--
	r.put(bad)
	if err := r.servedErr(t); !errors.Is(err, bridge.ErrBadRecord) {
		t.Errorf("Serve err = %v, want ErrBadRecord", err)
	}
}

// A frame sent twice: the first is delivered, the second is under a counter
// already spent, and the link drops.
//
// verifies SPEC §3.2.2
func TestAReplayedFrameDropsTheLink(t *testing.T) {
	r, recs := sealedUplink(t, wakes("hey_eddie")...)
	r.put(recs[0])
	r.c.await(t, "the genuine wake", func() bool { return len(r.c.wakes) == 1 })
	r.put(recs[0])
	if err := r.servedErr(t); !errors.Is(err, bridge.ErrBadRecord) {
		t.Errorf("Serve err = %v, want ErrBadRecord", err)
	}
	if got := r.delivered(); len(got) != 1 {
		t.Errorf("wakes = %v, want the one genuine wake", got)
	}
}

// Two frames swapped in flight: the first to arrive is under the wrong
// counter, so neither is delivered.
//
// verifies SPEC §3.2.2
func TestReorderedFramesDropTheLink(t *testing.T) {
	r, recs := sealedUplink(t, wakes("hey_eddie", "button")...)
	r.put(recs[1])
	r.put(recs[0])
	if err := r.servedErr(t); !errors.Is(err, bridge.ErrBadRecord) {
		t.Errorf("Serve err = %v, want ErrBadRecord", err)
	}
	if got := r.delivered(); len(got) != 0 {
		t.Errorf("a reordered frame was delivered: %v", got)
	}
}

// A frame dropped in flight leaves the next one under the wrong counter.
//
// verifies SPEC §3.2.2
func TestADroppedFrameDropsTheLink(t *testing.T) {
	r, recs := sealedUplink(t, wakes("hey_eddie", "button")...)
	r.put(recs[1])
	if err := r.servedErr(t); !errors.Is(err, bridge.ErrBadRecord) {
		t.Errorf("Serve err = %v, want ErrBadRecord", err)
	}
}

// A frame cut off mid-record is a failed link, not a clean disconnect, and
// nothing of it is delivered.
//
// verifies SPEC §3.2.2
func TestATruncatedFrameIsAnErrorNotAHangUp(t *testing.T) {
	r, recs := sealedUplink(t, wakes("hey_eddie")...)
	r.put(recs[0][:len(recs[0])/2])
	_ = r.wire.Close()
	err := r.servedErr(t)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Serve err = %v, want io.ErrUnexpectedEOF", err)
	}
	if got := r.delivered(); len(got) != 0 {
		t.Errorf("a truncated frame was delivered: %v", got)
	}
}

// A record too short to hold a tag is refused without waiting on it.
//
// verifies SPEC §3.2.2
func TestARecordShorterThanATagDropsTheLink(t *testing.T) {
	r, _ := sealedUplink(t)
	r.put([]byte{0x00, 0x03, 0x02, 0x00, 0x00})
	if err := r.servedErr(t); !errors.Is(err, bridge.ErrBadRecord) {
		t.Errorf("Serve err = %v, want ErrBadRecord", err)
	}
}

// The host's own downlink frame reflected back up: the directions are
// sealed under different keys, so it does not open.
//
// verifies SPEC §3.2.2
func TestAHostFrameReflectedBackDropsTheLink(t *testing.T) {
	host, device := newPipe(t)
	down := &tap{Conn: host}
	got := make(chan *bridge.DeviceConn, 1)
	go func() {
		dc, _ := joins(device, 1)
		got <- dc
	}()
	l, err := bridge.NewLink(down, keys)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}
	<-got
	down.hold()
	if err := l.SendTTS(make([]byte, 64)); err != nil {
		t.Fatalf("SendTTS: %v", err)
	}
	c := newCollector()
	errc := serve(t, l, c)
	_, _ = device.Write(down.records()[0])
	select {
	case err := <-errc:
		if !errors.Is(err, bridge.ErrBadRecord) {
			t.Errorf("Serve err = %v, want ErrBadRecord", err)
		}
	case <-time.After(patience):
		t.Fatal("Serve kept a link that reflected the host's frame")
	}
}

// The device's half: a downlink frame altered, replayed or reordered in
// flight does not open, so a peer on the path cannot speak into the room.
//
// verifies SPEC §3.2.2
func TestTheDeviceRefusesATamperedDownlink(t *testing.T) {
	for name, deliver := range map[string]func(recs [][]byte) [][]byte{
		"tampered": func(recs [][]byte) [][]byte {
			bad := bytes.Clone(recs[0])
			bad[len(bad)-1] ^= 0x80
			return [][]byte{bad}
		},
		"replayed":  func(recs [][]byte) [][]byte { return [][]byte{recs[0], recs[0]} },
		"reordered": func(recs [][]byte) [][]byte { return [][]byte{recs[1], recs[0]} },
	} {
		t.Run(name, func(t *testing.T) {
			host, device := newPipe(t)
			down := &tap{Conn: host}
			got := make(chan *bridge.DeviceConn, 1)
			go func() {
				dc, _ := joins(device, 1)
				got <- dc
			}()
			l, err := bridge.NewLink(down, keys)
			if err != nil {
				t.Fatalf("NewLink: %v", err)
			}
			dc := <-got
			down.hold()
			if err := l.SendTTS(make([]byte, 64)); err != nil {
				t.Fatal(err)
			}
			if _, err := l.Stop(); err != nil {
				t.Fatal(err)
			}
			wire := deliver(down.records())
			go func() {
				for _, rec := range wire {
					_, _ = host.Write(rec)
				}
			}()
			var opened int
			for {
				_, err := dc.ReadFrame()
				if err != nil {
					if !errors.Is(err, bridge.ErrBadRecord) {
						t.Errorf("ReadFrame err = %v, want ErrBadRecord", err)
					}
					break
				}
				opened++
			}
			if want := map[string]int{"tampered": 0, "replayed": 1, "reordered": 0}[name]; opened != want {
				t.Errorf("the device opened %d frames, want %d", opened, want)
			}
		})
	}
}

// Every frame after the handshake is sealed: nothing a device says is on the
// wire in the clear, and each frame costs SealedOverhead bytes.
//
// verifies SPEC §3.2.2
func TestSealedFramesCarryNoPlaintext(t *testing.T) {
	pcm := bytes.Repeat([]byte("alan asks about the porch light "), 32)[:1024]
	_, recs := sealedUplink(t, bridge.Frame{Type: bridge.TypeMic, Payload: pcm})
	if bytes.Contains(recs[0], pcm[:32]) {
		t.Error("the mic chunk is on the wire in the clear")
	}
	if got, want := len(recs[0]), len(pcm)+bridge.SealedOverhead; got != want {
		t.Errorf("a 32 ms chunk is %d bytes sealed, want %d", got, want)
	}
}

func TestSealedWriterRefusesAnOversizePayload(t *testing.T) {
	w, err := bridge.NewSealedWriter(io.Discard, make([]byte, bridge.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(bridge.Frame{Type: bridge.TypeTTS, Payload: make([]byte, bridge.MaxSealedPayload+1)}); err == nil {
		t.Error("WriteFrame sealed a record its length field cannot hold")
	}
	if err := w.WriteFrame(bridge.Frame{Type: bridge.TypeTTS, Payload: make([]byte, bridge.MaxSealedPayload)}); err != nil {
		t.Errorf("WriteFrame(MaxSealedPayload): %v", err)
	}
	if _, err := bridge.NewSealedReader(nil, make([]byte, 16)); err == nil {
		t.Error("NewSealedReader accepted a 16-byte key")
	}
}

// The reader opens only what the writer sealed, under the counter it was
// sealed at: for any input, every frame it returns reseals to exactly the
// bytes it consumed.
func FuzzSealedReaderOpensOnlyGenuineRecords(f *testing.F) {
	key := bytes.Repeat([]byte{0x42}, bridge.KeySize)
	var genuine bytes.Buffer
	w, err := bridge.NewSealedWriter(&genuine, key)
	if err != nil {
		f.Fatal(err)
	}
	for _, fr := range append(wakes("hey_eddie"), bridge.Mute{Software: true}.Frame()) {
		if err := w.WriteFrame(fr); err != nil {
			f.Fatal(err)
		}
	}
	f.Add(genuine.Bytes())
	f.Add([]byte{0x00, 0x12})
	f.Add([]byte{0xff, 0xff, 0x00})
	f.Fuzz(func(t *testing.T, in []byte) {
		src := bytes.NewReader(in)
		r, err := bridge.NewSealedReader(src, key)
		if err != nil {
			t.Fatal(err)
		}
		var reseal bytes.Buffer
		rw, err := bridge.NewSealedWriter(&reseal, key)
		if err != nil {
			t.Fatal(err)
		}
		for {
			fr, err := r.ReadFrame()
			if err != nil {
				return
			}
			if err := rw.WriteFrame(fr); err != nil {
				t.Fatalf("reseal %s: %v", fr.Type, err)
			}
			consumed := in[:len(in)-src.Len()]
			if !bytes.Equal(reseal.Bytes(), consumed) {
				t.Fatalf("opened %s from bytes it did not seal:\n got %x\nwant %x", fr.Type, consumed, reseal.Bytes())
			}
		}
	})
}

// No hello, however malformed, panics the parser.
func FuzzParseHello(f *testing.F) {
	f.Add(named(kitchen, 2).Frame().Payload)
	f.Add([]byte{0x03, 0x00, 0x00, 0x3e, 0x80, 0x10, 0x02})
	f.Fuzz(func(t *testing.T, p []byte) {
		h, err := bridge.ParseHello(p)
		if err != nil {
			return
		}
		if !bytes.Equal(h.Frame().Payload, p) {
			t.Fatalf("hello %x does not round-trip: %x", p, h.Frame().Payload)
		}
	})
}

// What one side's X25519 work in a handshake costs on the host: a key pair
// and a shared secret. ADR-0074 cites it beside the firmware's estimate.
func BenchmarkX25519(b *testing.B) {
	peer, err := bridge.NewEphemeral()
	if err != nil {
		b.Fatal(err)
	}
	pub := bridge.PublicOf(peer)
	for b.Loop() {
		priv, err := bridge.NewEphemeral()
		if err != nil {
			b.Fatal(err)
		}
		if _, err := bridge.SharedSecret(priv, pub); err != nil {
			b.Fatal(err)
		}
	}
}

// What sealing one 32 ms mic chunk costs on the host. ADR-0074 cites it
// beside the firmware's estimate.
func BenchmarkSeal32ms(b *testing.B) {
	w, err := bridge.NewSealedWriter(io.Discard, make([]byte, bridge.KeySize))
	if err != nil {
		b.Fatal(err)
	}
	f := bridge.Frame{Type: bridge.TypeMic, Payload: make([]byte, 1024)}
	b.SetBytes(int64(len(f.Payload)))
	for b.Loop() {
		if err := w.WriteFrame(f); err != nil {
			b.Fatal(err)
		}
	}
}
