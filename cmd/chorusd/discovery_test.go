package main

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/discover"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/mdns"
	"github.com/teagan42/chorus/internal/mdns/mdnstest"
)

// The household with nothing pinned to an IP: the kitchen's Satellite1 is
// found by its node name, the office's Voice PE by its .local name.
const (
	kitchenNode = "kitchen-satellite1-a1b2c3"
	officeNode  = "office-voice-pe-5c7e91"
)

func foundInventory() *config.Config {
	return &config.Config{Satellites: []config.Satellite{
		{Name: kitchenNode, PSK: goodPSK, Room: "kitchen", Profile: "satellite1"},
		{Name: officeNode, Address: officeNode + ".local:6053", PSK: goodPSK, Room: "office", Profile: "voice-pe"},
	}}
}

// homeLink is the household's network as mDNS sees it, with both
// satellites on the addresses their leases gave them.
func homeLink(t *testing.T) *mdnstest.Responder {
	t.Helper()
	link := mdnstest.Listen(t)
	link.Advertise(mdnstest.Node{Name: kitchenNode, Friendly: "Kitchen Satellite", MAC: "f0f5bda1b2c3", Board: "esp32-s3-devkitc-1", Addr: netip.MustParseAddr(kitchenIP)})
	link.Advertise(mdnstest.Node{Name: officeNode, Friendly: "Office Voice PE", MAC: "20f83b5c7e91", Board: "esp32-s3-devkitc-1", Addr: netip.MustParseAddr(officeIP)})
	return link
}

// onLink finds satellites over real mDNS queries to link. Every node the
// tests ask for answers, so no query waits out its timeout.
func onLink(link *mdnstest.Responder) func(*deps) {
	return func(d *deps) {
		d.Locator = &discover.Network{MDNS: &mdns.Resolver{To: link.Addr(), Timers: neverTimers{}}}
	}
}

// found waits until a satellite is found at ip, and is the only one there.
func (r *rig) found(t *testing.T, ip string) {
	t.Helper()
	await(t, "a satellite found at "+ip, func() bool { return r.at(ip) != nil })
}

// A satellite the inventory gives no IP is found on the network, then
// heard from and dialled where it was found: name, key and the found
// address must all hold, as for an address the inventory pins.
//
// verifies SPEC §13, §3.2.2
func TestASatelliteFoundOnTheNetworkIsHeardAndDialled(t *testing.T) {
	link := homeLink(t)
	dialer := newDialer()
	r := newRig(t, foundInventory(), onLink(link), func(d *deps) { d.Native = dialer })

	r.found(t, kitchenIP)
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("what's the weather like this afternoon", alan))

	opened := r.store.awaitKind(t, journal.KindSessionOpened, 1)
	if opened.Fields["satellite"] != kitchenNode || opened.Fields["speaker_id"] != "alan" {
		t.Errorf("session_opened = %v, want %s/alan", opened.Fields, kitchenNode)
	}
	await(t, "both native APIs dialled", func() bool { return len(dialer.dialed()) == 2 })
	got := dialer.dialed()
	slices.Sort(got)
	want := []string{kitchenIP + ":6053 " + goodPSK, officeIP + ":6053 " + goodPSK}
	if !slices.Equal(got, want) {
		t.Errorf("dialled %v, want %v: each where it was found", got, want)
	}
}

// Overnight the router renews the kitchen satellite onto another address.
// Its first link from there matches nobody and is refused, which has the
// daemon look again; the next is heard, and the old address is nobody's.
//
// verifies SPEC §13
func TestASatelliteThatMovesIsFoundAgainWhenItDialsFromItsNewAddress(t *testing.T) {
	const renewed = "10.0.0.21"
	link := homeLink(t)
	r := newRig(t, foundInventory(), onLink(link))
	r.found(t, kitchenIP)
	r.join(t, kitchenIP)

	link.Advertise(mdnstest.Node{Name: kitchenNode, Addr: netip.MustParseAddr(renewed)})
	first := r.join(t, renewed)
	gone(t, first, "a link from the new address before it was found there")
	r.logs.await(t, "satellite moved")
	if !r.logs.contains("from=" + kitchenIP + ":6053 to=" + renewed + ":6053") {
		t.Errorf("the move does not say where from and to:\n%s", r.logs.String())
	}

	dev := r.join(t, renewed)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("turn on the kitchen lights", alan))
	if opened := r.store.awaitKind(t, journal.KindSessionOpened, 1); opened.Fields["satellite"] != kitchenNode {
		t.Errorf("session_opened on %s, want %s", opened.Fields["satellite"], kitchenNode)
	}
	if sat := r.at(kitchenIP); sat != nil {
		t.Errorf("the old address still matches %s", sat.Name)
	}
}

