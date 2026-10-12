// Package mcp offers the model the tools of the household's MCP servers, as
// one provider among the registry's (SPEC §6, ADR-0075). The registry owns
// what MCP has no word for, and every MCP tool starts from DefaultPolicy.
//
// The tools offered are fixed when chorusd starts: the journal stamps one
// tool-schema version for the process, a hash of what the model is sent
// (SPEC §8), so a list that changed later is reconciled for calls and
// logged, and offered from the next start.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

// Timing of a server's connection. Every wait runs on Options.Timers, so a
// test drives it (CONTRIBUTING §1).
const (
	// requestTimeout bounds a handshake and a listing: a server that
	// accepts and never answers must not hold startup.
	requestTimeout = 10 * time.Second
	// A lost server is retried after minBackoff, doubling to maxBackoff
	// while it stays unreachable.
	minBackoff = time.Second
	maxBackoff = time.Minute
)

// maxResult bounds what one call's result puts in the model's context.
const maxResult = 16 << 10

// subscriptionsAcknowledged is the server saying it will now send
// list_changed, under protocol 2026-07-28 and later.
const subscriptionsAcknowledged = "notifications/subscriptions/acknowledged"

// Timers is the clock's timer half.
type Timers interface {
	After(d time.Duration) <-chan time.Time
}

// Dial opens a fresh transport to a server, once per connection attempt.
type Dial func(Server) (sdk.Transport, error)

// Options wires a Provider. Everything that reads a clock or does I/O is
// injected, so `task test` stays hermetic.
type Options struct {
	Timers Timers
	// Log is where connections, refusals and list changes are reported.
	// Nil discards.
	Log *slog.Logger
	// Dial defaults to Transport: a command over stdio, or a URL over
	// Streamable HTTP.
	Dial Dial
	// Native is every tool name already taken. Nil is registry.Specs.
	Native map[string]registry.ToolSpec
	// Version is what chorusd tells each server it is.
	Version string
}

// Provider is the household's MCP servers, connected, and the tools they
// offered when it started.
type Provider struct {
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	servers []*server
	specs   map[string]registry.ToolSpec
	tools   map[string]session.Tool
}

