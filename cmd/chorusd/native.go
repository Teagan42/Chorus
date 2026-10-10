package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/esphome"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/pb"
)

// The native API is control only (SPEC §3.1). The host reads the room's
// presence sensor over it and drives nothing yet. It is held open regardless:
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
			err = hold(ctx, conn, p)
			p.dropped(ctx)
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
// lists the device's entities, and subscribes to states only when one of
// them is the room's presence sensor.
func hold(ctx context.Context, c nativeConn, p *presence) error {
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
		case *pb.ListEntitiesDoneResponse:
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
