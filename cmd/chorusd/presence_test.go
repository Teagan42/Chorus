package main

import (
	"slices"
	"testing"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/pb"
)

// The Satellite1's mmWave radar registers "Room Presence" with the occupancy
// class (FutureProofHomes/Satellite1-ESPHome satellite1_radar). The keys are
// the device's; these are made up.
const (
	presenceKey = 0x5a17c0de
	muteKey     = 0x0b5e55ed
)

func satellite1Entities() []*pb.ListEntitiesBinarySensorResponse {
	return []*pb.ListEntitiesBinarySensorResponse{
		{ObjectId: "xmos_ready", Key: muteKey, Name: "XMOS Ready"},
		{ObjectId: "room_presence", Key: presenceKey, Name: "Room Presence", DeviceClass: "occupancy"},
	}
}

// connect has the kitchen's native API answer one dial and returns it.
func connect(t *testing.T, dialer *fakeDialer) *fakeNative {
	t.Helper()
	conn := newNative()
	dialer.results <- dialResult{conn: conn}
	return conn
}

// list answers the daemon's entity list as the device does: each entity,
// then done.
func list(t *testing.T, conn *fakeNative, es []*pb.ListEntitiesBinarySensorResponse) {
	t.Helper()
	sent[*pb.ListEntitiesRequest](t, conn, "the entity list")
	for _, e := range es {
		conn.in <- e
	}
	conn.in <- &pb.ListEntitiesDoneResponse{}
}

func presenceIn(r *rig, sat string) []journal.Event {
	var out []journal.Event
	for _, e := range r.store.ofKind(journal.KindPresenceChanged) {
		if e.ConversationID == listen.DeviceConversation(sat) {
			out = append(out, e)
		}
	}
	return out
}

// The room's presence sensor is read over the native API and each change is
// journalled to the satellite's own log, where Browse draws it: someone
// walks into the kitchen at breakfast, the radar repeats itself while they
// stay, and they leave. The XMOS's own binary sensor is not presence.
//
// verifies SPEC §3.3.1
func TestTheRoomsPresenceIsJournalledToTheDeviceLog(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[0]}}
	dialer, clk := newDialer(), newClock()
	r := newRig(t, inv, func(d *deps) { d.Native, d.Clock, d.Timers = dialer, clk, clk })

	conn := connect(t, dialer)
	list(t, conn, satellite1Entities())
	sent[*pb.SubscribeStatesRequest](t, conn, "the state subscription")

	// The device sends every entity's state on subscribing, presence among them.
	conn.in <- &pb.BinarySensorStateResponse{Key: muteKey, State: true}
	conn.in <- &pb.BinarySensorStateResponse{Key: presenceKey, State: false}
	conn.in <- &pb.BinarySensorStateResponse{Key: presenceKey, State: true}
	conn.in <- &pb.BinarySensorStateResponse{Key: presenceKey, State: true}
	conn.in <- &pb.BinarySensorStateResponse{Key: presenceKey, State: false}
	r.store.awaitKind(t, journal.KindPresenceChanged, 3)
	// A ping after the last report: answered means every report before it was read.
	conn.in <- &pb.PingRequest{}
	sent[*pb.PingResponse](t, conn, "the ping's answer")

	var states []string
	for _, e := range presenceIn(r, "kitchen") {
		states = append(states, e.Fields["state"])
		if e.Fields["sensor"] != "room_presence" {
			t.Errorf("presence from sensor %q, want room_presence", e.Fields["sensor"])
		}
	}
	if want := []string{"absent", "present", "absent"}; !slices.Equal(states, want) {
		t.Errorf("journalled %v, want %v: once per change, nothing from the XMOS sensor", states, want)
	}
}

// A dropped connection closes a presence span it can no longer vouch for,
// as unknown, and the redial's first report reopens it: Browse never draws
// someone in the room across a gap nothing was watching.
//
// verifies SPEC §3.3.1
func TestADroppedNativeAPILeavesPresenceUnknown(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[0]}}
	dialer, clk := newDialer(), newClock()
	r := newRig(t, inv, func(d *deps) { d.Native, d.Clock, d.Timers = dialer, clk, clk })

	conn := connect(t, dialer)
	list(t, conn, satellite1Entities())
	sent[*pb.SubscribeStatesRequest](t, conn, "the state subscription")
	conn.in <- &pb.BinarySensorStateResponse{Key: presenceKey, State: true}
	r.store.awaitKind(t, journal.KindPresenceChanged, 1)

	_ = conn.Close()
	lost := r.store.awaitKind(t, journal.KindPresenceChanged, 2)
	if lost.Fields["state"] != "unknown" {
		t.Errorf("after the drop presence is %q, want unknown", lost.Fields["state"])
	}

	again := connect(t, dialer)
	clk.awaitWait(t, nativeRetryMin)
	clk.advance(nativeRetryMin)
	list(t, again, satellite1Entities())
	sent[*pb.SubscribeStatesRequest](t, again, "the state subscription")
	// Before the radar has a reading: unknown, which is already recorded.
	again.in <- &pb.BinarySensorStateResponse{Key: presenceKey, MissingState: true}
	again.in <- &pb.BinarySensorStateResponse{Key: presenceKey, State: true}
	back := r.store.awaitKind(t, journal.KindPresenceChanged, 3)
	if back.Fields["state"] != "present" {
		t.Errorf("after the redial presence is %q, want present", back.Fields["state"])
	}
	if n := len(presenceIn(r, "kitchen")); n != 3 {
		t.Errorf("%d presence events, want present, unknown, present", n)
	}
}

// A Voice PE has no radar: the daemon lists its entities, finds no presence
// sensor, and does not subscribe to a stream of states it has no use for.
//
// verifies SPEC §3.3.1
func TestASatelliteWithNoRadarIsNotSubscribed(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[1]}}
	dialer, clk := newDialer(), newClock()
	r := newRig(t, inv, func(d *deps) { d.Native, d.Clock, d.Timers = dialer, clk, clk })

	conn := connect(t, dialer)
	list(t, conn, []*pb.ListEntitiesBinarySensorResponse{{ObjectId: "mute_switch", Key: muteKey, Name: "Mute"}})
	r.logs.await(t, "no presence sensor")
	conn.in <- &pb.PingRequest{}
	sent[*pb.PingResponse](t, conn, "the ping's answer, and no subscription before it")

	_ = conn.Close()
	r.logs.await(t, "native api dropped")
	if n := len(r.store.ofKind(journal.KindPresenceChanged)); n != 0 {
		t.Errorf("%d presence events from a satellite with no presence sensor", n)
	}
}
