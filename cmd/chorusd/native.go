package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/teaganglenn/chorus/internal/config"
	"github.com/teaganglenn/chorus/internal/esphome"
	"github.com/teaganglenn/chorus/internal/pb"
)

// The native API is control only (SPEC §3.1) and nothing on the host drives
// it yet. It is held open anyway: a stock api component reboots the board
// when no client has connected within its reboot_timeout, link and all.
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
			err = hold(ctx, conn)
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
// ends. Unanswered pings are how the device decides a client is gone.
func hold(ctx context.Context, c nativeConn) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// Recv is not cancellable; closing the connection is.
		<-ctx.Done()
		_ = c.Close()
	}()
	for {
		m, err := c.Recv()
		if err != nil {
			if esphome.IsUnknownMessage(err) {
				continue
			}
			return err
		}
		switch m.(type) {
		case *pb.PingRequest:
			if err := c.Send(&pb.PingResponse{}); err != nil {
				return fmt.Errorf("answer ping: %w", err)
			}
		case *pb.DisconnectRequest:
			_ = c.Send(&pb.DisconnectResponse{})
			return errDeviceDisconnected
		}
	}
}
