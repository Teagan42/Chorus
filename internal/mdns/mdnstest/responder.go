// Package mdnstest is the household's link for tests: a responder that
// answers one-shot mDNS queries the way an ESPHome satellite does, on a
// loopback socket, so mdns.Resolver.To can point at it.
package mdnstest

import (
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// Node is one ESPHome device's advertisement: its node name is both its
// host label and its _esphomelib._tcp instance.
type Node struct {
	Name     string
	Friendly string
	MAC      string
	Board    string
	Addr     netip.Addr
	// Port is the native API's; zero is ESPHome's 6053.
	Port uint16
}

// Responder answers for the nodes it advertises. A legacy unicast query
// gets the reply RFC 6762 §6.7 asks for: its ID and question echoed, ten
// second TTLs, no cache-flush bit.
type Responder struct {
	conn net.PacketConn

	mu    sync.Mutex
	nodes map[string]Node
	// split leaves the host's address out of an instance's reply, so it
	// must be asked for itself.
	split   bool
	queries []string
	asked   chan struct{}
}

// Listen starts a responder on loopback, closed when t ends.
func Listen(t *testing.T) *Responder {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mdnstest: %v", err)
	}
	r := &Responder{conn: conn, nodes: map[string]Node{}, asked: make(chan struct{}, 64)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.serve()
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
	return r
}

// Addr is where queries for this link go.
func (r *Responder) Addr() net.Addr { return r.conn.LocalAddr() }

// Advertise answers for n from now on, at its address as given: a node
// that moved is advertised again.
func (r *Responder) Advertise(n Node) {
	if n.Port == 0 {
		n.Port = 6053
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[strings.ToLower(n.Name)] = n
}

// Withdraw stops answering for the node: it is unplugged.
func (r *Responder) Withdraw(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nodes, strings.ToLower(name))
}

// SplitAddress leaves the host's A record out of an instance's reply.
func (r *Responder) SplitAddress() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.split = true
}

// Queries is every question asked so far, as "name TYPE".
func (r *Responder) Queries() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.queries...)
}

// Asked delivers once per question asked.
func (r *Responder) Asked() <-chan struct{} { return r.asked }

func (r *Responder) serve() {
	buf := make([]byte, 9000)
	for {
		n, from, err := r.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		if reply := r.answer(buf[:n]); reply != nil {
			_, _ = r.conn.WriteTo(reply, from)
		}
	}
}

// answer is the reply to a query, or nil when nothing it asks is ours.
func (r *Responder) answer(query []byte) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil || h.Response {
		return nil
	}
	qs, err := p.AllQuestions()
	if err != nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var answers, extra []dnsmessage.Resource
	for _, q := range qs {
		r.queries = append(r.queries, q.Name.String()+" "+strings.TrimPrefix(q.Type.String(), "Type"))
		select {
		case r.asked <- struct{}{}:
		default:
		}
		name := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))
		switch {
		case q.Type == dnsmessage.TypeA && strings.HasSuffix(name, ".local"):
			if n, ok := r.nodes[strings.TrimSuffix(name, ".local")]; ok {
				answers = append(answers, a(n))
			}
		case q.Type == dnsmessage.TypeSRV && strings.HasSuffix(name, "._esphomelib._tcp.local"):
			if n, ok := r.nodes[strings.TrimSuffix(name, "._esphomelib._tcp.local")]; ok {
				answers = append(answers, srv(n))
				extra = append(extra, txt(n))
				if !r.split {
					extra = append(extra, a(n))
				}
			}
		}
	}
	if len(answers) == 0 {
		return nil
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true})
	b.EnableCompression()
	_ = b.StartQuestions()
	for _, q := range qs {
		_ = b.Question(q)
	}
	_ = b.StartAnswers()
	for _, rr := range answers {
		add(&b, rr)
	}
	_ = b.StartAdditionals()
	for _, rr := range extra {
		add(&b, rr)
	}
	out, err := b.Finish()
	if err != nil {
		return nil
	}
	return out
}

// legacyTTL is the most a legacy unicast reply may give (RFC 6762 §6.7).
const legacyTTL = 10

func header(name string, typ dnsmessage.Type) dnsmessage.ResourceHeader {
	return dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET, TTL: legacyTTL}
}

func a(n Node) dnsmessage.Resource {
	return dnsmessage.Resource{Header: header(n.Name+".local.", dnsmessage.TypeA), Body: &dnsmessage.AResource{A: n.Addr.As4()}}
}

func srv(n Node) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: header(n.Name+"._esphomelib._tcp.local.", dnsmessage.TypeSRV),
		Body:   &dnsmessage.SRVResource{Target: dnsmessage.MustNewName(n.Name + ".local."), Port: n.Port},
	}
}

// txt is what ESPHome's mdns component says about a node.
func txt(n Node) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: header(n.Name+"._esphomelib._tcp.local.", dnsmessage.TypeTXT),
		Body: &dnsmessage.TXTResource{TXT: []string{
			"friendly_name=" + n.Friendly, "version=2026.7.2", "mac=" + n.MAC,
			"platform=ESP32", "board=" + n.Board, "network=wifi",
			"api_encryption=Noise_NNpsk0_25519_ChaChaPoly_SHA256",
		}},
	}
}

func add(b *dnsmessage.Builder, rr dnsmessage.Resource) {
	switch body := rr.Body.(type) {
	case *dnsmessage.AResource:
		_ = b.AResource(rr.Header, *body)
	case *dnsmessage.SRVResource:
		_ = b.SRVResource(rr.Header, *body)
	case *dnsmessage.TXTResource:
		_ = b.TXTResource(rr.Header, *body)
	}
}
