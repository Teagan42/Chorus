package mdns

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const patience = 5 * time.Second

// The office's Voice PE, as its own mdns component advertises it: the
// cache-flush bit set, two minutes' TTL.
var officeVoicePE = netip.MustParseAddr("192.168.1.42")

// verifies SPEC §13
func TestHostIsTheAddressTheLinkAnswersWith(t *testing.T) {
	link, timers := newLink(), newTimers()
	r := &Resolver{Listen: link.listen, Timers: timers}

	got := ask(t, func(ctx context.Context) (Found, error) {
		return r.Host(ctx, "office-voice-pe-5c7e91.local")
	}, func() {
		q := link.query(t)
		if q.Name.String() != "office-voice-pe-5c7e91.local." || q.Type != dnsmessage.TypeA || q.Class != dnsmessage.ClassINET {
			t.Errorf("asked %v", q)
		}
		// DNS names are case-blind; the device may answer in its own case.
		link.reply(t, reply(aRecord("Office-Voice-PE-5c7e91.local.", officeVoicePE, 120, true)))
	})
	if got.Addr != officeVoicePE || got.TTL != 2*time.Minute || got.Port != 0 {
		t.Errorf("found %+v, want %v for 2m", got, officeVoicePE)
	}
}

// A one-shot query is sent with ID zero and one question (RFC 6762 §18.1),
// from a port of its own so the answer comes back to it.
func TestAQueryIsOneQuestionWithIDZero(t *testing.T) {
	link, timers := newLink(), newTimers()
	r := &Resolver{Listen: link.listen, Timers: timers}
	ask(t, func(ctx context.Context) (Found, error) {
		return r.Host(ctx, "kitchen-satellite1-a1b2c3.local")
	}, func() {
		var p dnsmessage.Parser
		h, err := p.Start(<-link.sent)
		if err != nil {
			t.Fatalf("query does not parse: %v", err)
		}
		qs, _ := p.AllQuestions()
		if h.ID != 0 || h.Response || len(qs) != 1 {
			t.Errorf("header %+v with %d questions", h, len(qs))
		}
		if link.to() != Group.String() {
			t.Errorf("sent to %s, want %s", link.to(), Group)
		}
		link.reply(t, reply(aRecord("kitchen-satellite1-a1b2c3.local.", netip.MustParseAddr("192.168.1.31"), 10, false)))
	})
}

// A record with TTL zero is a responder withdrawing it (RFC 6762 §10.1),
// and another device's answer is not this one's.
func TestGoodbyesAndOtherDevicesAreNotAnswers(t *testing.T) {
	link, timers := newLink(), newTimers()
	r := &Resolver{Listen: link.listen, Timers: timers}

	got := ask(t, func(ctx context.Context) (Found, error) {
		return r.Host(ctx, "office-voice-pe-5c7e91.local")
	}, func() {
		link.query(t)
		link.reply(t, reply(aRecord("office-voice-pe-5c7e91.local.", netip.MustParseAddr("192.168.1.19"), 0, true)))
		link.reply(t, reply(aRecord("garage-satellite1-0d4f22.local.", netip.MustParseAddr("192.168.1.77"), 120, true)))
		link.reply(t, []byte("not a DNS message"))
		link.reply(t, reply(aRecord("office-voice-pe-5c7e91.local.", officeVoicePE, 120, true)))
	})
	if got.Addr != officeVoicePE {
		t.Errorf("found %v, want %v", got.Addr, officeVoicePE)
	}
}

// Nobody answering is asked again, the wait doubling, then reported.
func TestASilentLinkIsAskedThreeTimesThenNoAnswer(t *testing.T) {
	link, timers := newLink(), newTimers()
	r := &Resolver{Listen: link.listen, Timers: timers}

	done := make(chan error, 1)
	go func() {
		_, err := r.Host(context.Background(), "garage-satellite1-0d4f22.local")
		done <- err
	}()
	for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		link.query(t)
		if got := timers.fire(t); got != want {
			t.Errorf("waited %v, want %v", got, want)
		}
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrNoAnswer) {
			t.Errorf("err = %v, want ErrNoAnswer", err)
		}
	case <-time.After(patience):
		t.Fatal("never gave up")
	}
	link.allClosed(t)
}

