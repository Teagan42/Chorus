package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/announce"
	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/satellite"
	"github.com/teagan42/chorus/internal/session"
	"github.com/teagan42/chorus/internal/timer"
)

// helloTimeout bounds the opening handshake, so a connection from something
// that is not a satellite cannot hold a serve goroutine forever.
const helloTimeout = 10 * time.Second

// minBargeInWords is the gate's third stage: one word is usually "uh" (SPEC §4.3).
const minBargeInWords = 2

// rehearseEvery is how often rendering the canned lines is retried while the
// synthesiser is not answering, as it may not be yet when the daemon starts.
const rehearseEvery = 30 * time.Second

// bargeInGate is the detector every link's supervisor shares. Only this place
// knows whether a resolver was configured, so the speaker mode is set here,
// not from an empty id (ADR-0031).
func (p providers) bargeInGate() session.Gate {
	return session.Gate{
		MinEnergy:            listen.DefaultSpeechEnergy,
		MinWords:             minBargeInWords,
		Household:            p.household,
		SpeakerIDUnavailable: p.speakers == nil,
	}
}

// endpointer is a link's own: it holds the turn in progress on that device.
// Nil leaves the listener's Energy default. The judge's "finished" is checked
// against the words, decoded by the transcriber every turn is (ADR-0042).
func (p providers) endpointer() listen.Endpointer {
	if p.judge == nil {
		return nil
	}
	return listen.NewSemantic(listen.Dangling{Judge: p.judge, Words: p.stt})
}

// deps is everything the daemon composes that does I/O or reads a clock, so
// a test runs the whole daemon over doubles (CONTRIBUTING §1).
type deps struct {
	// Listener accepts satellites dialing the audio port.
	Listener net.Listener

	// Native dials each satellite's native API. Nil holds none.
	Native nativeDialer

	Store  journal.Store
	Blobs  blob.Store
	Clock  journal.Clock
	Timers session.Timers
	providers

	// Memories keeps what each person asked to be remembered, and what their
	// conversations were about. Nil remembers nothing: remember and forget
	// answer not_implemented, no conversation is summarized, and no turn is
	// told anything (SPEC §5).
	Memories memory.Store

	Log *slog.Logger
}

// daemon is one process: shared stack, one supervisor per link.
type daemon struct {
	deps
	inv     *config.Config
	byHost  map[string]*config.Satellite
	journal *journal.Journal
	// convs is the one thing per-satellite supervisors share: the person's
	// conversation follows them from device to device (SPEC §4.5, ADR-0022).
	convs    *session.Conversations
	gate     session.Gate
	memories session.Memories

	// rooms is where each satellite stands, from the inventory: what every
	// turn is told, whichever satellite's supervisor opens it (SPEC §5).
	rooms map[string]string

	// links is each connected satellite's Listening child, by name: where
	// an announcement for that satellite is said (ADR-0045).
	linksMu sync.Mutex
	links   map[string]*listen.Listener

	// wg also counts the summaries still being written, which outlive the
	// link whose conversation ended: run returns only once they are kept.
	wg sync.WaitGroup
}

