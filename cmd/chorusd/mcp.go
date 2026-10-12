package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"

	"github.com/teagan42/chorus/internal/provider/mcp"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

// provided is tools a provider declares at runtime rather than the build:
// the household's MCP servers' (ADR-0075).
type provided interface {
	Specs() map[string]registry.ToolSpec
	Tools() map[string]session.Tool
}

// startMCP connects to the servers CHORUS_MCP_CONFIG names, waiting out
// their discovery window, or returns nil when it names none.
func startMCP(ctx context.Context, cfg Config, getenv func(string) string, timers mcp.Timers, log *slog.Logger) (*mcp.Provider, error) {
	if cfg.MCPConfig == "" {
		return nil, nil
	}
	mc, err := mcp.Load(cfg.MCPConfig, getenv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", mcp.ConfigEnv, err)
	}
	return mcp.Start(ctx, mc, mcp.Options{Timers: timers, Log: log, Version: version})
}

// offerable is the tools the session enforces policy from and the tools the
// model is offered: the registry's, and extra's beside them. Both nil when
// there is no extra, which leaves each to its own default.
func offerable(extra provided) (enforced, offered map[string]registry.ToolSpec, err error) {
	if extra == nil {
		return nil, nil, nil
	}
	enforced, offered = maps.Clone(registry.Specs), registry.Offered()
	for name, spec := range extra.Specs() {
		if _, taken := enforced[name]; taken {
			return nil, nil, fmt.Errorf("provided tool %s has a declared tool's name", name)
		}
		enforced[name], offered[name] = spec, spec
	}
	return enforced, offered, nil
}
