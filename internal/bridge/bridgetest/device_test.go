package bridgetest_test

import (
	"net"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/bridge/bridgetest"
)

const patience = 5 * time.Second

// sink records what Serve dispatched, on channels so a test can wait for it.
type sink struct {
	wakes  chan string
	played chan bridge.Played
}

func (s sink) OnMic(uint8, []byte) error { return nil }
func (s sink) OnWake(w string) error     { s.wakes <- w; return nil }
func (s sink) OnMute(bridge.Mute) error  { return nil }

func (s sink) OnPlayed(p bridge.Played) error {
	if s.played != nil {
		s.played <- p
	}
	return nil
}

// A daemon accepts the connection and completes the handshake itself, so the
// device has to be able to join a connection it did not create: that is how
// the satellite reaches an orchestrator under test (SPEC §13).
//
// verifies SPEC §3.1
func TestConnectSpeaksHelloOnAnAcceptedConnection(t *testing.T) {
	host, device := net.Pipe()
	t.Cleanup(func() {
		_ = host.Close()
		_ = device.Close()
	})

	dev := bridgetest.Connect(device, 2)
	link, err := bridge.NewLink(host)
	if err != nil {
		t.Fatalf("host link: %v", err)
	}
	if got := link.Hello().MicChannels; got != 2 {
		t.Errorf("MicChannels = %d, want 2", got)
	}

	s := sink{wakes: make(chan string, 1)}
	go func() { _ = link.Serve(t.Context(), s) }()
	dev.SendWake(t, "hey_eddie")
	select {
	case w := <-s.wakes:
		if w != "hey_eddie" {
			t.Errorf("wake = %q", w)
		}
	case <-time.After(patience):
		t.Fatal("the wake never reached the host")
	}
}

// A host that hangs up, or refuses the connection before reading the hello,
// is something a test has to be able to observe.
func TestGoneClosesWhenTheHostHangsUp(t *testing.T) {
	host, device := net.Pipe()
	t.Cleanup(func() { _ = device.Close() })

	dev := bridgetest.Connect(device, 1)
	select {
	case <-dev.Gone():
		t.Fatal("gone before the host did anything")
	default:
	}

	_ = host.Close()
	select {
	case <-dev.Gone():
	case <-time.After(patience):
		t.Fatal("Gone did not close after the host hung up")
	}
}

// Dial keeps working the way every other package's rig expects.
func TestDialStillJoinsBothEnds(t *testing.T) {
	link, dev := bridgetest.Dial(t, 2)
	if got := link.Hello().MicChannels; got != 2 {
		t.Errorf("MicChannels = %d, want 2", got)
	}
	if err := link.SendTTS(make([]byte, 64)); err != nil {
		t.Fatalf("SendTTS: %v", err)
	}
	dev.AwaitTTS(t, 64)
}

// A report the DAC has emitted is not yet a report the host has read. Holding
// the uplink is what lets a test place one on each side of a stop.
func TestHoldUplinkDelaysReportsWithoutReorderingThem(t *testing.T) {
	link, dev := bridgetest.Dial(t, 1)
	s := sink{played: make(chan bridge.Played, 4)}
	go func() { _ = link.Serve(t.Context(), s) }()

	if err := link.SendTTS(make([]byte, 8)); err != nil {
		t.Fatalf("SendTTS: %v", err)
	}
	dev.AwaitTTS(t, 8)

	hold := dev.HoldUplink(t)
	dev.Play(t, 1)
	dev.Play(t, 1)
	dev.Play(t, 1)
	select {
	case p := <-s.played:
		t.Fatalf("a held report reached the host: %+v", p)
	case <-time.After(50 * time.Millisecond):
	}

	// One at a time, so a test can act between two reports that were both
	// already in flight.
	hold.Release(1)
	expectPlayed(t, s.played, 1)
	select {
	case p := <-s.played:
		t.Fatalf("a report still held reached the host: %+v", p)
	case <-time.After(50 * time.Millisecond):
	}

	hold.Lift()
	expectPlayed(t, s.played, 2)
	expectPlayed(t, s.played, 3)

	// Lifted means the wire is back to normal, not open for one frame.
	dev.Play(t, 1)
	expectPlayed(t, s.played, 4)
}

func expectPlayed(t *testing.T, played <-chan bridge.Played, frames uint64) {
	t.Helper()
	select {
	case p := <-played:
		if p.Frames != frames {
			t.Errorf("Frames = %d, want %d: reports arrived out of order", p.Frames, frames)
		}
	case <-time.After(patience):
		t.Fatalf("the report of %d frames never reached the host", frames)
	}
}