// Start connects to every server and waits, up to cfg.DiscoveryTimeout, for
// each to list its tools. A server that has not by then is offered nothing
// this process, and is retried for the log's sake; one that has is kept
// connected, and reconnected when it drops, until Close.
func Start(ctx context.Context, cfg Config, opts Options) (*Provider, error) {
	if opts.Timers == nil {
		return nil, errors.New("mcp: timers are required")
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Dial == nil {
		opts.Dial = Transport
	}
	if opts.Native == nil {
		opts.Native = registry.Specs
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	ctx, cancel := context.WithCancel(ctx)
	p := &Provider{cancel: cancel}
	for _, sc := range cfg.Servers {
		s := &server{
			cfg: sc, opts: opts, log: opts.Log.With("mcp_server", sc.Name),
			changed: make(chan struct{}, 1), listed: make(chan struct{}),
		}
		p.servers = append(p.servers, s)
		p.wg.Go(func() { s.run(ctx) })
	}
	deadline := opts.Timers.After(cfg.DiscoveryTimeout)
wait:
	for _, s := range p.servers {
		select {
		case <-s.listed:
		case <-deadline:
			break wait
		case <-ctx.Done():
			p.Close()
			return nil, ctx.Err()
		}
	}
	p.offer()
	return p, nil
}

// Specs is every tool offered, by the name the model calls it.
func (p *Provider) Specs() map[string]registry.ToolSpec { return maps.Clone(p.specs) }

// Tools is each offered tool's executor.
func (p *Provider) Tools() map[string]session.Tool { return maps.Clone(p.tools) }

// Close disconnects every server, stopping any command it started, and
// returns once nothing it started is running.
func (p *Provider) Close() {
	p.cancel()
	p.wg.Wait()
}

// offer fixes what the model is offered from what each server listed.
func (p *Provider) offer() {
	p.specs, p.tools = map[string]registry.ToolSpec{}, map[string]session.Tool{}
	for _, s := range p.servers {
		specs, offered, ok := s.freeze()
		if !ok {
			s.log.Warn("mcp server offers nothing: it had not listed its tools by the end of startup's discovery window, and what the model is offered is fixed until the next start")
			continue
		}
		names := slices.Sorted(maps.Keys(specs))
		for _, name := range names {
			p.specs[name] = specs[name]
			p.tools[name] = call{s: s, tool: offered[name]}
		}
		s.log.Info("mcp server offers its tools", "offered", names)
	}
}

// server is one MCP server's connection, kept up for as long as chorusd runs.
type server struct {
	cfg  Server
	opts Options
	log  *slog.Logger

	// changed is poked by the server's notifications/tools/list_changed.
	changed chan struct{}
	// listed closes on the first listing, which is what startup waits for.
	listed     chan struct{}
	listedOnce sync.Once

	mu sync.Mutex
	// cs is the live session; nil while disconnected.
	cs *sdk.ClientSession
	// live is what the server lists now, by its own name.
	live map[string]*sdk.Tool
	// first is the listing startup offers from, until frozen.
	first  []*sdk.Tool
	frozen bool
	// offered maps each offered name to the server's own, once frozen.
	offered map[string]string
}

// freeze ends discovery for this server, and declares what its listing
// offers. ok is false when it never listed. Under the lock, so a listing
// that lands meanwhile is reconciled against what was offered.
func (s *server) freeze() (specs map[string]registry.ToolSpec, offered map[string]string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frozen = true
	if s.first == nil {
		return nil, nil, false
	}
	specs, s.offered = s.declare(s.first)
	return specs, s.offered, true
}

// declare turns a listing into the specs offered, refusing, and saying why,
// each tool that cannot be offered safely.
func (s *server) declare(listing []*sdk.Tool) (map[string]registry.ToolSpec, map[string]string) {
	byName := map[string][]*sdk.Tool{}
	listed := map[string]bool{}
	for _, t := range listing {
		listed[t.Name] = true
		name, err := Name(s.cfg.Name, t.Name)
		if err != nil {
			s.log.Warn("mcp tool not offered", "tool", t.Name, "err", err)
			continue
		}
		byName[name] = append(byName[name], t)
	}
	for _, name := range slices.Sorted(maps.Keys(s.cfg.Tools)) {
		if !listed[name] {
			s.log.Warn("mcp configuration names a tool the server does not list", "tool", name)
		}
	}
	out, offered := map[string]registry.ToolSpec{}, map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		tools := byName[name]
		if len(tools) > 1 {
			var clash []string
			for _, t := range tools {
				clash = append(clash, t.Name)
			}
			// Neither: whichever won would depend on the server's order.
			s.log.Warn("mcp tools not offered: their names are the same once made a tool name", "name", name, "tools", clash)
			continue
		}
		t := tools[0]
		if _, taken := s.opts.Native[name]; taken {
			s.log.Warn("mcp tool not offered: chorusd already has a tool by its name", "tool", t.Name, "name", name)
			continue
		}
		spec, offer, err := s.spec(name, t)
		switch {
		case err != nil:
			s.log.Warn("mcp tool not offered", "tool", t.Name, "err", err)
			continue
		case !offer:
			s.log.Info("mcp tool not offered: the configuration says not to", "tool", t.Name)
			continue
		}
		out[name] = spec
		offered[name] = t.Name
	}
	return out, offered
}

// spec is a listed tool as the registry holds it, under its policy.
func (s *server) spec(name string, t *sdk.Tool) (registry.ToolSpec, bool, error) {
	policy := DefaultPolicy().over(s.cfg.Policy).over(s.cfg.Tools[t.Name])
	if !policy.offered() {
		return registry.ToolSpec{}, false, nil
	}
	params, err := Params(t.InputSchema)
	if err != nil {
		return registry.ToolSpec{}, true, err
	}
	spec, err := registry.Provide(policy.apply(registry.ToolSpec{
		Name: name, Description: describeTool(s.cfg.Name, t), Params: params,
	}))
	return spec, true, err
}

// describeTool is what the model is told a tool does: the server's own
// words, or its title, bounded.
func describeTool(server string, t *sdk.Tool) string {
	d := strings.TrimSpace(t.Description)
	if d == "" {
		d = strings.TrimSpace(t.Title)
	}
	if d == "" && t.Annotations != nil {
		d = strings.TrimSpace(t.Annotations.Title)
	}
	if d == "" {
		d = "The " + t.Name + " tool of the " + server + " MCP server."
	}
	return clip(d)
}

// run keeps the server connected until ctx ends, retrying with backoff.
func (s *server) run(ctx context.Context) {
	backoff := minBackoff
	for {
		up, err := s.connect(ctx)
		if ctx.Err() != nil {
			return
		}
		if up {
			backoff = minBackoff
			s.log.Warn("mcp server connection lost; its tools answer mcp_unavailable until it is back", "retry_in", backoff)
		} else {
			s.log.Warn("mcp server unreachable", "retry_in", backoff, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.opts.Timers.After(backoff):
		}
		if !up {
			backoff = min(2*backoff, maxBackoff)
		}
	}
}

// connect holds one session, from the handshake until it drops or ctx ends.
// up reports whether it got as far as a listing.
func (s *server) connect(ctx context.Context) (up bool, err error) {
	t, err := s.opts.Dial(s.cfg)
	if err != nil {
		return false, fmt.Errorf("dial: %w", err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "chorusd", Version: s.opts.Version}, &sdk.ClientOptions{
		// No roots: chorusd has no files to show a server.
		Capabilities:           &sdk.ClientCapabilities{},
		ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) { s.poke() },
	})
	// The subscription to list_changed is acknowledged after the first
	// listing may have been read; listing again then misses no change.
	client.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method == subscriptionsAcknowledged {
				// The SDK subscribes only to tool changes: an ack without them is a refusal.
				if ack, ok := req.GetParams().(*sdk.SubscriptionsAcknowledgedParams); ok && !ack.Notifications.ToolsListChanged {
					s.log.Warn("mcp server will not say when its tools change; a change is seen only when it reconnects")
				}
				s.poke()
			}
			return next(ctx, method, req)
		}
	})
	hctx, cancel := bounded(ctx, s.opts.Timers, requestTimeout)
	cs, err := client.Connect(hctx, t, nil)
	cancel()
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	closed := make(chan struct{})
	// Ends when the session closes, which the deferred Close guarantees.
	go func() {
		_ = cs.Wait()
		close(closed)
	}()
	defer func() {
		_ = cs.Close()
		<-closed
	}()
	if err := s.refresh(ctx, cs); err != nil {
		return false, err
	}
	defer s.detach()
	s.log.Info("mcp server connected")
	for {
		select {
		case <-ctx.Done():
			return true, nil
		case <-closed:
			return true, nil
		case <-s.changed:
			if err := s.refresh(ctx, cs); err != nil {
				s.log.Warn("mcp server's changed tool list could not be read", "err", err)
			}
		}
	}
}

