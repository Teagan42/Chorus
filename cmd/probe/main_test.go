package main

import (
	"net/netip"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/discover"
	"github.com/teagan42/chorus/internal/mdns"
	"github.com/teagan42/chorus/internal/mdns/mdnstest"
)

// probe dials a satellite where chorusd would: an IP literal as given, a
// .local name or no address wherever mDNS finds it.
//
// verifies SPEC §13
func TestProbeDialsASatelliteWhereChorusdWouldFindIt(t *testing.T) {
	link := mdnstest.Listen(t)
	link.Advertise(mdnstest.Node{Name: "office-voice-pe-5c7e91", Friendly: "Office Voice PE", Addr: netip.MustParseAddr("192.168.1.42")})
	link.Advertise(mdnstest.Node{Name: "kitchen-satellite1-a1b2c3", Friendly: "Kitchen Satellite", Addr: netip.MustParseAddr("192.168.1.31")})
	net := &discover.Network{MDNS: &mdns.Resolver{To: link.Addr(), Timers: never{}}}

	for _, c := range []struct {
		sat  config.Satellite
		want string
	}{
		{config.Satellite{Name: "garage-satellite1-0d4f22", Address: "192.168.1.77:6053"}, "192.168.1.77:6053"},
		{config.Satellite{Name: "office-voice-pe-5c7e91", Address: "office-voice-pe-5c7e91.local:6053"}, "192.168.1.42:6053"},
		{config.Satellite{Name: "kitchen-satellite1-a1b2c3"}, "192.168.1.31:6053"},
	} {
		got, err := where(t.Context(), &c.sat, net)
		if err != nil || got != c.want {
			t.Errorf("%s: dialled %q (%v), want %s", c.sat.Name, got, err, c.want)
		}
	}
}

type never struct{}

func (never) After(time.Duration) <-chan time.Time { return nil }
