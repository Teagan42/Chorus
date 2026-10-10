package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/esphome"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/pb"
	"github.com/teagan42/chorus/internal/session"
)

// The native API is control only (SPEC §3.1). The host reads the room's
// presence sensor over it and drives the LED ring. It is held open regardless:
// a stock api component reboots the board when no client has connected
// within its reboot_timeout, link and all.
const (
	nativeDialTimeout = 15 * time.Second
	nativeRetryMin    = 5 * time.Second
	nativeRetryMax    = 60 * time.Second
)

// nativeDialer opens the control connection to one satellite with its PSK.
type nativeDialer interface {
	Dial(ctx context.Context, address, psk string) (nativeConn, error)
}

// nativeConn is the held connection: what the device said it is, and the
// message loop that keeps it alive.
type nativeConn interface {
	Describe() string
	Send(proto.Message) error
	Recv() (proto.Message, error)
	Close() error
}

// errDeviceDisconnected is the device ending the connection on purpose.
var errDeviceDisconnected = errors.New("device requested disconnect")

// keep holds one satellite's native API open for the life of the daemon,
// redialing with backoff. The audio link is independent of it, so a device
// that is offline is a retry here and never a failed start.
func (d *daemon) keep(ctx context.Context, sat *config.Satellite) {
	log := d.Log.With("satellite", sat.Name, "address", sat.Address)
	p := &presence{journal: d.journal, conv: listen.DeviceConversation(sat.Name), log: log}
	l := &led{ring: d.rings[sat.Name], log: log}
	backoff := nativeRetryMin
	for {
		conn, err := within(ctx, d.Timers, nativeDialTimeout, func(ctx context.Context) (nativeConn, error) {
			return d.Native.Dial(ctx, sat.Address, sat.PSK)
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Warn("native api unreachable", "err", err, "retry_in", backoff)
		} else {
			log.Info("native api connected", "device", conn.Describe())
			backoff = nativeRetryMin
			err = hold(ctx, conn, p, l)
			p.dropped(ctx)
			l.dropped()
			if ctx.Err() != nil {
				return
			}
			log.Warn("native api dropped", "err", err, "retry_in", backoff)
		}
		select {
		case <-d.Timers.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(2*backoff, nativeRetryMax)
	}
}

// hold answers the device's keepalive until the connection fails or ctx
// ends. Unanswered pings are how the device decides a client is gone. It
// lists the device's entities, drives the LED ring when it has one Chorus
// can, and subscribes to states only when one is the room's presence sensor.
func hold(ctx context.Context, c nativeConn, p *presence, l *led) error {
	// Waited on last, after the cancel below has ended the ring's driver.
	var driving sync.WaitGroup
	defer driving.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// Recv is not cancellable; closing the connection is.
		<-ctx.Done()
		_ = c.Close()
	}()
	if err := c.Send(&pb.ListEntitiesRequest{}); err != nil {
		return fmt.Errorf("list entities: %w", err)
	}
	for {
		m, err := c.Recv()
		if err != nil {
			if esphome.IsUnknownMessage(err) {
				continue
			}
			return err
		}
		switch m := m.(type) {
		case *pb.PingRequest:
			if err := c.Send(&pb.PingResponse{}); err != nil {
				return fmt.Errorf("answer ping: %w", err)
			}
		case *pb.DisconnectRequest:
			_ = c.Send(&pb.DisconnectResponse{})
			return errDeviceDisconnected
		case *pb.ListEntitiesBinarySensorResponse:
			p.entity(m)
		case *pb.ListEntitiesLightResponse:
			l.entity(m)
		case *pb.ListEntitiesDoneResponse:
			if l.listed() {
				driving.Go(func() { l.drive(ctx, c) })
			}
			if !p.listed() {
				continue
			}
			if err := c.Send(&pb.SubscribeStatesRequest{}); err != nil {
				return fmt.Errorf("subscribe states: %w", err)
			}
		case *pb.BinarySensorStateResponse:
			p.state(ctx, m)
		}
	}
}

// presence journals one satellite's presence sensor to the device's log:
// the Satellite1's mmWave "Room Presence" (ADR-0050). It lives as long as
// keep does, so a redial does not record again a state already recorded.
type presence struct {
	journal *journal.Journal
	conv    string
	log     *slog.Logger

	// Per connection: the sensor the device listed, if any.
	found  bool
	key    uint32
	sensor string

	// last is the state most recently journalled, across connections.
	last string
}

// entity takes the first binary sensor whose device class says it senses
// people. ESPHome names it by object id; the class is what is stable.
func (p *presence) entity(e *pb.ListEntitiesBinarySensorResponse) {
	if p.found {
		return
	}
	switch e.GetDeviceClass() {
	case "occupancy", "presence":
		p.found, p.key, p.sensor = true, e.GetKey(), e.GetObjectId()
	}
}

// listed reports, once the device has listed everything, whether it has a
// presence sensor. A Voice PE has none, and that is no fault.
func (p *presence) listed() bool {
	if p.found {
		p.log.Info("presence sensor found", "sensor", p.sensor)
	} else {
		p.log.Info("no presence sensor, so presence is not journalled")
	}
	return p.found
}

// state journals the presence sensor's report when it changes.
func (p *presence) state(ctx context.Context, m *pb.BinarySensorStateResponse) {
	if !p.found || m.GetKey() != p.key {
		return
	}
	st := "absent"
	switch {
	case m.GetMissingState():
		st = "unknown"
	case m.GetState():
		st = "present"
	}
	p.record(ctx, st)
}

// dropped ends the connection's sensor. Someone seen present can no longer
// be vouched for, so that span is closed as unknown rather than left open.
func (p *presence) dropped(ctx context.Context) {
	if p.found && p.last == "present" {
		p.record(ctx, "unknown")
	}
	p.found, p.key, p.sensor = false, 0, ""
}

func (p *presence) record(ctx context.Context, st string) {
	if st == p.last {
		return
	}
	// WithoutCancel: a shutdown still closes the span it ends.
	_, err := p.journal.Append(context.WithoutCancel(ctx), p.conv, journal.Record{
		Kind: journal.KindPresenceChanged, Fields: map[string]string{"state": st, "sensor": p.sensor},
	})
	if err != nil {
		p.log.Warn("journal presence", "state", st, "err", err)
		return
	}
	p.last = st
}

// ring is one satellite's LED ring as its sessions last showed it, kept for
// whichever native API connection drives it (ADR-0056).
type ring struct {
	mu      sync.Mutex
	state   session.RingState
	changed chan struct{}
}

func newRing() *ring {
	return &ring{state: session.RingOff, changed: make(chan struct{}, 1)}
}

// Show keeps the newest state and wakes the driver. It never blocks: a
// session calls it holding its own lock.
func (r *ring) Show(st session.RingState) {
	r.mu.Lock()
	r.state = st
	r.mu.Unlock()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *ring) current() session.RingState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// led drives one satellite's ring over the native API, when the device lists
// a light with an effect for each state. Any other ring is the firmware's
// own to animate, and is left alone.
type led struct {
	ring *ring
	log  *slog.Logger

	// Per connection: the ring the device listed, if any.
	found bool
	key   uint32
	light string
}

// ringEffect is the effect esphome/satellite1.yaml names a state by.
func ringEffect(st session.RingState) string {
	switch st {
	case session.RingListening:
		return "Listening"
	case session.RingThinking:
		return "Thinking"
	case session.RingSpeaking:
		return "Speaking"
	}
	return ""
}

// entity takes the first light that has every state's effect.
func (l *led) entity(e *pb.ListEntitiesLightResponse) {
	if l.found {
		return
	}
	for _, st := range []session.RingState{session.RingListening, session.RingThinking, session.RingSpeaking} {
		if !slices.Contains(e.GetEffects(), ringEffect(st)) {
			return
		}
	}
	l.found, l.key, l.light = true, e.GetKey(), e.GetObjectId()
}

// listed reports, once the device has listed everything, whether it has a
// ring to drive.
func (l *led) listed() bool {
	if l.found {
		l.log.Info("LED ring found", "light", l.light)
	} else {
		l.log.Info("no LED ring with Chorus's effects, so the ring is left to the firmware")
	}
	return l.found
}

// drive shows the ring's state now, then each change, until ctx ends. A
// state passed through before it could be sent is skipped: the ring shows
// the newest. A failed send ends it, and the read loop sees the drop.
func (l *led) drive(ctx context.Context, c nativeConn) {
	var shown session.RingState
	for {
		if st := l.ring.current(); st != shown {
			if err := c.Send(ringCommand(l.key, st)); err != nil {
				l.log.Warn("show the LED ring", "state", st, "err", err)
				return
			}
			shown = st
		}
		select {
		case <-l.ring.changed:
		case <-ctx.Done():
			return
		}
	}
}

// dropped forgets the connection's ring.
func (l *led) dropped() { l.found, l.key, l.light = false, 0, "" }

// ringCommand sets the ring to a state: its effect, or off.
func ringCommand(key uint32, st session.RingState) *pb.LightCommandRequest {
	if st == session.RingOff {
		return &pb.LightCommandRequest{Key: key, HasState: true, State: false}
	}
	return &pb.LightCommandRequest{Key: key, HasState: true, State: true, HasEffect: true, Effect: ringEffect(st)}
}