// A node's SRV names the host; when its address does not ride along, the
// host is asked for, and the answer holds as long as the shorter TTL.
//
// verifies SPEC §13
func TestNodeAsksForTheHostItsInstanceNames(t *testing.T) {
	link, timers := newLink(), newTimers()
	r := &Resolver{Listen: link.listen, Timers: timers}
	kitchen := netip.MustParseAddr("192.168.1.31")

	got := ask(t, func(ctx context.Context) (Found, error) {
		return r.Node(ctx, "kitchen-satellite1-a1b2c3")
	}, func() {
		q := link.query(t)
		if q.Name.String() != "kitchen-satellite1-a1b2c3._esphomelib._tcp.local." || q.Type != dnsmessage.TypeSRV {
			t.Errorf("asked %v", q)
		}
		link.reply(t, reply(srvRecord("kitchen-satellite1-a1b2c3._esphomelib._tcp.local.", "kitchen-satellite1-a1b2c3.local.", 6053, 120)))
		q = link.query(t)
		if q.Name.String() != "kitchen-satellite1-a1b2c3.local." || q.Type != dnsmessage.TypeA {
			t.Errorf("then asked %v", q)
		}
		link.reply(t, reply(aRecord("kitchen-satellite1-a1b2c3.local.", kitchen, 10, false)))
	})
	if got.Addr != kitchen || got.Port != 6053 || got.TTL != 10*time.Second {
		t.Errorf("found %+v, want %v:6053 for 10s", got, kitchen)
	}
}

func TestOnlyALocalNameIsAskedOfTheLink(t *testing.T) {
	r := &Resolver{Listen: newLink().listen, Timers: newTimers()}
	if _, err := r.Host(context.Background(), "office.lan"); err == nil {
		t.Error("office.lan was asked of the link")
	}
}

// The daemon stopping ends a query mid-wait, and its socket with it.
func TestACancelledQueryReturnsAndClosesItsSocket(t *testing.T) {
	link, timers := newLink(), newTimers()
	r := &Resolver{Listen: link.listen, Timers: timers}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.Node(ctx, "kitchen-satellite1-a1b2c3")
		done <- err
	}()
	link.query(t)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want canceled", err)
		}
	case <-time.After(patience):
		t.Fatal("query outlived its context")
	}
	link.allClosed(t)
}

// ask runs a query while script plays the link's side of it.
func ask(t *testing.T, query func(context.Context) (Found, error), script func()) Found {
	t.Helper()
	type result struct {
		f   Found
		err error
	}
	done := make(chan result, 1)
	go func() {
		f, err := query(t.Context())
		done <- result{f, err}
	}()
	script()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("query: %v", r.err)
		}
		return r.f
	case <-time.After(patience):
		t.Fatal("query never returned")
		return Found{}
	}
}

func reply(rrs ...dnsmessage.Resource) []byte {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
	_ = b.StartAnswers()
	for _, rr := range rrs {
		switch body := rr.Body.(type) {
		case *dnsmessage.AResource:
			_ = b.AResource(rr.Header, *body)
		case *dnsmessage.SRVResource:
			_ = b.SRVResource(rr.Header, *body)
		}
	}
	out, err := b.Finish()
	if err != nil {
		panic(err)
	}
	return out
}

func aRecord(name string, ip netip.Addr, ttl uint32, flush bool) dnsmessage.Resource {
	class := dnsmessage.ClassINET
	if flush {
		class |= 0x8000
	}
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Type: dnsmessage.TypeA, Class: class, TTL: ttl},
		Body:   &dnsmessage.AResource{A: ip.As4()},
	}
}

