// Package mdns asks the local link where a satellite is: one-shot multicast
// DNS queries (RFC 6762 §5.1) for a .local host, or for an ESPHome node's
// _esphomelib._tcp instance (SPEC §13, ADR-0103).
package mdns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

// Service is what every ESPHome node advertises its native API under, with
// its node name as the instance.
const Service = "_esphomelib._tcp"

// Group is the link's mDNS address, where every query goes.
var Group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// ErrNoAnswer is a name nobody on the link answered for.
var ErrNoAnswer = errors.New("no mDNS answer")

// One-shot queries retransmit with the wait doubling (RFC 6762 §5.1), so an
// absent name costs seven seconds.
const (
	firstWait = time.Second
	attempts  = 3
)

// Timers is the clock's only use here: how long to wait for an answer.
type Timers interface {
	After(d time.Duration) <-chan time.Time
}

// Found is where a name answered, and for how long the answer holds.
type Found struct {
	Addr netip.Addr
	// Port is the instance's SRV port; a host lookup has none.
	Port uint16
	TTL  time.Duration
}

// Resolver asks from an ephemeral port, so responders answer it by unicast
// as a legacy query (RFC 6762 §6.7) and port 5353 stays free.
type Resolver struct {
	// Listen opens that socket; nil opens an ephemeral UDP port.
	Listen func() (net.PacketConn, error)
	// To is where queries go; nil is Group.
	To     net.Addr
	Timers Timers
}

// Host is the IPv4 address that answers to a .local name.
func (r *Resolver) Host(ctx context.Context, host string) (Found, error) {
	name, err := fqdn(host)
	if err != nil {
		return Found{}, err
	}
	return r.ask(ctx, name, dnsmessage.TypeA, func(m *message) (Found, bool) {
		return m.a(name)
	})
}

// Node is where the ESPHome node of that name serves its native API: its
// instance's SRV, and the address of the host the SRV names.
func (r *Resolver) Node(ctx context.Context, node string) (Found, error) {
	instance, err := fqdn(node + "." + Service + ".local")
	if err != nil {
		return Found{}, err
	}
	var target dnsmessage.Name
	var srv Found
	found, err := r.ask(ctx, instance, dnsmessage.TypeSRV, func(m *message) (Found, bool) {
		t, port, ttl, ok := m.srv(instance)
		if !ok {
			return Found{}, false
		}
		target, srv = t, Found{Port: port, TTL: ttl}
		// A responder usually sends the host's address alongside.
		a, ok := m.a(target)
		if !ok {
			return Found{}, true
		}
		return Found{Addr: a.Addr, Port: port, TTL: min(ttl, a.TTL)}, true
	})
	if err != nil || found.Addr.IsValid() {
		return found, err
	}
	a, err := r.ask(ctx, target, dnsmessage.TypeA, func(m *message) (Found, bool) {
		return m.a(target)
	})
	if err != nil {
		return Found{}, fmt.Errorf("%s names %s: %w", instance, target, err)
	}
	return Found{Addr: a.Addr, Port: srv.Port, TTL: min(srv.TTL, a.TTL)}, nil
}