// poke asks the connection to list the tools again. Off the notification's
// own goroutine: a request sent from inside a handler may wait on itself.
func (s *server) poke() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// detach marks the server disconnected, so a call answers at once.
func (s *server) detach() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cs = nil
}

// refresh lists the server's tools and makes cs the session calls use,
// then says how the list now differs from what the model was offered.
func (s *server) refresh(ctx context.Context, cs *sdk.ClientSession) error {
	lctx, cancel := bounded(ctx, s.opts.Timers, requestTimeout)
	defer cancel()
	tools := []*sdk.Tool{}
	for t, err := range cs.Tools(lctx, nil) {
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}
		tools = append(tools, t)
	}
	live := make(map[string]*sdk.Tool, len(tools))
	for _, t := range tools {
		live[t.Name] = t
	}
	s.mu.Lock()
	s.cs, s.live = cs, live
	frozen, offered := s.frozen, s.offered
	if !frozen {
		s.first = tools
	}
	s.mu.Unlock()
	// After cs is set: a provider started on this listing can call at once.
	s.listedOnce.Do(func() { close(s.listed) })
	if frozen {
		s.reconcile(offered, live)
	}
	return nil
}

// reconcile logs what changed since the model's tools were fixed. Nothing
// offered changes: a call to a tool the server dropped answers so.
func (s *server) reconcile(offered map[string]string, live map[string]*sdk.Tool) {
	var gone, added []string
	theirs := map[string]bool{}
	for _, tool := range offered {
		theirs[tool] = true
		if live[tool] == nil {
			gone = append(gone, tool)
		}
	}
	for tool := range live {
		if !theirs[tool] {
			added = append(added, tool)
		}
	}
	sort.Strings(gone)
	sort.Strings(added)
	if len(gone) > 0 {
		s.log.Warn("mcp server no longer lists tools the model is offered; calling them answers mcp_tool_gone", "tools", gone)
	}
	if len(added) > 0 {
		s.log.Info("mcp server lists tools the model is not offered; a restart offers those the configuration allows", "tools", added)
	}
}

