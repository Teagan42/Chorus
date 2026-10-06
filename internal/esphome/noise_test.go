package esphome

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/flynn/noise"
)

const testPSK = "seJlx7BCna54FicYF6Sg4xUB1y+8cUpFHJRm9+eKf2c="

// ---------------------------------------------------------------- framing

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := &frameConn{rw: &buf}

	bodies := [][]byte{
		[]byte("hello"),
		{0x00},
		bytes.Repeat([]byte{0xAB}, 1024),
	}
	for _, body := range bodies {
		if err := w.writeFrame(body); err != nil {
			t.Fatalf("writeFrame(%d bytes): %v", len(body), err)
		}
	}

	r := &frameConn{rw: &buf}
	for _, want := range bodies {
		got, err := r.readFrame()
		if err != nil {
			t.Fatalf("readFrame: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("frame mismatch: got %d bytes, want %d", len(got), len(want))
		}
	}
}

// The client hello is an empty frame, so a zero-length body must survive the
// round trip rather than being treated as a malformed frame.
func TestFrameEmptyBody(t *testing.T) {
	var buf bytes.Buffer
	if err := (&frameConn{rw: &buf}).writeFrame(nil); err != nil {
		t.Fatal(err)
	}
	if got := buf.Bytes(); !bytes.Equal(got, []byte{indicatorNoise, 0, 0}) {
		t.Fatalf("empty frame = % x, want 01 00 00", got)
	}

	body, err := (&frameConn{rw: &buf}).readFrame()
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("want empty body, got %d bytes", len(body))
	}
}

// A 0x00 indicator means the device has encryption disabled. That is a
// configuration mistake worth naming, not a parse error.
func TestReadFrameRejectsPlaintextIndicator(t *testing.T) {
	buf := bytes.NewBuffer([]byte{0x00, 0x00, 0x01, 0x42})
	_, err := (&frameConn{rw: buf}).readFrame()
	if err == nil {
		t.Fatal("want error for plaintext indicator")
	}
	if !strings.Contains(err.Error(), "encryption") {
		t.Errorf("error should mention encryption: %v", err)
	}
}

func TestReadFrameShortBodyIsAnError(t *testing.T) {
	// Header claims 16 bytes, stream carries 2.
	buf := bytes.NewBuffer([]byte{indicatorNoise, 0x00, 0x10, 0x01, 0x02})
	if _, err := (&frameConn{rw: buf}).readFrame(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("want ErrUnexpectedEOF, got %v", err)
	}
}

func TestWriteFrameRejectsOversizedBody(t *testing.T) {
	var buf bytes.Buffer
	err := (&frameConn{rw: &buf}).writeFrame(make([]byte, 0x10000))
	if err == nil {
		t.Fatal("want error for body exceeding the 16-bit length field")
	}
	if buf.Len() != 0 {
		t.Error("oversized frame must not write a partial header")
	}
}

// ---------------------------------------------------------- inner framing

func TestInnerRoundTrip(t *testing.T) {
	cases := []struct {
		id      uint32
		payload []byte
	}{
		{1, []byte("hi")},
		{0xFFFF, nil},
		{10, bytes.Repeat([]byte{7}, 300)},
	}
	for _, c := range cases {
		id, payload, err := decodeInner(encodeInner(c.id, c.payload))
		if err != nil {
			t.Fatalf("id %d: %v", c.id, err)
		}
		if id != c.id {
			t.Errorf("id = %d, want %d", id, c.id)
		}
		if !bytes.Equal(payload, c.payload) {
			t.Errorf("id %d payload mismatch", c.id)
		}
	}
}

func TestDecodeInnerRejectsTruncated(t *testing.T) {
	for _, b := range [][]byte{
		{},
		{0x00},
		{0x00, 0x01, 0x00},
		// Header claims a 5-byte payload but only 1 byte follows.
		{0x00, 0x01, 0x00, 0x05, 0xFF},
	} {
		if _, _, err := decodeInner(b); err == nil {
			t.Errorf("decodeInner(% x) should fail", b)
		}
	}
}

// ---------------------------------------------------------- server hello

func TestParseServerHello(t *testing.T) {
	hello := append([]byte{chosenProtoNoise}, []byte("ce2a50\x0098:a3:16:ce:2a:50\x00")...)
	name, err := parseServerHello(hello)
	if err != nil {
		t.Fatalf("parseServerHello: %v", err)
	}
	if name != "ce2a50" {
		t.Errorf("node name = %q, want ce2a50", name)
	}
}

func TestParseServerHelloRejectsUnknownProto(t *testing.T) {
	_, err := parseServerHello([]byte{0x02, 'x', 0})
	if err == nil {
		t.Fatal("want error for unsupported proto")
	}
	if !strings.Contains(err.Error(), "0x02") {
		t.Errorf("error should report the offending byte: %v", err)
	}
}

func TestParseServerHelloShort(t *testing.T) {
	if _, err := parseServerHello(nil); err == nil {
		t.Error("want error for empty server hello")
	}
}

// ------------------------------------------------------------- handshake