// Two satellites found at one address could not be told apart, so links
// from it are refused until one of them is found somewhere else.
//
// verifies SPEC §13
func TestSatellitesFoundAtOneAddressAreRefusedUntilOneMoves(t *testing.T) {
	link := homeLink(t)
	link.Advertise(mdnstest.Node{Name: officeNode, Addr: netip.MustParseAddr(kitchenIP)})
	r := newRig(t, foundInventory(), onLink(link))
	r.logs.await(t, "satellites share an address")

	gone(t, r.join(t, kitchenIP), "a link from a shared address")

	// The office's lease is fixed; its link from there is refused once more,
	// and the daemon, looking again, finds it.
	link.Advertise(mdnstest.Node{Name: officeNode, Addr: netip.MustParseAddr(officeIP)})
	gone(t, r.join(t, officeIP), "a link from where the office was not yet found")
	r.found(t, officeIP)

	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("set a timer for the pasta", alan))
	if opened := r.store.awaitKind(t, journal.KindSessionOpened, 1); opened.Fields["satellite"] != kitchenNode {
		t.Errorf("session_opened on %s, want %s", opened.Fields["satellite"], kitchenNode)
	}
}

// A satellite unplugged when the daemon starts is looked for with backoff,
// and its native API is not dialled until it is found. Found, it is looked
// for again a minute on, even though a legacy answer says ten seconds.
//
// verifies SPEC §13
func TestASatelliteNotFoundAtStartIsSoughtUntilItIs(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{foundInventory().Satellites[0]}}
	loc := newScriptedLocator()
	dialer := newDialer()
	clk := newClock()
	r := newRig(t, inv, func(d *deps) {
		d.Locator, d.Native, d.Clock, d.Timers = loc, dialer, clk, clk
	})

	loc.results <- located{err: mdns.ErrNoAnswer}
	r.logs.await(t, "satellite not found on the network")
	clk.awaitWait(t, discoverRetryMin)
	loc.results <- located{err: mdns.ErrNoAnswer}
	clk.advance(discoverRetryMin)
	clk.awaitWait(t, 2*discoverRetryMin)
	if got := dialer.dialed(); len(got) != 0 {
		t.Fatalf("dialled %v before the satellite was found", got)
	}

	loc.results <- located{at: netip.MustParseAddrPort("10.0.0.31:6053"), ttl: 10 * time.Second}
	clk.advance(2 * discoverRetryMin)
	r.logs.await(t, "satellite found")
	await(t, "the native API dialled", func() bool { return len(dialer.dialed()) == 1 })
	if got := dialer.dialed()[0]; got != "10.0.0.31:6053 "+goodPSK {
		t.Errorf("dialled %s, want where it was found", got)
	}
	clk.awaitWait(t, rediscoverMin)
	if n := loc.asked(); n != 3 {
		t.Errorf("looked %d times, want 3", n)
	}
}

// Behind Docker Desktop's source NAT a satellite's audio arrives from the
// gateway, whatever its native API's found address is.
func TestAnAudioSourceIPHoldsForASatelliteThatIsFound(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{
		{Name: officeNode, Address: officeNode + ".local:6053", AudioSourceIP: "192.168.65.1", PSK: goodPSK},
		{Name: kitchenNode, Address: kitchenIP + ":6053", PSK: goodPSK},
	}}
	a, err := newAddresses(inv)
	if err != nil {
		t.Fatal(err)
	}
	office, kitchen := &inv.Satellites[0], &inv.Satellites[1]
	if _, err := a.dialAddress(office); err == nil {
		t.Error("the office was dialled before it was found")
	}
	a.record(office, netip.MustParseAddrPort(officeIP+":6053"))

	if got := a.at(netip.MustParseAddr("192.168.65.1")); got != office {
		t.Errorf("audio from the gateway is %v, want the office", got)
	}
	if got := a.at(netip.MustParseAddr(officeIP)); got != nil {
		t.Errorf("audio from the office's own address is %s, want nobody's", got.Name)
	}
	if got, _ := a.dialAddress(office); got != officeIP+":6053" {
		t.Errorf("the office is dialled at %s", got)
	}
	if got, _ := a.dialAddress(kitchen); got != kitchenIP+":6053" {
		t.Errorf("the kitchen is dialled at %s", got)
	}
	if a.dialled(officeIP+":6053") != officeNode || a.dialled(kitchenIP+":6053") != kitchenNode {
		t.Error("a dialled address does not name its satellite")
	}
}

// Nothing in the daemon finds a satellite unless it is given a locator, so
// an inventory that needs one is refused at startup, naming the satellite.
func TestAnInventoryThatNeedsDiscoveryNeedsALocator(t *testing.T) {
	_, err := newDaemon(foundInventory(), deps{Log: nil})
	if err == nil || !strings.Contains(err.Error(), kitchenNode) {
		t.Errorf("err = %v, want the satellite named", err)
	}
}

// scriptedLocator answers each look with what the test queued.
type scriptedLocator struct {
	results chan located
	looks   chan struct{}
}

type located struct {
	at  netip.AddrPort
	ttl time.Duration
	err error
}

func newScriptedLocator() *scriptedLocator {
	return &scriptedLocator{results: make(chan located, 4), looks: make(chan struct{}, 16)}
}

func (l *scriptedLocator) Locate(ctx context.Context, _ *config.Satellite) (netip.AddrPort, time.Duration, error) {
	l.looks <- struct{}{}
	select {
	case r := <-l.results:
		return r.at, r.ttl, r.err
	case <-ctx.Done():
		return netip.AddrPort{}, 0, ctx.Err()
	}
}

func (l *scriptedLocator) asked() int { return len(l.looks) }
