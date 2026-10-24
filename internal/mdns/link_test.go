package mdns_test

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/mdns"
	"github.com/teagan42/chorus/internal/mdns/mdnstest"
)

// The household's two satellites, as their ESPHome firmware advertises them.
var (
	kitchen = mdnstest.Node{
		Name: "kitchen-satellite1-a1b2c3", Friendly: "Kitchen Satellite", MAC: "f0f5bda1b2c3",
		Board: "esp32-s3-devkitc-1", Addr: netip.MustParseAddr("192.168.1.31"),
	}
	office = mdnstest.Node{
		Name: "office-voice-pe-5c7e91", Friendly: "Office Voice PE", MAC: "20f83b5c7e91",
		Board: "esp32-s3-devkitc-1", Addr: netip.MustParseAddr("192.168.1.42"),
	}
)

// Over a real socket, a node is found by its name alone: the instance's
// SRV gives the native API's port, its address rides along.
//
// verifies SPEC §13
func TestTheKitchenSatelliteIsFoundByItsNodeName(t *testing.T) {
	link := mdnstest.Listen(t)
	link.Advertise(kitchen)
	link.Advertise(office)
	r := &mdns.Resolver{To: link.Addr(), Timers: never{}}

	got, err := r.Node(t.Context(), "kitchen-satellite1-a1b2c3")
	if err != nil {
		t.Fatalf("Node: %v", err)
	}
	if got.Addr != kitchen.Addr || got.Port != 6053 || got.TTL != 10*time.Second {
		t.Errorf("found %+v, want %v:6053 for a legacy answer's 10s", got, kitchen.Addr)
	}
	if q := link.Queries(); len(q) != 1 || q[0] != "kitchen-satellite1-a1b2c3._esphomelib._tcp.local. SRV" {
		t.Errorf("asked %v, want the instance's SRV alone", q)
	}
}

// A responder that leaves the address out is asked for the host as well.
func TestAnInstanceReplyWithoutAnAddressIsFollowedToTheHost(t *testing.T) {
	link := mdnstest.Listen(t)
	link.Advertise(mdnstest.Node{Name: office.Name, Addr: office.Addr, Port: 6054})
	link.SplitAddress()
	r := &mdns.Resolver{To: link.Addr(), Timers: never{}}

	got, err := r.Node(t.Context(), "office-voice-pe-5c7e91")
	if err != nil {
		t.Fatalf("Node: %v", err)
	}
	if got.Addr != office.Addr || got.Port != 6054 {
		t.Errorf("found %+v, want %v:6054", got, office.Addr)
	}
	want := []string{"office-voice-pe-5c7e91._esphomelib._tcp.local. SRV", "office-voice-pe-5c7e91.local. A"}
	if q := link.Queries(); len(q) != 2 || q[0] != want[0] || q[1] != want[1] {
		t.Errorf("asked %v, want %v", q, want)
	}
}

func TestTheOfficeVoicePEIsFoundByItsLocalName(t *testing.T) {
	link := mdnstest.Listen(t)
	link.Advertise(office)
	r := &mdns.Resolver{To: link.Addr(), Timers: never{}}

	got, err := r.Host(t.Context(), "office-voice-pe-5c7e91.local")
	if err != nil {
		t.Fatalf("Host: %v", err)
	}
	if got.Addr != office.Addr {
		t.Errorf("found %v, want %v", got.Addr, office.Addr)
	}
}

// An unplugged satellite is asked for three times, then reported missing.
func TestAnUnpluggedSatelliteIsNoAnswer(t *testing.T) {
	link := mdnstest.Listen(t)
	link.Advertise(kitchen)
	link.Withdraw(kitchen.Name)
	r := &mdns.Resolver{To: link.Addr(), Timers: once{asked: link.Asked()}}

	_, err := r.Node(t.Context(), kitchen.Name)
	if !errors.Is(err, mdns.ErrNoAnswer) {
		t.Errorf("err = %v, want ErrNoAnswer", err)
	}
	if got := len(link.Queries()); got != 3 {
		t.Errorf("asked %d times, want 3", got)
	}
}

type never struct{}

func (never) After(time.Duration) <-chan time.Time { return nil }

// once ends each wait as soon as the link has heard the query it follows,
// so a silent link is a fast one.
type once struct{ asked <-chan struct{} }

func (o once) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	go func() {
		<-o.asked
		ch <- time.Time{}
	}()
	return ch
}
