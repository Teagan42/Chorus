package bridgetest_test

import (
	"net"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/bridge/bridgetest"
)

const patience = 5 * time.Second

// sink records what Serve dispatched, on a channel so a test can wait for it.
type sink struct{ wakes chan string }

func (s sink) OnMic(uint8, []byte) error    { return nil }
func (s sink) OnWake(w string) error        { s.wakes <- w; return nil }
func (s sink) OnPlayed(bridge.Played) error { return nil }
func (s sink) OnMute(bridge.Mute) error     { return nil }

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