// run serves satellites until ctx ends, then returns once every link is
// closed and every goroutine it started has exited. A listener that dies
// otherwise is the error, and ends everything else the same way.
func run(ctx context.Context, inv *config.Config, d deps) error {
	byHost, err := indexByHost(inv)
	if err != nil {
		return err
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dm := &daemon{
		deps:    d,
		inv:     inv,
		byHost:  byHost,
		journal: journal.New(d.Store, d.Clock, d.versions),
		convs:   session.NewConversations(d.Clock, session.MigrationWindow),
		gate:    d.bargeInGate(),
		links:   map[string]*listen.Listener{},
		rooms:   map[string]string{},
	}
	for _, sat := range inv.Satellites {
		dm.rooms[sat.Name] = sat.Room
	}
	// The executors join whatever else is wired, Home Assistant's included.
	tools := maps.Clone(d.tools)
	if tools == nil {
		tools = map[string]session.Tool{}
	}
	if d.Memories != nil {
		maps.Copy(tools, memory.Tools(d.Memories, d.Clock))
		dm.memories = memory.Recaller(d.Memories, memory.RecallConfig{
			Embedder: d.embedder,
			Failed: func(person string, err error) {
				d.Log.Warn("recall: ranking failed, so the turn is told the newest", "person", person, "err", err)
			},
		})
	} else {
		// Nowhere to keep a summary, so none is written.
		dm.summarizer = nil
	}
	// Timers live in the journal, so a household always has them: armed
	// from the house log before the first satellite connects (ADR-0045).
	timers, err := timer.Start(ctx, timer.Config{
		Journal: dm.journal, Store: d.Store, Clock: d.Clock, Timers: d.Timers,
		Announcer: dm, Log: d.Log,
	})
	if err != nil {
		return err
	}
	defer timers.Wait()
	maps.Copy(tools, timer.Tools(timers))
	tools["announce"] = announce.Tool(dm, inv.Satellites)
	dm.tools = tools
	// Every satellite speaks through one voice that keeps its own apology,
	// rendered while it still answers: that line is only ever needed once
	// it does not (SPEC §7, ADR-0051).
	canned := satellite.NewCanned(d.synth)
	dm.synth = canned
	dm.wg.Add(1)
	go func() {
		defer dm.wg.Done()
		dm.rehearse(ctx, canned)
	}()
	if d.Native != nil {
		for i := range inv.Satellites {
			sat := &inv.Satellites[i]
			dm.wg.Add(1)
			go func() {
				defer dm.wg.Done()
				dm.keep(ctx, sat)
			}()
		}
	}
	err = dm.accept(ctx)
	cancel()
	dm.wg.Wait()
	return err
}

// rehearse renders the canned lines, retrying until the synthesiser answers
// or the daemon stops. Until it does, a voice that fails mid-turn fails in
// silence, so that is said rather than left to be found.
func (d *daemon) rehearse(ctx context.Context, c *satellite.Canned) {
	lines := session.DefaultCanned.Lines()
	for {
		err := c.Render(ctx, lines...)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		d.Log.Warn("the canned lines are not rendered yet, so a voice that fails mid-turn cannot apologise; retrying",
			"err", err, "retry_in", rehearseEvery)
		select {
		case <-ctx.Done():
			return
		case <-d.Timers.After(rehearseEvery):
		}
	}
}

// indexByHost maps each inventory address's host to its satellite. The audio
// link's hello carries no name (internal/bridge), so the source address is
// how a device is known; a host two satellites share, or one that is not an
// IP literal, cannot be matched and is refused up front.
func indexByHost(inv *config.Config) (map[string]*config.Satellite, error) {
	byHost := make(map[string]*config.Satellite, len(inv.Satellites))
	for i := range inv.Satellites {
		sat := &inv.Satellites[i]
		host, _, err := net.SplitHostPort(sat.Address)
		if err != nil {
			return nil, fmt.Errorf("satellite %s: address %q: %w", sat.Name, sat.Address, err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return nil, fmt.Errorf("satellite %s: address host %q is not an IP literal, and the audio link identifies a device by its source address", sat.Name, host)
		}
		key := ip.String()
		if other, dup := byHost[key]; dup {
			return nil, fmt.Errorf("satellites %s and %s share the host %s, so their audio links cannot be told apart", other.Name, sat.Name, key)
		}
		byHost[key] = sat
	}
	return byHost, nil
}

// accept hands each connection to its own goroutine until ctx ends. Closing
// the listener is the only way to unblock Accept, so the close rides on ctx.
func (d *daemon) accept(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = d.Listener.Close()
	}()
	for {
		conn, err := d.Listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept on %s: %w", d.Listener.Addr(), err)
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.serve(ctx, conn)
		}()
	}
}

// satelliteAt names the device behind a remote address, or nil.
func (d *daemon) satelliteAt(addr net.Addr) *config.Satellite {
	var ip net.IP
	if tcp, ok := addr.(*net.TCPAddr); ok {
		ip = tcp.IP
	} else {
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			return nil
		}
		ip = net.ParseIP(host)
	}
	if ip == nil {
		return nil
	}
	return d.byHost[ip.String()]
}

// serve is one connection's whole life: identify, handshake, attach the
// stack, serve the link, tear down in order.
func (d *daemon) serve(ctx context.Context, conn net.Conn) {
	remote := conn.RemoteAddr().String()
	sat := d.satelliteAt(conn.RemoteAddr())
	if sat == nil {
		// Before a frame is read: nothing from an address the inventory does
		// not name is worth parsing (SPEC §13).
		d.Log.Warn("refused a connection from an address not in the inventory", "remote", remote)
		_ = conn.Close()
		return
	}
	log := d.Log.With("satellite", sat.Name, "remote", remote)

	link, err := d.handshake(ctx, conn)
	if err != nil {
		log.Warn("audio link handshake failed", "err", err)
		return
	}
	defer func() { _ = link.Close() }()

	if err := d.attach(ctx, sat, link, log); err != nil {
		log.Warn("audio link failed", "err", err)
		return
	}
	log.Info("satellite disconnected")
}

// handshake completes the hello, bounded by the injected timers rather than
// a socket deadline, so no wall clock is read here.
func (d *daemon) handshake(ctx context.Context, conn net.Conn) (*bridge.Link, error) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		// Nagle would coalesce the 32 ms chunks barge-in timing depends on.
		_ = tcp.SetNoDelay(true)
	}
	link, err := within(ctx, d.Timers, helloTimeout, func(ctx context.Context) (*bridge.Link, error) {
		// A net.Conn read is not cancellable; closing it is. Only while the
		// read is in flight: within ends its context on return too, and a
		// handshake that succeeded must keep its connection, so done is
		// checked again once the context ends rather than raced against it.
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-done:
				return
			case <-ctx.Done():
			}
			select {
			case <-done:
			default:
				_ = conn.Close()
			}
		}()
		return bridge.NewLink(conn)
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return link, nil
}

