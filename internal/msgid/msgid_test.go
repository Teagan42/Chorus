package msgid

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/teaganglenn/chorus/internal/pb"
)

// The id table is derived from descriptors at init. If codegen ever drops the
// `id` MessageOption, every send fails at runtime with no obvious cause, so
// assert the table is populated rather than trusting it.
func TestKnownIDsIsPopulated(t *testing.T) {
	// ESPHome has well over a hundred API messages; a handful means the
	// extension was lost and only a few descriptors carried it.
	if n := Known(); n < 100 {
		t.Fatalf("Known() = %d; descriptors appear to be missing the id option", n)
	}
}

// These ids are wire protocol, not implementation detail: the device will not
// renumber them, and a change here means we would talk past the hardware.
func TestForReturnsStableWireIDs(t *testing.T) {
	cases := []struct {
		msg  proto.Message
		want uint32
	}{
		{&pb.HelloRequest{}, 1},
		{&pb.HelloResponse{}, 2},
		{&pb.DisconnectRequest{}, 5},
		{&pb.DeviceInfoRequest{}, 9},
		{&pb.DeviceInfoResponse{}, 10},
	}
	for _, c := range cases {
		got, err := For(c.msg)
		if err != nil {
			t.Errorf("For(%T) error: %v", c.msg, err)
			continue
		}
		if got != c.want {
			t.Errorf("For(%T) = %d, want %d", c.msg, got, c.want)
		}
	}
}

func TestNewRoundTripsWithFor(t *testing.T) {
	for _, original := range []proto.Message{
		&pb.HelloRequest{},
		&pb.DeviceInfoRequest{},
		&pb.SubscribeLogsRequest{},
	} {
		id, err := For(original)
		if err != nil {
			t.Fatalf("For(%T): %v", original, err)
		}
		got, err := New(id)
		if err != nil {
			t.Fatalf("New(%d): %v", id, err)
		}
		if got.ProtoReflect().Descriptor().FullName() != original.ProtoReflect().Descriptor().FullName() {
			t.Errorf("id %d round-tripped to %T, want %T", id, got, original)
		}
	}
}

// New must report unknown ids rather than return nil, so Recv can skip the
// frame instead of desyncing a cipher state that has already advanced.
func TestNewRejectsUnknownID(t *testing.T) {
	m, err := New(0xFFFF)
	if err == nil {
		t.Fatalf("New(0xFFFF) = %v, want error", m)
	}
	if m != nil {
		t.Errorf("New should return a nil message with its error, got %T", m)
	}
}

func TestNewAllocatesIndependentMessages(t *testing.T) {
	id, err := For(&pb.HelloRequest{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(id)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(id)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("New returned the same instance twice; concurrent Recv would corrupt it")
	}
	a.(*pb.HelloRequest).ClientInfo = "mutated"
	if b.(*pb.HelloRequest).ClientInfo != "" {
		t.Error("messages from New share state")
	}
}

// A message with no `id` option is not sendable; the error must say so rather
// than silently encoding id 0.
func TestForRejectsMessageWithoutIDOption(t *testing.T) {
	if id, err := For(&pb.VoiceAssistantAudioSettings{}); err == nil {
		t.Errorf("For(sub-message) = %d, want error", id)
	}
}