// ask sends the question until a reply answers it or the attempts run out.
func (r *Resolver) ask(ctx context.Context, name dnsmessage.Name, typ dnsmessage.Type, answers func(*message) (Found, bool)) (Found, error) {
	query, err := pack(name, typ)
	if err != nil {
		return Found{}, err
	}
	conn, err := r.listen()
	if err != nil {
		return Found{}, fmt.Errorf("ask %s: %w", name, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	// Closing is the only way to unblock ReadFrom, so it ends the reader.
	defer func() { _ = conn.Close() }()
	defer cancel()
	replies := make(chan []byte)
	go read(ctx, conn, replies)
	wait := firstWait
	for range attempts {
		if _, err := conn.WriteTo(query, r.to()); err != nil {
			return Found{}, fmt.Errorf("ask %s: %w", name, err)
		}
		timeout := r.Timers.After(wait)
		for waiting := true; waiting; {
			select {
			case b := <-replies:
				m, ok := parse(b)
				if !ok {
					continue
				}
				if f, ok := answers(m); ok {
					return f, nil
				}
			case <-timeout:
				waiting = false
			case <-ctx.Done():
				return Found{}, ctx.Err()
			}
		}
		wait *= 2
	}
	return Found{}, fmt.Errorf("%s: %w", name, ErrNoAnswer)
}

// read hands each datagram over until the socket closes or ctx ends.
func read(ctx context.Context, conn net.PacketConn, replies chan<- []byte) {
	buf := make([]byte, 9000)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		select {
		case replies <- append([]byte(nil), buf[:n]...):
		case <-ctx.Done():
			return
		}
	}
}

func (r *Resolver) listen() (net.PacketConn, error) {
	if r.Listen != nil {
		return r.Listen()
	}
	c, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	// RFC 6762 §11: a responder may drop a link-local query that was routed.
	_ = ipv4.NewPacketConn(c).SetMulticastTTL(255)
	return c, nil
}

func (r *Resolver) to() net.Addr {
	if r.To != nil {
		return r.To
	}
	return Group
}

// fqdn is a name under .local, rooted, as a question carries it.
func fqdn(host string) (dnsmessage.Name, error) {
	host = strings.TrimSuffix(host, ".")
	if !strings.HasSuffix(strings.ToLower(host), ".local") {
		return dnsmessage.Name{}, fmt.Errorf("%q is not under .local", host)
	}
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return dnsmessage.Name{}, fmt.Errorf("%q is not a DNS name: %w", host, err)
	}
	return name, nil
}

// pack is one question with ID zero, as RFC 6762 §18.1 has a query sent.
func pack(name dnsmessage.Name, typ dnsmessage.Type) ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: name, Type: typ, Class: dnsmessage.ClassINET}); err != nil {
		return nil, fmt.Errorf("ask %s: %w", name, err)
	}
	return b.Finish()
}

// message is a reply's records, answers and additionals alike: a responder
// may put the host's address in either.
type message struct {
	records []dnsmessage.Resource
}

// parse reads a response, or reports a datagram that is not one.
func parse(b []byte) (*message, bool) {
	var p dnsmessage.Parser
	h, err := p.Start(b)
	if err != nil || !h.Response {
		return nil, false
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil, false
	}
	m := &message{}
	answers, err := p.AllAnswers()
	if err != nil {
		return nil, false
	}
	m.records = append(m.records, answers...)
	if err := p.SkipAllAuthorities(); err != nil {
		return m, true
	}
	if extra, err := p.AllAdditionals(); err == nil {
		m.records = append(m.records, extra...)
	}
	return m, true
}

// a is the first IPv4 address the reply gives name.
func (m *message) a(name dnsmessage.Name) (Found, bool) {
	for _, r := range m.records {
		if a, ok := r.Body.(*dnsmessage.AResource); ok && live(r.Header) && same(r.Header.Name, name) {
			return Found{Addr: netip.AddrFrom4(a.A), TTL: ttl(r.Header)}, true
		}
	}
	return Found{}, false
}

// srv is the host and port the reply gives instance.
func (m *message) srv(instance dnsmessage.Name) (dnsmessage.Name, uint16, time.Duration, bool) {
	for _, r := range m.records {
		if s, ok := r.Body.(*dnsmessage.SRVResource); ok && live(r.Header) && same(r.Header.Name, instance) {
			return s.Target, s.Port, ttl(r.Header), true
		}
	}
	return dnsmessage.Name{}, 0, 0, false
}

// live is an Internet record still held: TTL zero is a goodbye, the
// responder withdrawing it (RFC 6762 §10.1). The class's top bit is mDNS's
// cache-flush flag.
func live(h dnsmessage.ResourceHeader) bool {
	return h.Class&0x7fff == dnsmessage.ClassINET && h.TTL > 0
}

func same(a, b dnsmessage.Name) bool {
	return strings.EqualFold(a.String(), b.String())
}

func ttl(h dnsmessage.ResourceHeader) time.Duration {
	return time.Duration(h.TTL) * time.Second
}
