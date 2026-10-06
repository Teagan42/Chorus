package esphome

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/flynn/noise"
	"google.golang.org/protobuf/proto"

	"github.com/teaganglenn/chorus/internal/msgid"
	"github.com/teaganglenn/chorus/internal/pb"
)

// fakeAPI is a fake satellite that completes the handshake and then speaks the
// native API. CONTRIBUTING.md mandates this: behavior is the fake's job,
// hardware tests are smoke tests.
type fakeAPI struct {
	t  *testing.T
	fc *frameConn

	send, recv *noise.CipherState

	mu   sync.Mutex
	seen []proto.Message

	// Messages to emit before answering a request, mimicking the unprompted
	// entity-state traffic a real device sends on connect.
	preamble []proto.Message

	deviceName string
}

func newFakeAPI(t *testing.T, rw io.ReadWriter) *fakeAPI {
	t.Helper()
	return &fakeAPI{t: t, fc: &frameConn{rw: rw}, deviceName: "ce2a50"}
}

func (f *fakeAPI) handshake() error {
	psk, err := base64.StdEncoding.DecodeString(testPSK)
	if err != nil {
		return err
	}
	if _, err := f.fc.readFrame(); err != nil {
		return err
	}
	hello := append([]byte{chosenProtoNoise}, []byte(f.deviceName+"\x0098:a3:16:ce:2a:50\x00")...)
	if err := f.fc.writeFrame(hello); err != nil {
		return err
	}
	msg, err := f.fc.readFrame()
	if err != nil {
		return err
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:           noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256),
		Pattern:               noise.HandshakeNN,
		Initiator:             false,
		Prologue:              noisePrologue,
		PresharedKey:          psk,
		PresharedKeyPlacement: 0,
	})
	if err != nil {
		return err
	}
	if _, _, _, err := hs.ReadMessage(nil, msg[1:]); err != nil {
		return err
	}
	out, cs1, cs2, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return err
	}
	// Responder: cs2 encrypts to the initiator, cs1 decrypts from it.
	f.send, f.recv = cs2, cs1
	return f.fc.writeFrame(append([]byte{handshakeOK}, out...))
}

func (f *fakeAPI) write(m proto.Message) error {
	id, err := msgid.For(m)
	if err != nil {
		return err
	}
	payload, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	ct, err := f.send.Encrypt(nil, nil, encodeInner(id, payload))
	if err != nil {
		return err
	}
	return f.fc.writeFrame(ct)
}

// writeRawID emits a frame carrying an id our descriptors do not know, which is
// what happens when the device runs a newer ESPHome than we generated from.
func (f *fakeAPI) writeRawID(id uint32, payload []byte) error {
	ct, err := f.send.Encrypt(nil, nil, encodeInner(id, payload))
	if err != nil {
		return err
	}
	return f.fc.writeFrame(ct)
}

func (f *fakeAPI) read() (proto.Message, error) {
	frame, err := f.fc.readFrame()
	if err != nil {
		return nil, err
	}
	pt, err := f.recv.Decrypt(nil, nil, frame)
	if err != nil {
		return nil, err
	}
	id, payload, err := decodeInner(pt)
	if err != nil {
		return nil, err
	}
	m, err := msgid.New(id)
	if err != nil {
		return nil, err
	}
	if err := proto.Unmarshal(payload, m); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.seen = append(f.seen, m)
	f.mu.Unlock()
	return m, nil
}

// serveOpen answers the Hello/DeviceInfo opening sequence.
func (f *fakeAPI) serveOpen() {
	if err := f.handshake(); err != nil {
		return
	}
	for {
		m, err := f.read()
		if err != nil {
			return
		}
		switch m.(type) {
		case *pb.HelloRequest:
			for _, p := range f.preamble {
				if err := f.write(p); err != nil {
					return
				}
			}
			if err := f.write(&pb.HelloResponse{
				ApiVersionMajor: 1,
				ApiVersionMinor: 10,
				Name:            f.deviceName,
				ServerInfo:      "fake",
			}); err != nil {
				return
			}
		case *pb.DeviceInfoRequest:
			if err := f.write(&pb.DeviceInfoResponse{
				Name:           f.deviceName,
				Model:          "satellite1",
				EsphomeVersion: "2026.1.0",
				MacAddress:     "98:a3:16:ce:2a:50",
				FriendlyName:   "Living Room",
			}); err != nil {
				return
			}
		}
	}
}

func (f *fakeAPI) received() []proto.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proto.Message(nil), f.seen...)
}