// current is the live session, and whether it lists tool.
func (s *server) current(tool string) (*sdk.ClientSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cs, s.live[tool] != nil
}

// bounded is ctx, cancelled after d on t.
func bounded(ctx context.Context, t Timers, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-t.After(d):
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// call runs one offered tool on its server. Failures are errors, which the
// session records as results the model reasons about (SPEC §7).
type call struct {
	s *server
	// tool is the server's own name for it.
	tool string
}

// Invoke sends the arguments as the model wrote them, less the
// orchestrator's, which the session has already taken off.
func (c call) Invoke(ctx context.Context, args string) (string, error) {
	cs, listed := c.s.current(c.tool)
	switch {
	case cs == nil:
		return "", fmt.Errorf("mcp_unavailable: the %s server is not connected", c.s.cfg.Name)
	case !listed:
		return "", fmt.Errorf("mcp_tool_gone: the %s server no longer lists %s", c.s.cfg.Name, c.tool)
	}
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	if !json.Valid([]byte(args)) {
		return "", errors.New("bad_arguments: not JSON")
	}
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: c.tool, Arguments: json.RawMessage(args)})
	if ctx.Err() != nil {
		// The SDK has told the server the call is cancelled.
		return "", ctx.Err()
	}
	var rpc *jsonrpc.Error
	switch {
	case errors.As(err, &rpc):
		return "", fmt.Errorf("mcp_error: the %s server refused %s: %s", c.s.cfg.Name, c.tool, rpc.Message)
	case err != nil:
		return "", fmt.Errorf("mcp_unavailable: the %s server stopped answering: %w", c.s.cfg.Name, err)
	}
	return result(res)
}

// result is what the model is shown of a call's result: its structured
// content when the server gave one, else its text, bounded. Images, audio
// and the like are named, not inlined: the model reads text.
func result(r *sdk.CallToolResult) (string, error) {
	var texts, omitted []string
	for _, c := range r.Content {
		switch c := c.(type) {
		case *sdk.TextContent:
			texts = append(texts, c.Text)
		case *sdk.EmbeddedResource:
			if c.Resource != nil && c.Resource.Text != "" {
				texts = append(texts, c.Resource.Text)
			} else if c.Resource != nil {
				omitted = append(omitted, "resource "+c.Resource.URI)
			}
		case *sdk.ImageContent:
			omitted = append(omitted, "image "+c.MIMEType)
		case *sdk.AudioContent:
			omitted = append(omitted, "audio "+c.MIMEType)
		case *sdk.ResourceLink:
			omitted = append(omitted, "resource "+c.URI)
		default:
			omitted = append(omitted, fmt.Sprintf("%T", c))
		}
	}
	text := strings.Join(texts, "\n")
	if r.IsError {
		if text == "" {
			text = "the tool reported an error and said nothing more"
		}
		return "", fmt.Errorf("tool_error: %s", bound(text))
	}
	if r.StructuredContent != nil {
		if b, err := json.Marshal(r.StructuredContent); err == nil && len(b) <= maxResult {
			return string(b), nil
		}
	}
	out := struct {
		Text      string   `json:"text"`
		Truncated bool     `json:"truncated,omitempty"`
		Omitted   []string `json:"omitted,omitempty"`
	}{Text: bound(text), Truncated: len(text) > maxResult, Omitted: omitted}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return string(b), nil
}

// bound cuts s to maxResult bytes on a rune boundary.
func bound(s string) string {
	if len(s) <= maxResult {
		return s
	}
	cut := maxResult
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}
