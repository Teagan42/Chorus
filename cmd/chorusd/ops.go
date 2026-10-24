package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/ops"
	"github.com/teagan42/chorus/internal/provider/kokoro"
	"github.com/teagan42/chorus/internal/provider/openai"
	"github.com/teagan42/chorus/internal/provider/smartturn"
	"github.com/teagan42/chorus/internal/provider/speakerid"
	"github.com/teagan42/chorus/internal/provider/wyoming"
)

// opsListenEnv is where /healthz, /readyz and /metrics are served
// (ADR-0068). "off" serves none.
const opsListenEnv = "CHORUS_OPS_LISTEN"

// defaultOpsListen is loopback: nothing there asks who is asking, and the
// counters say when the house is talking, so reaching it from another
// machine is a choice, as the review UI's is (ADR-0068).
const defaultOpsListen = "127.0.0.1:6056"

// opsOff turns the listener off.
const opsOff = "off"

// opsShutdown bounds how long open requests may finish once the daemon stops.
const opsShutdown = 5 * time.Second

func (c Config) opsEnabled() bool { return c.OpsListen != opsOff }

// opsListenErr says what is wrong with the address, or nil.
func (c Config) opsListenErr() error {
	if !c.opsEnabled() {
		return nil
	}
	if _, _, err := net.SplitHostPort(c.OpsListen); err != nil {
		return fmt.Errorf("%s=%q is not host:port (or %q): %w", opsListenEnv, c.OpsListen, opsOff, err)
	}
	return nil
}

// opsDeps is what start hands the daemon to serve the endpoints.
type opsDeps struct {
	// Listener serves the endpoints. Nil keeps their state and serves none.
	Listener net.Listener
	// Checks are the dependencies readiness probes.
	Checks []ops.Check
	// Metrics is the registry start already metered the blob store into.
	// Nil makes one.
	Metrics *ops.Metrics
}

// opsChecks are the dependencies the house configured: required as
// validate requires them, the optional sidecars only when set. Each asks
// the cheapest route its server has.
func opsChecks(cfg Config, client *http.Client, postgres func(context.Context) error) []ops.Check {
	at := func(base, path string) string { return strings.TrimSuffix(base, "/") + path }
	// vLLM's /health answers without the API key; only /v1 asks for it.
	llm := at(cfg.OllamaURL, "/api/version")
	if cfg.LLMProvider == providerVLLM {
		llm = at(strings.TrimSuffix(strings.TrimSuffix(cfg.VLLMURL, "/"), "/v1"), "/health")
	}
	checks := []ops.Check{
		{Name: "postgres", Required: true, Probe: postgres},
		{Name: "stt", Required: true, Probe: earProbe(cfg, client)},
		{Name: "tts", Required: true, Probe: voiceProbe(cfg, client)},
		{Name: "llm", Required: true, Probe: ops.HTTPProbe(client, llm, nil)},
	}
	// The sidecars that refuse a reply from another model are asked which
	// model they serve, so one that answers but would refuse every call is
	// down here, not discovered at the first wake (ADR-0097).
	if cfg.SpeakerIDURL != "" {
		emb, err := speakerid.New(speakerid.Config{BaseURL: cfg.SpeakerIDURL, HTTP: client})
		checks = append(checks, ops.Check{Name: "speaker_id", Probe: healthOf(emb, err)})
	}
	if cfg.SmartTurnURL != "" {
		judge, err := smartturn.New(smartturn.Config{BaseURL: cfg.SmartTurnURL, HTTP: client})
		checks = append(checks, ops.Check{Name: "smart_turn", Probe: healthOf(judge, err)})
	}
	if cfg.HassURL != "" {
		// HA's API root answers "API running." to a token it accepts.
		auth := http.Header{"Authorization": {"Bearer " + cfg.HassToken}}
		checks = append(checks, ops.Check{Name: "home_assistant", Probe: ops.HTTPProbe(client, at(cfg.HassURL, "/api/"), auth)})
	}
	return checks
}

// voiceProbe asks the chosen voice's server whether it is up: Kokoro's
// /health, an OpenAI-compatible server's model list with its key, or a
// Wyoming server's describe (ADR-0081).
func voiceProbe(cfg Config, client *http.Client) func(context.Context) error {
	switch cfg.TTSProvider {
	case providerOpenAI:
		return ops.HTTPProbe(client, openai.HealthURL(cfg.OpenAITTSURL), bearer(cfg.OpenAITTSKey))
	case providerWyoming:
		return wyoming.Probe(cfg.WyomingTTSAddr)
	}
	// Kokoro's voice list, not its /health: it answers only once the server
	// is up, and it is the one place an unknown voice shows before the first
	// utterance is a 400 (ADR-0097).
	synth, err := kokoro.New(kokoro.Config{BaseURL: cfg.KokoroURL, HTTP: client})
	return healthOf(synth, err)
}

// healthOf is a client's own health check as a probe, or the error that
// building it returned.
func healthOf[C interface{ Health(context.Context) error }](c C, err error) func(context.Context) error {
	if err != nil {
		return func(context.Context) error { return err }
	}
	return c.Health
}