// dialFake wires a Client to an in-process fake over net.Pipe, bypassing TCP.
func dialFake(t *testing.T, setup func(*fakeAPI)) (*Client, *fakeAPI) {
	t.Helper()
	clientConn, serverConn := newPipe(t)

	fake := newFakeAPI(t, serverConn)
	if setup != nil {
		setup(fake)
	}
	go fake.serveOpen()

	c := &Client{conn: clientConn, frames: &frameConn{rw: clientConn}}
	send, recv, nodeName, err := handshake(c.frames, testPSK)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	c.send, c.recv, c.nodeName = send, recv, nodeName
	return c, fake
}

// verifies SPEC §3.2
func TestClientOpenCompletesHelloAndDeviceInfo(t *testing.T) {
	c, fake := dialFake(t, nil)

	if err := c.open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if c.APIVersion != [2]uint32{1, 10} {
		t.Errorf("APIVersion = %v, want [1 10]", c.APIVersion)
	}
	if c.Name() != "ce2a50" {
		t.Errorf("Name() = %q", c.Name())
	}
	if c.DeviceInfoMsg.GetModel() != "satellite1" {
		t.Errorf("model = %q", c.DeviceInfoMsg.GetModel())
	}

	// No ConnectRequest: password auth was removed in ESPHome 2026.1.0 and the
	// Noise handshake is the authentication. Sending one would be an error.
	for _, m := range fake.received() {
		if name := string(m.ProtoReflect().Descriptor().Name()); strings.Contains(name, "Connect") {
			t.Errorf("client sent %s; the handshake is the authentication", name)
		}
	}
}

// Devices emit entity state unprompted. If expect() did not skip past it, the
// opening sequence would fail intermittently against real hardware.
// verifies SPEC §3.2
func TestClientOpenToleratesUnpromptedStateSpam(t *testing.T) {
	c, _ := dialFake(t, func(f *fakeAPI) {
		f.preamble = []proto.Message{
			&pb.SubscribeLogsResponse{Message: []byte("boot")},
			&pb.PingRequest{},
			&pb.SubscribeLogsResponse{Message: []byte("wifi up")},
		}
	})

	if err := c.open(); err != nil {
		t.Fatalf("open with preamble: %v", err)
	}
	if c.DeviceInfoMsg.GetModel() != "satellite1" {
		t.Errorf("model = %q", c.DeviceInfoMsg.GetModel())
	}
}

