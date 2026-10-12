package mcp_test

import (
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/provider/mcp"
)

// env is the daemon's environment, holding the weather server's token.
func env(name string) string {
	return map[string]string{"WEATHER_MCP_TOKEN": "wx-test-token"}[name]
}

// The example configuration loads as written, its token read from the
// environment, so the file the household copies is one chorusd accepts.
//
// verifies SPEC §6, §13
func TestTheExampleConfigurationLoads(t *testing.T) {
	cfg, err := mcp.Load("../../../mcp.example.yaml", env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DiscoveryTimeout != 10*time.Second || len(cfg.Servers) != 2 {
		t.Fatalf("config = %+v, want two servers and a ten second window", cfg)
	}
	notes, weather := cfg.Servers[0], cfg.Servers[1]
	if notes.Name != "notes" || weather.Name != "weather" {
		t.Errorf("servers = %s, %s, want them in name order", notes.Name, weather.Name)
	}
	if got := weather.Headers["Authorization"]; got != "Bearer wx-test-token" {
		t.Errorf("Authorization = %q, want the token from the environment", got)
	}
	if want := []string{"/usr/local/bin/notes-mcp", "--stdio"}; !reflect.DeepEqual(notes.Command, want) {
		t.Errorf("notes command = %q, want %q", notes.Command, want)
	}
}

// Without the token in the environment, the example does not load with an
// empty secret in its place.
//
// verifies SPEC §13
func TestAnUnsetSecretStopsStartup(t *testing.T) {
	_, err := mcp.Load("../../../mcp.example.yaml", func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "WEATHER_MCP_TOKEN not set") {
		t.Errorf("error %v, want it to name the unset token", err)
	}
}

// Each mistake a household could make in the file stops chorusd at startup
// with the reason, rather than offering tools under a policy nobody chose.
//
// verifies SPEC §6, §13
func TestAConfigurationMistakeStopsStartup(t *testing.T) {
	cases := []struct {
		why, yaml, want string
	}{
		{"no servers", "servers: {}", "no servers"},
		{"a misspelt field", "servers:\n  weather:\n    url: http://w.lan/mcp\n    tools:\n      get_forecast:\n        requires_confirmaton: false\n", "requires_confirmaton"},
		{"an underscore in a server's name", "servers:\n  home_weather:\n    url: http://w.lan/mcp\n", "name"},
		{"a server with neither", "servers:\n  weather: {}\n", "set command (stdio) or url"},
		{"a server with both", "servers:\n  weather:\n    url: http://w.lan/mcp\n    command: [wx]\n", "not both"},
		{"a url that is not http", "servers:\n  weather:\n    url: ftp://w.lan/mcp\n", "not an http"},
		{"headers on a command", "servers:\n  weather:\n    command: [wx]\n    headers: {X-Key: k}\n", "headers"},
		{"env on a url", "servers:\n  weather:\n    url: http://w.lan/mcp\n    env: {KEY: k}\n", "env"},
		{"an unknown scope", "servers:\n  weather:\n    url: http://w.lan/mcp\n    policy: {scope: family}\n", "scope"},
		{"uninterruptible without a yes", "servers:\n  weather:\n    url: http://w.lan/mcp\n    tools:\n      reboot: {on_interrupt: uninterruptible, requires_confirmation: false}\n", "tools.reboot: an uninterruptible tool must require confirmation"},
		{"slow with a short timeout", "servers:\n  weather:\n    url: http://w.lan/mcp\n    policy: {latency: slow}\n", "slow"},
		{"a latency that is neither", "servers:\n  weather:\n    url: http://w.lan/mcp\n    policy: {latency: quick}\n", "latency"},
		{"a timeout past two minutes", "servers:\n  weather:\n    url: http://w.lan/mcp\n    policy: {timeout_ms: 600000}\n", "timeout"},
		{"a guest fallback on a household tool", "servers:\n  weather:\n    url: http://w.lan/mcp\n    policy: {scope: household, unknown_speaker: guest_fallback}\n", "unknown_speaker"},
		{"a negative window", "discovery_timeout_ms: -1\nservers:\n  weather:\n    url: http://w.lan/mcp\n", "negative"},
	}
	for _, c := range cases {
		_, err := mcp.Parse([]byte(c.yaml), env)
		if err == nil {
			t.Errorf("%s: accepted", c.why)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q, want it to mention %q", c.why, err, c.want)
		}
	}
}

// A file that is not there names itself, as devices.yaml does.
//
// verifies SPEC §13
func TestAMissingConfigurationNamesItself(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	_, err := mcp.Load(path, env)
	if err == nil || !strings.Contains(err.Error(), path) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error %v, want it to name %s as missing", err, path)
	}
}
