package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teagan42/chorus/internal/provider/mcp"
)

// patience bounds a wait that only expires when the implementation is wrong.
const patience = 5 * time.Second

// await spins until cond holds.
func await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ------------------------------------------------------------------ clock

// clock is a virtual timer source: nothing fires until the test advances it.
type clock struct {
	mu     sync.Mutex
	now    time.Duration
	timers []*vtimer
}

type vtimer struct {
	deadline, waited time.Duration
	ch               chan time.Time
}

func (c *clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &vtimer{deadline: c.now + d, waited: d, ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.ch
}

// advance moves the clock and fires every timer that came due.
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now += d
	var due, live []*vtimer
	for _, t := range c.timers {
		if t.deadline > c.now {
			live = append(live, t)
			continue
		}
		due = append(due, t)
	}
	c.timers = live
	c.mu.Unlock()
	for _, t := range due {
		t.ch <- time.Time{}
	}
}

// awaitWait blocks until a timer of exactly d is armed.
func (c *clock) awaitWait(t *testing.T, d time.Duration) {
	t.Helper()
	await(t, "a "+d.String()+" timer", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, tm := range c.timers {
			if tm.waited == d {
				return true
			}
		}
		return false
	})
}

// ------------------------------------------------------------------ logs

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *logBuffer) await(t *testing.T, substr string) {
	t.Helper()
	await(t, "the log to say "+substr, func() bool { return strings.Contains(l.String(), substr) })
}

func (l *logBuffer) logger() *slog.Logger { return slog.New(slog.NewTextHandler(l, nil)) }

// ---------------------------------------------------------------- servers

// house is an MCP server the household runs, in process: each dial is a
// fresh session over an in-memory transport, as a reconnect would be.
type house struct {
	srv *sdk.Server

	mu       sync.Mutex
	refusing bool
	dials    int
	// wires is each session's server end, which a crash cuts.
	wires  []net.Conn
	called []string
	// listenGate, while set, holds each subscriptions/listen until closed.
	listenGate chan struct{}
}

// newHouse declares its tools: the SDK otherwise infers the capability from
// the list, and refuses a subscription taken up while the list is empty.
func newHouse(name string) *house {
	return houseWith(name, &sdk.ServerOptions{
		Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{ListChanged: true}},
	})
}

// houseWith is a house on the SDK server options given.
func houseWith(name string, opts *sdk.ServerOptions) *house {
	h := &house{srv: sdk.NewServer(&sdk.Implementation{Name: name, Version: "1.0.0"}, opts)}
	h.srv.AddReceivingMiddleware(h.gateListen)
	return h
}

// holdSubscription makes the server take up a subscription to its changes
// only once release is called, as a loaded server might.
func (h *house) holdSubscription() (release func()) {
	gate := make(chan struct{})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listenGate = gate
	return sync.OnceFunc(func() { close(gate) })
}

func (h *house) gateListen(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		h.mu.Lock()
		gate := h.listenGate
		h.mu.Unlock()
		if method == "subscriptions/listen" && gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return next(ctx, method, req)
	}
}

// tool adds a tool that answers with fn, recording each call's arguments.
func (h *house) tool(t *sdk.Tool, fn func(ctx context.Context, args map[string]any) (*sdk.CallToolResult, error)) {
	if t.InputSchema == nil {
		t.InputSchema = json.RawMessage(`{"type":"object"}`)
	}
	h.srv.AddTool(t, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, err
			}
		}
		h.mu.Lock()
		h.called = append(h.called, t.Name+" "+string(req.Params.Arguments))
		h.mu.Unlock()
		return fn(ctx, args)
	})
}

func (h *house) dial(mcp.Server) (sdk.Transport, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dials++
	if h.refusing {
		return nil, errors.New("dial tcp 10.0.0.5:8080: connect: connection refused")
	}
	client, server := net.Pipe()
	if _, err := h.srv.Connect(context.Background(), &sdk.IOTransport{Reader: server, Writer: server}, nil); err != nil {
		return nil, fmt.Errorf("serve: %w", err)
	}
	h.wires = append(h.wires, server)
	return &sdk.IOTransport{Reader: client, Writer: client}, nil
}

// refuse makes every dial fail, or succeed again.
func (h *house) refuse(on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refusing = on
}

// crash cuts every session's wire, as a process exiting would: nothing is
// said, and nothing in flight is answered.
func (h *house) crash() {
	h.mu.Lock()
	wires := h.wires
	h.wires = nil
	h.mu.Unlock()
	for _, w := range wires {
		_ = w.Close()
	}
}

func (h *house) dialCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dials
}

func (h *house) calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.called...)
}

// text is a result of one line of text.
func text(s string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}}
}

// weather is the household's weather server: one forecast tool.
func weather() *house {
	h := newHouse("weather")
	h.tool(&sdk.Tool{
		Name:        "get_forecast",
		Description: "Get the weather forecast for a city.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string","description":"City name, e.g. Lisbon."},"days":{"type":"integer","description":"How many days ahead."}},"required":["city"]}`),
	}, func(_ context.Context, args map[string]any) (*sdk.CallToolResult, error) {
		return text(fmt.Sprintf("Sunny and 21°C in %v.", args["city"])), nil
	})
	return h
}

// houses routes each server's dial to its double.
type houses map[string]*house

func (hs houses) dial(s mcp.Server) (sdk.Transport, error) {
	h, ok := hs[s.Name]
	if !ok {
		return nil, fmt.Errorf("no server %s", s.Name)
	}
	return h.dial(s)
}

// configured is a configuration naming each server, with policy from yaml
// appended under it when given.
func configured(t *testing.T, yaml string) mcp.Config {
	t.Helper()
	cfg, err := mcp.Parse([]byte(yaml), func(string) string { return "" })
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cfg
}

// start runs a provider over hs until the test ends.
func start(t *testing.T, cfg mcp.Config, hs houses, clk *clock, logs *logBuffer) *mcp.Provider {
	t.Helper()
	p, err := mcp.Start(context.Background(), cfg, mcp.Options{Timers: clk, Log: logs.logger(), Dial: hs.dial})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}
