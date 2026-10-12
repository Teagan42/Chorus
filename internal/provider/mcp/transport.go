package mcp

import (
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// inherited is all of chorusd's environment a command is given: enough to
// find a program and a home directory, and none of chorusd's credentials.
var inherited = []string{"PATH", "HOME"}

// Transport opens a server as its configuration says: a fresh process over
// stdio, or a Streamable HTTP endpoint sent its headers.
func Transport(s Server) (sdk.Transport, error) {
	if s.URL != "" {
		return &sdk.StreamableClientTransport{
			Endpoint: s.URL,
			// No client timeout: the server's event stream is long-lived,
			// and each request is bounded by its context.
			HTTPClient: &http.Client{Transport: headers{set: s.Headers, next: http.DefaultTransport}},
		}, nil
	}
	cmd := exec.Command(s.Command[0], s.Command[1:]...)
	cmd.Env = environ(os.Getenv, s.Env)
	// A server's own complaints are the operator's to read, beside chorusd's.
	cmd.Stderr = os.Stderr
	return &sdk.CommandTransport{Command: cmd}, nil
}

// environ is a command's environment: the inherited names that are set,
// then env, which wins.
func environ(getenv func(string) string, env map[string]string) []string {
	out := []string{}
	for _, name := range inherited {
		if _, set := env[name]; !set && getenv(name) != "" {
			out = append(out, name+"="+getenv(name))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(env)) {
		out = append(out, name+"="+env[name])
	}
	return out
}

// headers sends a server's configured headers with every request.
type headers struct {
	set  map[string]string
	next http.RoundTripper
}

func (h headers) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.set {
		r.Header.Set(k, v)
	}
	return h.next.RoundTrip(r)
}
