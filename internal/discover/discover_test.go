package discover

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/mdns"
	"github.com/teagan42/chorus/internal/mdns/mdnstest"
)

var (
	kitchenAt = netip.MustParseAddr("192.168.1.31")
	officeAt  = netip.MustParseAddr("192.168.1.42")
	garageAt  = netip.MustParseAddr("192.168.1.77")
)

// The household's network: the kitchen and office satellites answer mDNS,
// and the router's DNS knows the garage's by the name its lease gave it.
func household(t *testing.T) *Network {
	t.Helper()
	link := mdnstest.Listen(t)
	link.Advertise(mdnstest.Node{Name: "kitchen-satellite1-a1b2c3", Friendly: "Kitchen Satellite", Addr: kitchenAt})
	link.Advertise(mdnstest.Node{Name: "office-voice-pe-5c7e91", Friendly: "Office Voice PE", Addr: officeAt})
	return &Network{
		MDNS: &mdns.Resolver{To: link.Addr(), Timers: never{}},
		DNS: func(_ context.Context, host string) ([]netip.Addr, error) {
			if host != "garage-sat.lan" {
				t.Errorf("the router was asked for %s", host)
			}
			return []netip.Addr{netip.MustParseAddr("fd00::77"), netip.MustParseAddr("::ffff:192.168.1.77")}, nil
		},
	}
}

// Each way an inventory can say where a satellite is, found where it is.
//
// verifies SPEC §13
func TestLocateFindsEachSatelliteTheWayItsInventoryEntrySays(t *testing.T) {
	n := household(t)
	cases := []struct {
		sat  config.Satellite
		want netip.AddrPort
		ttl  time.Duration
	}{
		{config.Satellite{Name: "kitchen-satellite1-a1b2c3"}, netip.AddrPortFrom(kitchenAt, 6053), 10 * time.Second},
		{config.Satellite{Name: "office-voice-pe-5c7e91", Address: "office-voice-pe-5c7e91.local:6053"}, netip.AddrPortFrom(officeAt, 6053), 10 * time.Second},
		{config.Satellite{Name: "garage-satellite1-0d4f22", Address: "garage-sat.lan:6053"}, netip.AddrPortFrom(garageAt, 6053), 0},
	}
	for _, c := range cases {
		got, ttl, err := n.Locate(t.Context(), &c.sat)
		if err != nil {
			t.Errorf("%s: %v", c.sat.Name, err)
			continue
		}
		if got != c.want || ttl != c.ttl {
			t.Errorf("%s: found %v for %v, want %v for %v", c.sat.Name, got, ttl, c.want, c.ttl)
		}
	}
}

func TestAHostnameWithNoIPv4AddressIsNotFound(t *testing.T) {
	n := &Network{DNS: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("fd00::77")}, nil
	}}
	_, _, err := n.Locate(t.Context(), &config.Satellite{Name: "garage-satellite1-0d4f22", Address: "garage-sat.lan:6053"})
	if err == nil || !strings.Contains(err.Error(), "garage-sat.lan") {
		t.Errorf("err = %v, want the host named", err)
	}
}

func TestLiteralIsTheInventorysIPOrNone(t *testing.T) {
	for addr, want := range map[string]string{
		"192.168.1.31:6053":                 "192.168.1.31",
		"[::ffff:192.168.1.31]:6053":        "192.168.1.31",
		"office-voice-pe-5c7e91.local:6053": "invalid IP",
		"":                                  "invalid IP",
	} {
		got, err := Literal(&config.Satellite{Name: "kitchen-satellite1-a1b2c3", Address: addr})
		if err != nil || got.String() != want {
			t.Errorf("Literal(%q) = %v, %v; want %s", addr, got, err, want)
		}
	}
	if got := Described(&config.Satellite{Name: "kitchen-satellite1-a1b2c3"}); got != "kitchen-satellite1-a1b2c3._esphomelib._tcp.local" {
		t.Errorf("Described = %q", got)
	}
}

type never struct{}

func (never) After(time.Duration) <-chan time.Time { return nil }