func srvRecord(name, target string, port uint16, ttl uint32) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Type: dnsmessage.TypeSRV, Class: dnsmessage.ClassINET | 0x8000, TTL: ttl},
		Body:   &dnsmessage.SRVResource{Target: dnsmessage.MustNewName(target), Port: port},
	}
}

// memLink is the link with no network: what the resolver sends arrives on
// sent, and the test delivers the replies. Each query opens its own socket.
type memLink struct {
	sent    chan []byte
	replies chan []byte

	mu     sync.Mutex
	dest   string
	opened []*memSocket
}

func newLink() *memLink {
	return &memLink{sent: make(chan []byte, 8), replies: make(chan []byte)}
}

func (l *memLink) listen() (net.PacketConn, error) {
	s := &memSocket{link: l, closed: make(chan struct{})}
	l.mu.Lock()
	l.opened = append(l.opened, s)
	l.mu.Unlock()
	return s, nil
}

// query is the next question the resolver sent.
func (l *memLink) query(t *testing.T) dnsmessage.Question {
	t.Helper()
	select {
	case b := <-l.sent:
		var p dnsmessage.Parser
		if _, err := p.Start(b); err != nil {
			t.Fatalf("query does not parse: %v", err)
		}
		q, err := p.Question()
		if err != nil {
			t.Fatalf("query has no question: %v", err)
		}
		return q
	case <-time.After(patience):
		t.Fatal("nothing was asked")
		return dnsmessage.Question{}
	}
}

func (l *memLink) reply(t *testing.T, b []byte) {
	t.Helper()
	select {
	case l.replies <- b:
	case <-time.After(patience):
		t.Fatal("nobody read the reply")
	}
}

func (l *memLink) to() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dest
}

// allClosed waits for every socket a query opened to be closed.
func (l *memLink) allClosed(t *testing.T) {
	t.Helper()
	l.mu.Lock()
	opened := append([]*memSocket(nil), l.opened...)
	l.mu.Unlock()
	for _, s := range opened {
		select {
		case <-s.closed:
		case <-time.After(patience):
			t.Error("a query's socket was left open")
		}
	}
}

type memSocket struct {
	link   *memLink
	closed chan struct{}
	once   sync.Once
}

func (s *memSocket) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case r := <-s.link.replies:
		return copy(b, r), &net.UDPAddr{IP: net.IPv4(192, 168, 1, 42), Port: 5353}, nil
	case <-s.closed:
		return 0, nil, net.ErrClosed
	}
}

func (s *memSocket) WriteTo(b []byte, addr net.Addr) (int, error) {
	s.link.mu.Lock()
	s.link.dest = addr.String()
	s.link.mu.Unlock()
	s.link.sent <- append([]byte(nil), b...)
	return len(b), nil
}

func (s *memSocket) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *memSocket) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(192, 168, 1, 10), Port: 49731}
}
func (s *memSocket) SetDeadline(time.Time) error      { return nil }
func (s *memSocket) SetReadDeadline(time.Time) error  { return nil }
func (s *memSocket) SetWriteDeadline(time.Time) error { return nil }

// stepTimers fire only when the test says, oldest first.
type stepTimers struct {
	mu      sync.Mutex
	pending []pendingTimer
	armed   chan struct{}
}

type pendingTimer struct {
	d  time.Duration
	ch chan time.Time
}

func newTimers() *stepTimers { return &stepTimers{armed: make(chan struct{}, 16)} }

func (s *stepTimers) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	s.mu.Lock()
	s.pending = append(s.pending, pendingTimer{d, ch})
	s.mu.Unlock()
	s.armed <- struct{}{}
	return ch
}

// fire ends the oldest wait once it is armed, and says how long it was.
func (s *stepTimers) fire(t *testing.T) time.Duration {
	t.Helper()
	select {
	case <-s.armed:
	case <-time.After(patience):
		t.Fatal("nothing waited")
	}
	s.mu.Lock()
	p := s.pending[0]
	s.pending = s.pending[1:]
	s.mu.Unlock()
	p.ch <- time.Time{}
	return p.d
}