// Round-trips a message both ways through the real encrypt/decrypt path.
func TestClientSendRecvRoundTrip(t *testing.T) {
	clientConn, serverConn := newPipe(t)
	fake := newFakeAPI(t, serverConn)

	type result struct {
		got proto.Message
		err error
	}
	echoed := make(chan result, 1)

	go func() {
		if err := fake.handshake(); err != nil {
			echoed <- result{err: err}
			return
		}
		m, err := fake.read()
		if err != nil {
			echoed <- result{err: err}
			return
		}
		// Report before replying. net.Pipe is unbuffered, so writing first
		// would block here until the client reads, while the client is still
		// waiting on this channel.
		echoed <- result{got: m}

		// Answer so the client's Recv path is exercised too.
		_ = fake.write(&pb.PingResponse{})
	}()

	c := &Client{conn: clientConn, frames: &frameConn{rw: clientConn}}
	send, recv, name, err := handshake(c.frames, testPSK)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	c.send, c.recv, c.nodeName = send, recv, name

	if err := c.Send(&pb.SubscribeLogsRequest{Level: pb.LogLevel_LOG_LEVEL_DEBUG}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	res := <-echoed
	if res.err != nil {
		t.Fatalf("device side failed: %v", res.err)
	}
	req, ok := res.got.(*pb.SubscribeLogsRequest)
	if !ok {
		t.Fatalf("device received %T, want SubscribeLogsRequest", res.got)
	}
	// Proves the payload survived, not just the message id.
	if req.GetLevel() != pb.LogLevel_LOG_LEVEL_DEBUG {
		t.Errorf("level = %v, want DEBUG", req.GetLevel())
	}

	reply, err := c.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if _, ok := reply.(*pb.PingResponse); !ok {
		t.Errorf("got %T, want PingResponse", reply)
	}
}

// An unknown message id must not desync the stream: the cipher state has
// already advanced, so the only safe action is to skip that frame and continue.
// verifies SPEC §3.2
func TestClientRecvSkipsUnknownIDsWithoutDesync(t *testing.T) {
	clientConn, serverConn := newPipe(t)
	fake := newFakeAPI(t, serverConn)

	go func() {
		if err := fake.handshake(); err != nil {
			return
		}
		// 0xFFF0 is not in our descriptors.
		if err := fake.writeRawID(0xFFF0, []byte("from the future")); err != nil {
			return
		}
		_ = fake.write(&pb.PingResponse{})
	}()

	c := &Client{conn: clientConn, frames: &frameConn{rw: clientConn}}
	send, recv, name, err := handshake(c.frames, testPSK)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	c.send, c.recv, c.nodeName = send, recv, name

	_, err = c.Recv()
	if err == nil {
		t.Fatal("want error for unknown id")
	}
	if !IsUnknownMessage(err) {
		t.Fatalf("error should be classified recoverable: %v", err)
	}

	// The real assertion: the next message still decrypts.
	m, err := c.Recv()
	if err != nil {
		t.Fatalf("stream desynced after unknown id: %v", err)
	}
	if _, ok := m.(*pb.PingResponse); !ok {
		t.Errorf("got %T, want PingResponse", m)
	}
}

// IsUnknownMessage must survive wrapping, or a caller that adds context to the
// error silently turns a recoverable skip into a dropped connection.
func TestIsUnknownMessageSurvivesWrapping(t *testing.T) {
	base := unknownMessage{id: 999, err: errors.New("unknown id 999")}
	if !IsUnknownMessage(base) {
		t.Fatal("bare unknownMessage not recognized")
	}
	wrapped := fmt.Errorf("recv: %w", error(base))
	if !IsUnknownMessage(wrapped) {
		t.Error("wrapped unknownMessage not recognized")
	}
	if IsUnknownMessage(errors.New("unrelated")) {
		t.Error("unrelated error misclassified as unknown message")
	}
	if IsUnknownMessage(nil) {
		t.Error("nil misclassified as unknown message")
	}
}

func TestClientOpenReportsDeviceDisconnect(t *testing.T) {
	clientConn, serverConn := newPipe(t)
	fake := newFakeAPI(t, serverConn)

	go func() {
		if err := fake.handshake(); err != nil {
			return
		}
		if _, err := fake.read(); err != nil { // HelloRequest
			return
		}
		_ = fake.write(&pb.DisconnectRequest{})
	}()

	c := &Client{conn: clientConn, frames: &frameConn{rw: clientConn}}
	send, recv, name, err := handshake(c.frames, testPSK)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	c.send, c.recv, c.nodeName = send, recv, name

	err = c.open()
	if err == nil {
		t.Fatal("want error when the device disconnects")
	}
	if !strings.Contains(err.Error(), "disconnect") {
		t.Errorf("error should name the disconnect: %v", err)
	}
}

// Send holds a mutex because the Noise cipher state carries a nonce; concurrent
// encryption without it produces frames the device rejects. Run under -race.
// verifies SPEC §3.2
func TestClientSendIsSafeForConcurrentUse(t *testing.T) {
	clientConn, serverConn := newPipe(t)
	fake := newFakeAPI(t, serverConn)

	const senders = 8
	const perSender = 10
	const total = senders * perSender

	// Closed once the fake has decrypted every frame. Reading fake.received()
	// straight after wg.Wait() races the fake's own bookkeeping.
	drained := make(chan error, 1)
	go func() {
		if err := fake.handshake(); err != nil {
			drained <- err
			return
		}
		for range total {
			if _, err := fake.read(); err != nil {
				drained <- err
				return
			}
		}
		drained <- nil
	}()

	c := &Client{conn: clientConn, frames: &frameConn{rw: clientConn}}
	send, recv, name, err := handshake(c.frames, testPSK)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	c.send, c.recv, c.nodeName = send, recv, name

	var wg sync.WaitGroup
	errs := make(chan error, senders*perSender)
	for range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perSender {
				if err := c.Send(&pb.PingRequest{}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent Send failed: %v", err)
	}

	// Every frame must decrypt on the device side. A nonce race surfaces here
	// as a chacha20poly1305 authentication failure.
	if err := <-drained; err != nil {
		t.Fatalf("device failed to decrypt concurrent traffic: %v", err)
	}
	if got := len(fake.received()); got != total {
		t.Errorf("device decrypted %d of %d frames", got, total)
	}
}

func TestDialRejectsUnreachableAddress(t *testing.T) {
	// Port 1 on the loopback refuses fast and needs no fixture.
	_, err := Dial(t.Context(), "127.0.0.1:1", testPSK)
	if err == nil {
		t.Fatal("want dial error")
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) && !strings.Contains(err.Error(), "dial") {
		t.Errorf("error should identify the dial failure: %v", err)
	}
}
