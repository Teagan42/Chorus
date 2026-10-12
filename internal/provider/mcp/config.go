package mcp

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/teagan42/chorus/internal/registry"
)

// ConfigEnv names the household's MCP configuration file. Unset, chorusd
// talks to no MCP server.
const ConfigEnv = "CHORUS_MCP_CONFIG"

// DefaultDiscoveryTimeout is how long startup waits for every server to list
// its tools. Long enough for a container started beside chorusd to come up.
const DefaultDiscoveryTimeout = 10 * time.Second

// serverName keeps the namespace unambiguous: with no underscore in a
// server's name, mcp_<server>_<tool> splits one way only.
var serverName = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)

// File is the configuration as written.
type File struct {
	// DiscoveryTimeoutMS bounds startup's wait for every server's tools.
	// Zero is DefaultDiscoveryTimeout.
	DiscoveryTimeoutMS int `yaml:"discovery_timeout_ms"`

	Servers map[string]Server `yaml:"servers"`
}

// Server is one MCP server: a command chorusd runs and speaks to over its
// stdin and stdout, or a Streamable HTTP endpoint. Exactly one of the two.
type Server struct {
	// Name is its key under servers: the <server> of each tool's name.
	Name string `yaml:"-"`

	// Command is the program and its arguments. Run with only PATH, HOME
	// and Env in its environment: chorusd's own holds the journal's and
	// Home Assistant's credentials.
	Command []string          `yaml:"command"`
	Env     map[string]string `yaml:"env"`

	// URL is a Streamable HTTP endpoint, sent Headers on every request.
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`

	// Policy relaxes or tightens DefaultPolicy for every tool the server
	// lists; Tools does so for one, by the name the server gives it.
	Policy Policy            `yaml:"policy"`
	Tools  map[string]Policy `yaml:"tools"`
}

// Config is the file, read and checked.
type Config struct {
	Servers          []Server
	DiscoveryTimeout time.Duration
}

// Load reads and checks the configuration at path. ${NAME} in an env or
// header value is read from getenv, so a token stays in the environment;
// one that is unset is an error, not an empty secret.
func Load(path string, getenv func(string) string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	cfg, err := Parse(b, getenv)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse is Load for a file already read.
func Parse(b []byte, getenv func(string) string) (Config, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(b))
	// A misspelt requires_confirmation must not pass as the default silently.
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return Config{}, fmt.Errorf("parse: %w", err)
	}
	if len(f.Servers) == 0 {
		return Config{}, errors.New("no servers: leave " + ConfigEnv + " unset for none")
	}
	if f.DiscoveryTimeoutMS < 0 {
		return Config{}, fmt.Errorf("discovery_timeout_ms %d is negative", f.DiscoveryTimeoutMS)
	}
	cfg := Config{DiscoveryTimeout: time.Duration(f.DiscoveryTimeoutMS) * time.Millisecond}
	if cfg.DiscoveryTimeout == 0 {
		cfg.DiscoveryTimeout = DefaultDiscoveryTimeout
	}
	names := make([]string, 0, len(f.Servers))
	for name := range f.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := f.Servers[name]
		s.Name = name
		if err := s.check(getenv); err != nil {
			return Config{}, fmt.Errorf("server %s: %w", name, err)
		}
		cfg.Servers = append(cfg.Servers, s)
	}
	return cfg, nil
}

func (s *Server) check(getenv func(string) string) error {
	if !serverName.MatchString(s.Name) {
		return errors.New("name must be up to 16 lowercase letters and digits, starting with a letter: it is the <server> in mcp_<server>_<tool>")
	}
	switch {
	case len(s.Command) > 0 && s.URL != "":
		return errors.New("set command or url, not both")
	case len(s.Command) > 0:
		if s.Command[0] == "" {
			return errors.New("command names no program")
		}
		if len(s.Headers) > 0 {
			return errors.New("headers are sent to a url; a command takes env")
		}
	case s.URL != "":
		u, err := url.Parse(s.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("url %q is not an http or https URL", s.URL)
		}
		if len(s.Env) > 0 {
			return errors.New("env is given to a command; a url takes headers")
		}
	default:
		return errors.New("set command (stdio) or url (Streamable HTTP)")
	}
	var err error
	if s.Env, err = expand(s.Env, getenv); err != nil {
		return fmt.Errorf("env: %w", err)
	}
	if s.Headers, err = expand(s.Headers, getenv); err != nil {
		return fmt.Errorf("headers: %w", err)
	}
	if err := s.Policy.check(); err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	for _, name := range slices.Sorted(maps.Keys(s.Tools)) {
		if err := s.Tools[name].check(); err != nil {
			return fmt.Errorf("tools.%s: %w", name, err)
		}
	}
	// How the fields combine is the registry's rule; checked here, so a
	// policy no tool could run under fails at startup, not per tool.
	for _, name := range append([]string{""}, slices.Sorted(maps.Keys(s.Tools))...) {
		where := "policy"
		if name != "" {
			where = "tools." + name
		}
		p := DefaultPolicy().over(s.Policy).over(s.Tools[name])
		if _, err := registry.Provide(p.apply(registry.ToolSpec{Name: "mcp_" + s.Name + "_tool", Description: "A tool."})); err != nil {
			return fmt.Errorf("%s: %w", where, errors.Unwrap(err))
		}
	}
	return nil
}

// expand reads every ${NAME} in vs from getenv.
func expand(vs map[string]string, getenv func(string) string) (map[string]string, error) {
	if len(vs) == 0 {
		return vs, nil
	}
	out := make(map[string]string, len(vs))
	var missing []string
	for k, v := range vs {
		out[k] = os.Expand(v, func(name string) string {
			got := getenv(name)
			if got == "" {
				missing = append(missing, name)
			}
			return got
		})
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("%s not set in the environment", strings.Join(slices.Compact(missing), ", "))
	}
	return out, nil
}
