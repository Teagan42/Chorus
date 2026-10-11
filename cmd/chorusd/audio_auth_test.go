package main

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/bridge/bridgetest"
	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/journal"
)

// officeKey is a 32-byte key that is not the inventory's: what someone on
// the LAN who knows a satellite's name, but not its psk, would answer with.
var officeKey = []byte("the office satellite's own key!!")

// as is the identity the inventory gives sat: its name, under its psk.
func as(t *testing.T, sat *config.Satellite) bridgetest.Option {
	t.Helper()
	psk, err := sat.PSKBytes()
	if err != nil {
		t.Fatal(err)
	}
	return bridgetest.As(sat.Name, psk)
}

// refused waits for the daemon to hang up on dev and to log why, naming
// reason, and checks the refused connection left nothing in the journal.
func (r *rig) refused(t *testing.T, dev *bridgetest.Device, reason string) {
	t.Helper()
	gone(t, dev, reason)
	r.logs.await(t, "reason=\""+reason+"\"")
	if n := len(r.store.events()); n != 0 {
		t.Errorf("%d events journalled for a refused connection", n)
	}
	if r.logs.contains("satellite connected") {
		t.Errorf("a refused connection was attached:\n%s", r.logs.String())
	}
}

func (l *logBuffer) contains(substr string) bool { return strings.Contains(l.String(), substr) }

// rawDevice is a satellite's end of a connection driven frame by frame, for
// the devices bridgetest will not play: old firmware, and broken firmware.
type rawDevice struct {
	conn net.Conn
	w    *bridge.Writer
	r    *bridge.Reader
	errs chan error
}

func dialRaw(t *testing.T, r *rig, ip string) *rawDevice {
	t.Helper()
	c := r.ln.dial(t, ip)
	return &rawDevice{conn: c, w: bridge.NewWriter(c), r: bridge.NewReader(c), errs: make(chan error, 1)}
}

// script runs the device's side in the background; net.Pipe would deadlock
// it against the daemon's reads otherwise.
func (d *rawDevice) script(f func() error) { go func() { d.errs <- f() }() }

func (d *rawDevice) hello(version uint8) error {
	return d.w.WriteFrame(bridge.Hello{
		Version: version, SampleRate: bridge.SampleRate, BitsPerSample: bridge.BitsPerSample, MicChannels: 2,
	}.Frame())
}

// challenge reads the frame the daemon sends after the hello.
func (d *rawDevice) challenge() error {
	f, err := d.r.ReadFrame()
	if err != nil {
		return err
	}
	if f.Type != bridge.TypeChallenge {
		return errors.New("the daemon sent " + f.Type.String() + " where the challenge belongs")
	}
	return nil
}

// hungUp waits for the script to end and the daemon to close the connection.
func (d *rawDevice) hungUp(t *testing.T) {
	t.Helper()
	select {
	case err := <-d.errs:
		if err != nil {
			t.Fatalf("device script: %v", err)
		}
	case <-time.After(patience):
		t.Fatal("the device script never finished")
	}
	done := make(chan error, 1)
	go func() {
		_, err := d.r.ReadFrame()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the daemon kept talking to a device it should have refused")
		}
	case <-time.After(patience):
		t.Fatal("the daemon never hung up")
	}
}

// Someone on the LAN who knows the kitchen satellite's name, from the
// kitchen's own address, but answers with a key that is not its psk: the
// MAC fails, the link is refused with that reason, and nothing is heard.
//
// verifies SPEC §3.2.2, §13
func TestAnAnswerUnderTheWrongKeyIsRefused(t *testing.T) {
	r := newRig(t, inventory())

	dev := bridgetest.Connect(r.ln.dial(t, kitchenIP), 2, bridgetest.As("kitchen", officeKey))
	r.refused(t, dev, "bad MAC")
	if !r.logs.contains("kitchen") {
		t.Errorf("the refusal does not name the satellite claimed:\n%s", r.logs.String())
	}

	// The real kitchen, right after, is let in.
	r.join(t, kitchenIP)
}

// verifies SPEC §3.2.2, §13
func TestANameTheInventoryDoesNotListIsRefused(t *testing.T) {
	r := newRig(t, inventory())
	psk, _ := inventory().Satellites[0].PSKBytes()

	dev := bridgetest.Connect(r.ln.dial(t, kitchenIP), 2, bridgetest.As("garage", psk))
	r.refused(t, dev, "unknown name")
	if !r.logs.contains("garage") {
		t.Errorf("the refusal does not name what the device claimed:\n%s", r.logs.String())
	}
}

// The kitchen's name and the kitchen's key, from the office's address. Both
// must hold: a satellite is its name, its key and its address together, so
// a stolen key alone does not let a second host pose as the kitchen.
//
// verifies SPEC §3.2.2, §13
func TestTheRightNameFromTheWrongAddressIsRefused(t *testing.T) {
	r := newRig(t, inventory())

	dev := bridgetest.Connect(r.ln.dial(t, officeIP), 2, as(t, r.dm.byHost[kitchenIP]))
	r.refused(t, dev, "wrong address")
	if r.dm.linked("kitchen") != nil || r.dm.linked("office") != nil {
		t.Error("a link refused for its address was linked anyway")
	}
}

