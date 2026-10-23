package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/discover"
)

// A satellite the inventory does not pin to an IP literal is found on the
// network, and found again on a timer or the moment it may have moved
// (SPEC §13, ADR-0103).
const (
	discoverRetryMin = 5 * time.Second
	discoverRetryMax = 60 * time.Second
	// A legacy unicast answer may say ten seconds (RFC 6762 §6.7); asking
	// that often buys nothing a nudge does not.
	rediscoverMin = time.Minute
	rediscoverMax = 5 * time.Minute
)

// locator finds where a satellite's native API is now, and for how long
// the answer holds; zero is unsaid.
type locator interface {
	Locate(ctx context.Context, sat *config.Satellite) (netip.AddrPort, time.Duration, error)
}

// addresses is which satellite an audio link's source address belongs to,
// and where each satellite's native API is dialled.
type addresses struct {
	sats []*config.Satellite
	// fixed is each audio source the inventory gives as an IP literal.
	fixed map[netip.Addr]*config.Satellite
	// sought is each satellite that is found, with its nudge: a wake for
	// its discovery loop, which coalesces.
	sought map[string]chan struct{}

	mu sync.Mutex
	// found is where discovery last found each sought satellite; known is
	// closed when it first is.
	found map[string]netip.AddrPort
	known map[string]chan struct{}
}

// newAddresses refuses an inventory whose fixed audio sources two satellites
// share: their links could not be told apart.
func newAddresses(inv *config.Config) (*addresses, error) {
	a := &addresses{
		fixed:  map[netip.Addr]*config.Satellite{},
		sought: map[string]chan struct{}{},
		found:  map[string]netip.AddrPort{},
		known:  map[string]chan struct{}{},
	}
	for i := range inv.Satellites {
		sat := &inv.Satellites[i]
		a.sats = append(a.sats, sat)
		literal, err := discover.Literal(sat)
		if err != nil {
			return nil, err
		}
		if !literal.IsValid() {
			a.sought[sat.Name] = make(chan struct{}, 1)
			a.known[sat.Name] = make(chan struct{})
		}
		source := literal
		if sat.AudioSourceIP != "" {
			ip, err := netip.ParseAddr(sat.AudioSourceIP)
			if err != nil {
				return nil, fmt.Errorf("satellite %s: audio source %q is not an IP literal", sat.Name, sat.AudioSourceIP)
			}
			source = ip.Unmap()
		}
		if !source.IsValid() {
			continue
		}
		if other, dup := a.fixed[source]; dup {
			return nil, fmt.Errorf("satellites %s and %s share the host %s, so their audio links cannot be told apart", other.Name, sat.Name, source)
		}
		a.fixed[source] = sat
	}
	return a, nil
}

// soughtSatellites are the satellites discovery finds, in inventory order.
func (a *addresses) soughtSatellites() []*config.Satellite {
	var out []*config.Satellite
	for _, sat := range a.sats {
		if _, ok := a.sought[sat.Name]; ok {
			out = append(out, sat)
		}
	}
	return out
}

// at is the satellite whose audio comes from ip, or nil when none does or
// more than one might.
func (a *addresses) at(ip netip.Addr) *config.Satellite {
	ip = ip.Unmap()
	a.mu.Lock()
	defer a.mu.Unlock()
	owners := a.ownersLocked(ip)
	if len(owners) != 1 {
		return nil
	}
	return owners[0]
}

// ownersLocked is every satellite whose audio source is ip.
func (a *addresses) ownersLocked(ip netip.Addr) []*config.Satellite {
	var owners []*config.Satellite
	if sat := a.fixed[ip]; sat != nil {
		owners = append(owners, sat)
	}
	for _, sat := range a.sats {
		if sat.AudioSourceIP != "" {
			continue
		}
		if ap, ok := a.found[sat.Name]; ok && ap.Addr() == ip {
			owners = append(owners, sat)
		}
	}
	return owners
}

// record keeps where sat was found, and names the satellites now sharing
// its audio source, which match nobody until one of them moves.
func (a *addresses) record(sat *config.Satellite, ap netip.AddrPort) (prev netip.AddrPort, sharing []string) {
	ap = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	a.mu.Lock()
	defer a.mu.Unlock()
	prev, seen := a.found[sat.Name]
	a.found[sat.Name] = ap
	if !seen {
		close(a.known[sat.Name])
	}
	if sat.AudioSourceIP != "" {
		return prev, nil
	}
	for _, other := range a.ownersLocked(ap.Addr()) {
		if other != sat {
			sharing = append(sharing, other.Name)
		}
	}
	return prev, sharing
}

// errNotFound is a sought satellite's native API before discovery has
// found it.
var errNotFound = errors.New("not found on the network yet")

// dialAddress is where sat's native API is now.
func (a *addresses) dialAddress(sat *config.Satellite) (string, error) {
	if _, sought := a.sought[sat.Name]; !sought {
		return sat.Address, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ap, ok := a.found[sat.Name]
	if !ok {
		return "", errNotFound
	}
	return ap.String(), nil
}

// wait returns once sat's native API has an address, or false when ctx
// ends first.
func (a *addresses) wait(ctx context.Context, sat *config.Satellite) bool {
	known, sought := a.known[sat.Name]
	if !sought {
		return true
	}
	select {
	case <-known:
		return true
	case <-ctx.Done():
		return false
	}
}

// dialled names the satellite whose native API is at address.
func (a *addresses) dialled(address string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, sat := range a.sats {
		if ap, ok := a.found[sat.Name]; ok && ap.String() == address {
			return sat.Name
		}
		if _, sought := a.sought[sat.Name]; !sought && sat.Address == address {
			return sat.Name
		}
	}
	return ""
}

// nudge has sat looked for again now: its native API failed to dial.
func (a *addresses) nudge(sat *config.Satellite) {
	if n, ok := a.sought[sat.Name]; ok {
		select {
		case n <- struct{}{}:
		default:
		}
	}
}

// nudgeAll has every sought satellite looked for again: a link came from
// an address that matched nobody, or the wrong satellite.
func (a *addresses) nudgeAll() {
	for _, sat := range a.sats {
		a.nudge(sat)
	}
}

// seek finds sat until ctx ends: again before its answer lapses, at a
// nudge, and with backoff while nobody answers, keeping the last address
// found meanwhile.
func (d *daemon) seek(ctx context.Context, sat *config.Satellite) {
	log := d.Log.With("satellite", sat.Name, "address", discover.Described(sat))
	retry := discoverRetryMin
	for {
		ap, ttl, err := d.Locator.Locate(ctx, sat)
		if ctx.Err() != nil {
			return
		}
		var wait time.Duration
		if err != nil {
			log.Warn("satellite not found on the network", "err", err, "retry_in", retry)
			wait, retry = retry, min(2*retry, discoverRetryMax)
		} else {
			retry = discoverRetryMin
			wait = min(max(ttl, rediscoverMin), rediscoverMax)
			prev, sharing := d.addrs.record(sat, ap)
			switch {
			case !prev.IsValid():
				log.Info("satellite found", "at", ap.String())
			case prev != ap:
				log.Info("satellite moved", "from", prev.String(), "to", ap.String())
			}
			if len(sharing) > 0 {
				log.Warn("satellites share an address, so links from it are refused until one moves",
					"at", ap.Addr().String(), "sharing", sharing)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-d.Timers.After(wait):
		case <-d.addrs.sought[sat.Name]:
		}
	}
}
