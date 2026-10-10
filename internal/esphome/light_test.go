package esphome

import (
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/teagan42/chorus/internal/msgid"
	"github.com/teagan42/chorus/internal/pb"
)

// The kitchen's LED ring, both ways over the encrypted link: the device
// lists it with the effects chorusd looks for, and the command setting it
// to Thinking arrives whole, as the vendored api.proto's message 32.
//
// verifies SPEC §3.3.1
func TestTheRingIsListedAndCommandedOverTheNoiseLink(t *testing.T) {
	clientConn, serverConn := newPipe(t)
	fake := newFakeAPI(t, serverConn)

	type result struct {
		got proto.Message
		err error
	}
	commanded := make(chan result, 1)
	go func() {
		if err := fake.handshake(); err != nil {
			commanded <- result{err: err}
			return
		}
		ring := &pb.ListEntitiesLightResponse{
			ObjectId: "led_ring", Key: 0x1ed0c0de, Name: "LED Ring",
			Effects: []string{"Listening", "Thinking", "Speaking"},
		}
		if err := fake.write(ring); err != nil {
			commanded <- result{err: err}
			return
		}
		m, err := fake.read()
		commanded <- result{got: m, err: err}
	}()

	c := &Client{conn: clientConn, frames: &frameConn{rw: clientConn}}
	send, recv, name, err := handshake(c.frames, testPSK)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	c.send, c.recv, c.nodeName = send, recv, name

	m, err := c.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	listed, ok := m.(*pb.ListEntitiesLightResponse)
	if !ok || listed.GetKey() != 0x1ed0c0de || !slices.Equal(listed.GetEffects(), []string{"Listening", "Thinking", "Speaking"}) {
		t.Fatalf("listed %v, want the ring with its three effects", m)
	}

	if err := c.Send(&pb.LightCommandRequest{Key: listed.GetKey(), HasState: true, State: true, HasEffect: true, Effect: "Thinking"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	res := <-commanded
	if res.err != nil {
		t.Fatalf("device side failed: %v", res.err)
	}
	cmd, ok := res.got.(*pb.LightCommandRequest)
	if !ok || cmd.GetKey() != 0x1ed0c0de || !cmd.GetState() || !cmd.GetHasEffect() || cmd.GetEffect() != "Thinking" {
		t.Errorf("device received %v, want the ring on, showing Thinking", res.got)
	}
	if id, err := msgid.For(cmd); err != nil || id != 32 {
		t.Errorf("LightCommandRequest is id %d (%v), want 32 as api.proto declares", id, err)
	}
}