// earProbe is voiceProbe for the ear.
func earProbe(cfg Config, client *http.Client) func(context.Context) error {
	switch cfg.STTProvider {
	case providerOpenAI:
		return ops.HTTPProbe(client, openai.HealthURL(cfg.OpenAISTTURL), bearer(cfg.OpenAISTTKey))
	case providerWyoming:
		return wyoming.Probe(cfg.WyomingSTTAddr)
	}
	return ops.HTTPProbe(client, strings.TrimSuffix(cfg.STTURL, "/")+"/health", nil)
}

// bearer is the header a key goes in, or none without one.
func bearer(key string) http.Header {
	if key == "" {
		return nil
	}
	return http.Header{"Authorization": {"Bearer " + key}}
}

// wireOps composes the endpoints' state and taps what feeds it: the
// journal's appends, and each satellite's native API dials.
func (d *daemon) wireOps() {
	if d.Ops == nil {
		return
	}
	sats := make([]ops.Satellite, 0, len(d.inv.Satellites))
	for _, sat := range d.inv.Satellites {
		sats = append(sats, ops.Satellite{Name: sat.Name, Room: sat.Room})
	}
	d.ops = ops.New(ops.Config{
		Version: version, Clock: d.Clock, Timers: d.Timers,
		Checks: d.Ops.Checks, Satellites: sats, Metrics: d.Ops.Metrics, Log: d.Log,
	})
	d.journal.Observe(d.ops.Metrics())
	if d.Native != nil {
		d.Native = trackedDialer{nativeDialer: d.Native, sats: d.ops.Satellites(), named: d.addrs.dialled}
	}
}

// serveOps probes and serves until ctx ends, then closes the listener and
// lets open requests finish, bounded by the injected timers.
func (d *daemon) serveOps(ctx context.Context) {
	var probing sync.WaitGroup
	defer probing.Wait()
	probing.Go(func() { d.ops.Run(ctx) })
	if d.Ops.Listener == nil {
		return
	}
	srv := &http.Server{Handler: d.ops.Handler(), ReadHeaderTimeout: 5 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(d.Ops.Listener) }()
	d.Log.Info("ops endpoints", "addr", d.Ops.Listener.Addr().String(), "paths", "/healthz /readyz /metrics")
	select {
	case err := <-served:
		d.Log.Warn("ops endpoints stopped; the daemon runs on unwatched", "err", err)
	case <-ctx.Done():
		_, _ = within(context.WithoutCancel(ctx), d.Timers, opsShutdown, func(sctx context.Context) (struct{}, error) {
			return struct{}{}, srv.Shutdown(sctx)
		})
		_ = srv.Close()
		<-served
	}
}

// trackedDialer tells the satellites' state each native API connection
// held and dropped.
type trackedDialer struct {
	nativeDialer
	sats *ops.Satellites
	// named is the satellite dialled at an address.
	named func(address string) string
}

func (t trackedDialer) Dial(ctx context.Context, address, psk string) (nativeConn, error) {
	c, err := t.nativeDialer.Dial(ctx, address, psk)
	if err != nil {
		return nil, err
	}
	name := t.named(address)
	t.sats.Native(name, true)
	return &trackedConn{nativeConn: c, dropped: func() { t.sats.Native(name, false) }}, nil
}

// trackedConn reports the drop on its first Close, which hold always makes.
type trackedConn struct {
	nativeConn
	once    sync.Once
	dropped func()
}

func (c *trackedConn) Close() error {
	err := c.nativeConn.Close()
	c.once.Do(c.dropped)
	return err
}

// frameSeen stamps each frame a satellite's audio link delivers.
type frameSeen struct {
	sats *ops.Satellites
	name string
}

var _ bridge.Handler = frameSeen{}

func (f frameSeen) OnMic(uint8, []byte) error    { f.sats.Frame(f.name); return nil }
func (f frameSeen) OnWake(string) error          { f.sats.Frame(f.name); return nil }
func (f frameSeen) OnPlayed(bridge.Played) error { f.sats.Frame(f.name); return nil }
func (f frameSeen) OnMute(bridge.Mute) error     { f.sats.Frame(f.name); return nil }

// linkHandlers is what serves one satellite's frames: the ops stamp first,
// so a frame the stack refuses was still seen.
func (d *daemon) linkHandlers(name string, hs ...bridge.Handler) handlers {
	if d.ops == nil {
		return hs
	}
	return append(handlers{frameSeen{sats: d.ops.Satellites(), name: name}}, hs...)
}

// opsLinked tells the endpoints a satellite's audio link came up or went.
func (d *daemon) opsLinked(name string, up bool) {
	if d.ops != nil {
		d.ops.Satellites().Linked(name, up)
	}
}

// healthURL is the /healthz a container's own check asks: loopback when the
// listener binds every interface.
func healthURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

// healthcheck asks a running chorusd's /healthz: the image has no shell or
// curl, so its container healthcheck is the binary itself (-healthcheck).
func healthcheck(ctx context.Context, client *http.Client, addr string) error {
	if addr == opsOff {
		return errors.New(opsListenEnv + " is off, so there is nothing to ask")
	}
	url := healthURL(addr)
	if url == "" {
		return fmt.Errorf("%s=%q is not host:port", opsListenEnv, addr)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %d %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