func TestHandshakeRejectsBadPSK(t *testing.T) {
	for _, psk := range []string{"not base64!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		var buf bytes.Buffer
		if _, _, _, err := handshake(&frameConn{rw: &buf}, psk); err == nil {
			t.Errorf("psk %q accepted", psk)
		}
	}
}

// A PSK mismatch is the most common real failure. The device explains itself in
// the handshake body, and that explanation must reach the operator.
func TestHandshakeSurfacesDeviceRejection(t *testing.T) {
	dev := newFakeDevice(t, "ce2a50")
	dev.rejectWith = "Handshake MAC failure"

	_, _, _, err := handshakeAgainst(t, dev)
	if err == nil {
		t.Fatal("want handshake error")
	}
	if !strings.Contains(err.Error(), "Handshake MAC failure") {
		t.Errorf("device's reason should be surfaced: %v", err)
	}
}

// The whole point: prove the NNpsk0 exchange completes and that the resulting
// cipher states interoperate, with no hardware involved.
// verifies SPEC §3.2
func TestHandshakeCompletesAndCipherStatesInteroperate(t *testing.T) {
	dev := newFakeDevice(t, "ce2a50")

	send, recv, nodeName, err := handshakeAgainst(t, dev)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if nodeName != "ce2a50" {
		t.Errorf("node name = %q, want ce2a50", nodeName)
	}
	if send == nil || recv == nil {
		t.Fatal("handshake returned nil cipher states")
	}

	devSend, devRecv := dev.ciphers()

	// Client -> device.
	ct, err := send.Encrypt(nil, nil, []byte("to device"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := devRecv.Decrypt(nil, nil, ct)
	if err != nil {
		t.Fatalf("device could not decrypt client traffic: %v", err)
	}
	if string(pt) != "to device" {
		t.Errorf("device read %q", pt)
	}

	// Device -> client. Asserts we did not swap cs1/cs2, which would only show
	// up as a decrypt failure on the second message against real hardware.
	ct, err = devSend.Encrypt(nil, nil, []byte("from device"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err = recv.Decrypt(nil, nil, ct)
	if err != nil {
		t.Fatalf("client could not decrypt device traffic: %v", err)
	}
	if string(pt) != "from device" {
		t.Errorf("client read %q", pt)
	}
}

// A wrong key must fail locally the same way it fails on hardware, so the
// error path is exercised rather than assumed.
func TestHandshakeFailsOnPSKMismatch(t *testing.T) {
	dev := newFakeDevice(t, "ce2a50")
	other := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))

	client, server := newPipe(t)
	go dev.serve(server)

	if _, _, _, err := handshake(&frameConn{rw: client}, other); err == nil {
		t.Fatal("mismatched PSK completed the handshake")
	}
}

// ------------------------------------------------------------ fake device

// fakeDevice is the responder half of the ESPHome Noise handshake. It exists so
// transport behavior is tested hermetically; hardware tests are smoke tests.
type fakeDevice struct {
	t          *testing.T
	name       string
	psk        []byte
	rejectWith string

	done     chan struct{}
	cs1, cs2 *noise.CipherState
}

func newFakeDevice(t *testing.T, name string) *fakeDevice {
	t.Helper()
	psk, err := base64.StdEncoding.DecodeString(testPSK)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeDevice{t: t, name: name, psk: psk, done: make(chan struct{})}
}

// ciphers returns the device's (send, recv) states once the handshake is done.
func (d *fakeDevice) ciphers() (send, recv *noise.CipherState) {
	<-d.done
	// Responder: cs2 encrypts to the initiator, cs1 decrypts from it.
	return d.cs2, d.cs1
}

func (d *fakeDevice) serve(rw io.ReadWriter) {
	defer close(d.done)
	fc := &frameConn{rw: rw}

	// Client hello: empty frame.
	if _, err := fc.readFrame(); err != nil {
		return
	}

	hello := append([]byte{chosenProtoNoise}, []byte(d.name+"\x0098:a3:16:ce:2a:50\x00")...)
	if err := fc.writeFrame(hello); err != nil {
		return
	}

	msg, err := fc.readFrame()
	if err != nil || len(msg) < 1 || msg[0] != handshakeOK {
		return
	}

	if d.rejectWith != "" {
		_ = fc.writeFrame(append([]byte{0x01}, []byte(d.rejectWith)...))
		return
	}

	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:           noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256),
		Pattern:               noise.HandshakeNN,
		Initiator:             false,
		Prologue:              noisePrologue,
		PresharedKey:          d.psk,
		PresharedKeyPlacement: 0,
	})
	if err != nil {
		return
	}
	if _, _, _, err := hs.ReadMessage(nil, msg[1:]); err != nil {
		// Mirrors real firmware: explain the failure in the response body.
		_ = fc.writeFrame(append([]byte{0x01}, []byte("Handshake MAC failure")...))
		return
	}

	out, cs1, cs2, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return
	}
	d.cs1, d.cs2 = cs1, cs2
	_ = fc.writeFrame(append([]byte{handshakeOK}, out...))
}

func handshakeAgainst(t *testing.T, d *fakeDevice) (send, recv *noise.CipherState, nodeName string, err error) {
	t.Helper()
	client, server := newPipe(t)
	go d.serve(server)
	return handshake(&frameConn{rw: client}, testPSK)
}