// attach builds this link's Speaking and Listening sides on a supervisor of
// its own and serves until the link drops. The Speaker is per device, which
// is why the supervisor is too; the Conversations are shared (ADR-0022).
//
// Teardown order: Serve returns, the link's context ends, the listener
// closes the session as device_lost and finishes its goroutines, then the
// link is closed by the caller (ADR-0030).
func (d *daemon) attach(ctx context.Context, sat *config.Satellite, link *bridge.Link, log *slog.Logger) error {
	linkCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	speaker, err := satellite.New(satellite.Config{
		Link: link, Synth: d.synth, Timers: d.Timers, Blobs: d.Blobs,
	})
	if err != nil {
		return err
	}
	sup, err := session.New(session.Config{
		Journal: d.journal, Store: d.Store, Clock: d.Clock, Timers: d.Timers,
		Engine: d.engine, Speaker: speaker, Conversations: d.convs,
		Tools: d.tools, Gate: d.gate, Memories: d.memories, Rooms: d.rooms,
		Summarizer: d.summarizer, Summarizing: &d.wg, Log: log,
	})
	if err != nil {
		return err
	}
	lst, err := listen.Open(linkCtx, listen.Config{
		Satellite: sat.Name, Sessions: sup, Transcriber: d.stt, Speakers: d.speakers,
		Playback: speaker, Blobs: d.Blobs, Journal: d.journal, Clock: d.Clock, Log: log,
		Endpointer: d.endpointer(),
	})
	if err != nil {
		return err
	}
	d.link(sat.Name, lst)
	defer d.unlink(sat.Name, lst)
	// Logged once the link can take an announcement, not at the hello.
	log.Info("satellite connected", "mic_channels", link.Hello().MicChannels)

	err = link.Serve(linkCtx, handlers{speaker, lst})
	cancel()
	<-lst.Done()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// link makes lst where the satellite's announcements are said. A device
// that reconnects replaces the link it left behind.
func (d *daemon) link(name string, lst *listen.Listener) {
	d.linksMu.Lock()
	defer d.linksMu.Unlock()
	d.links[name] = lst
}

// unlink forgets lst, unless a newer link has already replaced it.
func (d *daemon) unlink(name string, lst *listen.Listener) {
	d.linksMu.Lock()
	defer d.linksMu.Unlock()
	if d.links[name] == lst {
		delete(d.links, name)
	}
}

// Announce says a on the named satellite, through its Listening child, so
// it joins whatever session is open there (announce.Announcer).
func (d *daemon) Announce(ctx context.Context, satellite string, a session.Announcement) (string, error) {
	d.linksMu.Lock()
	lst := d.links[satellite]
	d.linksMu.Unlock()
	if lst == nil {
		return "", announce.ErrNotConnected
	}
	conv, err := lst.Announce(ctx, a)
	if errors.Is(err, listen.ErrLinkClosed) {
		return "", announce.ErrNotConnected
	}
	return conv, err
}

// handlers fans one link's frames out to both children: the satellite
// tracks the DAC position it truncates on, the listener tracks its own copy
// for the candidates it offers, from the same Played frames (ADR-0030).
type handlers []bridge.Handler

func (hs handlers) OnMic(channel uint8, pcm []byte) error {
	for _, h := range hs {
		if err := h.OnMic(channel, pcm); err != nil {
			return err
		}
	}
	return nil
}

func (hs handlers) OnWake(word string) error {
	for _, h := range hs {
		if err := h.OnWake(word); err != nil {
			return err
		}
	}
	return nil
}

func (hs handlers) OnPlayed(p bridge.Played) error {
	for _, h := range hs {
		if err := h.OnPlayed(p); err != nil {
			return err
		}
	}
	return nil
}

func (hs handlers) OnMute(m bridge.Mute) error {
	for _, h := range hs {
		if err := h.OnMute(m); err != nil {
			return err
		}
	}
	return nil
}

// within runs f under a context the injected timers end after d. It exists
// so a bound on a dial or a handshake never reads the wall clock.
func within[T any](ctx context.Context, timers session.Timers, d time.Duration, f func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		v   T
		err error
	}
	res := make(chan result, 1)
	go func() {
		v, err := f(ctx)
		res <- result{v, err}
	}()
	select {
	case r := <-res:
		return r.v, r.err
	case <-timers.After(d):
		cancel()
		r := <-res
		if r.err == nil {
			return r.v, nil
		}
		var zero T
		return zero, fmt.Errorf("gave up after %v: %w", d, r.err)
	}
}
