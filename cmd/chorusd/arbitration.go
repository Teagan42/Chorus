package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/teagan42/chorus/internal/listen"
)

// wakeArbitrationEnv is how long, in milliseconds, a wake waits for the
// same wake from the house's other satellites (ADR-0072). 0 turns it off.
const wakeArbitrationEnv = "CHORUS_WAKE_ARBITRATION_MS"

// wakeArbitration is the arbitration window, listen.DefaultArbitration
// when unset and zero when turned off.
func (c Config) wakeArbitration() (time.Duration, error) {
	if c.WakeArbitrationMS == "" {
		return listen.DefaultArbitration, nil
	}
	ms, err := strconv.ParseUint(c.WakeArbitrationMS, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a whole number of milliseconds (0 turns arbitration off)", wakeArbitrationEnv, c.WakeArbitrationMS)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// arbitrate builds the house's one arbiter, or none when the window is
// zero. Every link's listener enters its wakes in it, and every link's
// coming and going is what it waits on.
func (d *daemon) arbitrate(ctx context.Context) error {
	if d.WakeArbitration <= 0 {
		if len(d.inv.Satellites) > 1 {
			d.Log.Warn("wake arbitration is off: " + wakeArbitrationEnv + "=0, so every satellite that hears a wake answers it, and two in earshot of one person both do")
		}
		return nil
	}
	names := make([]string, len(d.inv.Satellites))
	for i, sat := range d.inv.Satellites {
		names[i] = sat.Name
	}
	arb, err := listen.NewArbiter(ctx, listen.ArbiterConfig{
		Window: d.WakeArbitration, Clock: d.Clock, Timers: d.Timers, Satellites: names,
	})
	if err != nil {
		return err
	}
	d.arbiter = arb
	return nil
}

// contender is what a link's listener enters its wakes in: the arbiter, or
// nil, never a nil *Arbiter that would read as one.
func (d *daemon) contender() listen.Contender {
	if d.arbiter == nil {
		return nil
	}
	return d.arbiter
}

// present tells the arbiter a satellite's link came up or went.
func (d *daemon) present(name string, up bool) {
	if d.arbiter != nil {
		d.arbiter.Present(name, up)
	}
}
