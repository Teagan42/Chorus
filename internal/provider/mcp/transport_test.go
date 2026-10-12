package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teagan42/chorus/internal/provider/mcp"
)

// serveEnv makes this test binary an MCP server on its stdin and stdout,
// so the stdio transport is tested against a real child process.
const serveEnv = "CHORUS_MCP_TEST_SERVE"

func TestMain(m *testing.M) {
	if os.Getenv(serveEnv) == "notes" {
		serveNotes()
		return
	}
	os.Exit(m.Run())
}

// serveNotes is the household's notes server: it reads back the shopping
// list it was given in its environment, and tells what else it was given.
func serveNotes() {
	srv := sdk.NewServer(&sdk.Implementation{Name: "notes", Version: "1.0.0"}, nil)
	srv.AddTool(&sdk.Tool{
		Name: "read_shopping_list", Description: "Read the shopping list.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var names []string
		for _, kv := range os.Environ() {
			names = append(names, strings.SplitN(kv, "=", 2)[0])
		}
		return &sdk.CallToolResult{
			Content:           []sdk.Content{&sdk.TextContent{Text: os.Getenv("NOTES_LIST")}},
			StructuredContent: map[string]any{"list": os.Getenv("NOTES_LIST"), "env": names},
		}, nil
	})
	if err := srv.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "notes:", err)
		os.Exit(1)
	}
}

// chorusd runs the notes server as a command and speaks MCP over its stdin
// and stdout. The child gets PATH, HOME and its configured env, and none of
// chorusd's credentials.
//
// verifies SPEC §6
func TestACommandServerRunsWithItsOwnEnvironment(t *testing.T) {
	t.Setenv("HASS_TOKEN", "ha-test-token")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := mcp.Config{Servers: []mcp.Server{{
		Name: "notes", Command: []string{exe},
		Env: map[string]string{serveEnv: "notes", "NOTES_LIST": "oat milk, eggs"},
	}}, DiscoveryTimeout: mcp.DefaultDiscoveryTimeout}
	p, err := mcp.Start(context.Background(), cfg, mcp.Options{Timers: &clock{}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer p.Close()
	tool, ok := p.Tools()["mcp_notes_read_shopping_list"]
	if !ok {
		t.Fatalf("tools = %v, want mcp_notes_read_shopping_list", p.Tools())
	}
	out, err := tool.Invoke(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	var got struct {
		List string   `json:"list"`
		Env  []string `json:"env"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("result %s: %v", out, err)
	}
	if got.List != "oat milk, eggs" {
		t.Errorf("list = %q, want the configured env's", got.List)
	}
	for _, name := range got.Env {
		switch name {
		case "PATH", "HOME", serveEnv, "NOTES_LIST":
		default:
			t.Errorf("the server was given %s from chorusd's environment", name)
		}
	}
}

// chorusd speaks Streamable HTTP to the weather server, sending the
// configured Authorization header with every request.
//
// verifies SPEC §6
func TestAURLServerIsSentItsHeaders(t *testing.T) {
	srv := weather().srv
	var (
		mu    sync.Mutex
		auths []string
	)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		mcpHandler.ServeHTTP(w, r)
	}))
	defer ts.Close()

	cfg, err := mcp.Parse([]byte("servers:\n  weather:\n    url: "+ts.URL+"\n    headers:\n      Authorization: Bearer ${WEATHER_MCP_TOKEN}\n"), env)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, err := mcp.Start(context.Background(), cfg, mcp.Options{Timers: &clock{}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	out, err := p.Tools()["mcp_weather_get_forecast"].Invoke(context.Background(), `{"city":"Porto"}`)
	p.Close()
	if err != nil || out != `{"text":"Sunny and 21°C in Porto."}` {
		t.Errorf("forecast = %s, %v", out, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) == 0 {
		t.Fatal("the server saw no requests")
	}
	for _, a := range auths {
		if a != "Bearer wx-test-token" {
			t.Errorf("a request carried Authorization %q", a)
		}
	}
}
