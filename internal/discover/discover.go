// Package discover finds a satellite whose inventory address is not an IP
// literal: a .local name, or none, over mDNS, and any other hostname with
// the system resolver (SPEC §13, ADR-0103).
package discover

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/mdns"
)

// Network finds satellites on the household's network.
type Network struct {
	MDNS *mdns.Resolver
	// DNS resolves a hostname that is not under .local.
	DNS func(ctx context.Context, host string) ([]netip.Addr, error)
}

// New asks the link over mDNS and the system resolver for the rest.
func New(timers mdns.Timers) *Network {
	return &Network{
		MDNS: &mdns.Resolver{Timers: timers},
		DNS: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		},
	}
}

// Locate is where sat's native API is now, and for how long the answer
// holds; zero is unsaid.
func (n *Network) Locate(ctx context.Context, sat *config.Satellite) (netip.AddrPort, time.Duration, error) {
	if sat.Address == "" {
		f, err := n.MDNS.Node(ctx, sat.Name)
		if err != nil {
			return netip.AddrPort{}, 0, fmt.Errorf("discover %s: %w", sat.Name, err)
		}
		return netip.AddrPortFrom(f.Addr, f.Port), f.TTL, nil
	}
	host, port, err := HostPort(sat.Address)
	if err != nil {
		return netip.AddrPort{}, 0, err
	}
	if Local(host) {
		f, err := n.MDNS.Host(ctx, host)
		if err != nil {
			return netip.AddrPort{}, 0, fmt.Errorf("resolve %s: %w", host, err)
		}
		return netip.AddrPortFrom(f.Addr, port), f.TTL, nil
	}
	ips, err := n.DNS(ctx, host)
	if err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, ip := range ips {
		if ip = ip.Unmap(); ip.Is4() {
			return netip.AddrPortFrom(ip, port), 0, nil
		}
	}
	return netip.AddrPort{}, 0, fmt.Errorf("resolve %s: no IPv4 address", host)
}

// Local is a name only mDNS answers for.
func Local(host string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ".local")
}

// HostPort splits an inventory address.
func HostPort(address string) (string, uint16, error) {
	host, p, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, fmt.Errorf("address %q: %w", address, err)
	}
	port, err := strconv.ParseUint(p, 10, 16)
	if err != nil {
		return "", 0, fmt.Errorf("address %q: port: %w", address, err)
	}
	return host, uint16(port), nil
}

// Literal is the inventory address's IP, or none for a hostname or no
// address: a satellite that is found.
func Literal(sat *config.Satellite) (netip.Addr, error) {
	if sat.Address == "" {
		return netip.Addr{}, nil
	}
	host, _, err := HostPort(sat.Address)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("satellite %s: %w", sat.Name, err)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap(), nil
	}
	return netip.Addr{}, nil
}

// Described is what the inventory says about where sat is, for a log line.
func Described(sat *config.Satellite) string {
	if sat.Address == "" {
		return sat.Name + "." + mdns.Service + ".local"
	}
	return sat.Address
}