// A satellite still flashed with protocol 2 has no answer to give. It is
// refused at its hello, and the log names both versions, so the fix it
// points at is reflashing the satellite (ADR-0066).
//
// verifies SPEC §3.2.2
func TestAVersion2SatelliteIsRefusedWithBothVersionsNamed(t *testing.T) {
	r := newRig(t, inventory())

	dev := dialRaw(t, r, kitchenIP)
	dev.script(func() error { return dev.hello(2) })
	dev.hungUp(t)
	r.logs.await(t, `reason="protocol version"`)
	if s := r.logs.String(); !strings.Contains(s, "protocol 2") || !strings.Contains(s, "speaks 3") {
		t.Errorf("the refusal does not name the two versions:\n%s", s)
	}
	if n := len(r.store.events()); n != 0 {
		t.Errorf("%d events journalled for a refused connection", n)
	}
}

// A device that says hello and then never answers is let go when the
// injected clock says the handshake has had its time, not before.
//
// verifies SPEC §3.2.2
func TestAHandshakeThatIsNeverAnsweredTimesOut(t *testing.T) {
	clk := newClock()
	r := newRig(t, inventory(), func(d *deps) { d.Timers = clk })

	dev := dialRaw(t, r, kitchenIP)
	dev.script(func() error {
		if err := dev.hello(bridge.ProtocolVersion); err != nil {
			return err
		}
		return dev.challenge()
	})
	clk.awaitWait(t, helloTimeout)
	if err := <-dev.errs; err != nil {
		t.Fatalf("device script: %v", err)
	}
	dev.errs <- nil

	clk.advance(helloTimeout - time.Millisecond)
	if r.logs.contains("refused an audio link") {
		t.Fatal("refused before the handshake's time was up")
	}
	clk.advance(time.Millisecond)
	dev.hungUp(t)
	r.logs.await(t, `reason=timeout`)
}

// Audio sent between the challenge and the answer is refused, not
// buffered for later: nothing counts before the device has proved itself.
//
// verifies SPEC §3.2.2
func TestAudioBeforeTheAnswerIsRefused(t *testing.T) {
	r := newRig(t, inventory())

	dev := dialRaw(t, r, kitchenIP)
	dev.script(func() error {
		if err := dev.hello(bridge.ProtocolVersion); err != nil {
			return err
		}
		if err := dev.challenge(); err != nil {
			return err
		}
		return dev.w.WriteFrame(bridge.Frame{Type: bridge.TypeMic, Payload: voice(9000, chunkBytes)})
	})
	dev.hungUp(t)
	r.logs.await(t, `reason="frame before auth"`)
	if n := len(r.store.events()); n != 0 {
		t.Errorf("%d events journalled for a refused connection", n)
	}
}

// The kitchen satellite reboots without its old connection dropping, and
// dials in again. The second verified connection closes the first, and
// its supervisor is gone before the second's starts: one per device.
//
// verifies SPEC §3.2.2
func TestASecondVerifiedConnectionReplacesTheFirst(t *testing.T) {
	r := newRig(t, inventory())
	first := r.join(t, kitchenIP)
	before := r.dm.linked("kitchen")

	second := r.join(t, kitchenIP)
	select {
	case <-before.Done():
	default:
		t.Fatal("the second link was attached while the first's supervisor still ran")
	}
	gone(t, first, "the replaced link")
	r.logs.await(t, "replaces the satellite's live audio link")

	second.SendWake(t, "hey_eddie")
	r.utter(t, second, r.line("find zeppelin", alan))
	opened := r.store.awaitKind(t, journal.KindSessionOpened, 1)
	if opened.Fields["satellite"] != "kitchen" {
		t.Errorf("session_opened = %v, want kitchen", opened.Fields)
	}
	select {
	case <-second.Gone():
		t.Fatal("the replacing link was hung up on")
	default:
	}
}

// A connection that fails to prove itself does not displace the live link:
// otherwise anyone on the LAN could knock a satellite off by dialing in.
//
// verifies SPEC §3.2.2
func TestARefusedConnectionLeavesTheLiveLinkAlone(t *testing.T) {
	r := newRig(t, inventory())
	live := r.join(t, kitchenIP)
	lst := r.dm.linked("kitchen")

	impostor := bridgetest.Connect(r.ln.dial(t, kitchenIP), 2, bridgetest.As("kitchen", officeKey))
	gone(t, impostor, "the impostor")
	r.logs.await(t, `reason="bad MAC"`)

	if r.dm.linked("kitchen") != lst {
		t.Fatal("the refused connection replaced the live link")
	}
	select {
	case <-live.Gone():
		t.Fatal("the live link was hung up on for an impostor")
	case <-lst.Done():
		t.Fatal("the live link's listener closed for an impostor")
	default:
	}
}
