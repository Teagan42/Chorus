// Package hass is the Home Assistant adapter of SPEC §6: the declared ha_*
// tools, backed by HA's REST API. It is the one real tool of SPEC §14 item 5,
// so the surface is what speak-while-tooling needs and no more (ADR-0027).
//
// REST rather than the websocket API because every call here is one request
// with one answer. The websocket is where the area and entity registries
// live, and the day areas need more than an id passed through is the day it
// earns its connection lifecycle.
package hass

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds one request. Longer than the registry's default tool
// timeout on purpose: the registry's timeout is the one the model is told
// about, so it should be the one that fires (SPEC §7).
const DefaultTimeout = 15 * time.Second

// errBody bounds how much of a failed response is quoted. HA's errors are a
// one-line JSON message; a proxy's are a whole HTML page.
const errBody = 1 << 10

// ErrUnauthorized is a rejected access token. The token itself is never part
// of an error or a log line.
var ErrUnauthorized = errors.New("home assistant rejected the access token")

// ErrUnknownEntity is HA's 404 for an entity id it has never heard of.
var ErrUnknownEntity = errors.New("unknown entity")

// ErrUnknownService is a domain.service HA does not have. HA answers it with
// the same bare 400 as rejected service data, so it is told apart by reading
// the service catalogue after the fact.
var ErrUnknownService = errors.New("unknown service")

// URLEnv and TokenEnv name the environment a composition root reads, as
// .env.example documents them.
const (
	URLEnv   = "HASS_URL"
	TokenEnv = "HASS_TOKEN"
)

// FromEnv builds a Config from the environment. The lookup is injected so a
// test never reads the real one.
func FromEnv(getenv func(string) string) Config {
	return Config{BaseURL: getenv(URLEnv), Token: getenv(TokenEnv)}
}

// Config wires a Client. The token lives in the environment (.env.example),
// never in the repo (CONTRIBUTING §6).
type Config struct {
	// BaseURL is the HA root, e.g. http://homeassistant.local:8123.
	BaseURL string

	// Token is a long-lived access token from the user's HA profile.
	Token string

	// HTTP defaults to a client bounded by DefaultTimeout.
	HTTP *http.Client
}

// Client speaks HA's REST API. Every method takes the caller's context, which
// is how the registry's interrupt policy reaches the wire (SPEC §4.4).
type Client struct {
	cfg  Config
	base string
}

// New validates the wiring and applies defaults.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("hass: base url is required")
	}
	if cfg.Token == "" {
		return nil, errors.New("hass: access token is required")
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{cfg: cfg, base: strings.TrimSuffix(cfg.BaseURL, "/") + "/api"}, nil
}

// State is one entity as /api/states reports it.
type State struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged string         `json:"last_changed"`
}

// States lists every entity. Areas are not in here: HA keeps them in its
// registries, which only the websocket API exposes.
func (c *Client) States(ctx context.Context) ([]State, error) {
	var out []State
	if err := c.do(ctx, http.MethodGet, "/states", nil, &out); err != nil {
		return nil, fmt.Errorf("list states: %w", err)
	}
	return out, nil
}

// EntityState reads one entity. An id HA does not know is ErrUnknownEntity.
func (c *Client) EntityState(ctx context.Context, entityID string) (State, error) {
	var out State
	err := c.do(ctx, http.MethodGet, "/states/"+url.PathEscape(entityID), nil, &out)
	if errors.Is(err, errNotFound) {
		err = fmt.Errorf("%w %s", ErrUnknownEntity, entityID)
	}
	if err != nil {
		return State{}, fmt.Errorf("get state %s: %w", entityID, err)
	}
	return out, nil
}

// CallService invokes domain.service with data as the service data, which is
// where HA takes the target too: entity_id and area_id are top-level fields.
// It returns the states that changed while the call ran. A target HA does
// not know is not an error: the call lands on nothing and the list is empty.
func (c *Client) CallService(ctx context.Context, domain, service string, data map[string]any) ([]State, error) {
	var out []State
	path := "/services/" + url.PathEscape(domain) + "/" + url.PathEscape(service)
	err := c.do(ctx, http.MethodPost, path, data, &out)
	if errors.Is(err, errBadRequest) {
		err = c.explainBadRequest(ctx, domain, service)
	}
	if err != nil {
		return nil, fmt.Errorf("call %s.%s: %w", domain, service, err)
	}
	return out, nil
}

// explainBadRequest reads the catalogue to say which of the two things a
// bare 400 means. The read itself failing leaves the 400 unexplained rather
// than misattributed.
func (c *Client) explainBadRequest(ctx context.Context, domain, service string) error {
	known, err := c.hasService(ctx, domain, service)
	if err != nil {
		return fmt.Errorf("400 Bad Request, and the service catalogue could not say why: %w", err)
	}
	if !known {
		return fmt.Errorf("%w %s.%s", ErrUnknownService, domain, service)
	}
	return errors.New("home assistant rejected the service data")
}

// serviceDomain is one entry of /api/services, read only for its names.
type serviceDomain struct {
	Domain   string                     `json:"domain"`
	Services map[string]json.RawMessage `json:"services"`
}

func (c *Client) hasService(ctx context.Context, domain, service string) (bool, error) {
	var out []serviceDomain
	if err := c.do(ctx, http.MethodGet, "/services", nil, &out); err != nil {
		return false, fmt.Errorf("list services: %w", err)
	}
	for _, d := range out {
		if d.Domain == domain {
			_, ok := d.Services[service]
			return ok, nil
		}
	}
	return false, nil
}

// errNotFound and errBadRequest are the raw statuses, classified by the
// caller that knows what was asked.
var (
	errNotFound   = errors.New("not found")
	errBadRequest = errors.New("bad request")
)

func (c *Client) do(ctx context.Context, method, path string, body any, into any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusNotFound:
		return errNotFound
	case http.StatusBadRequest:
		// HA explains a validation error in a JSON message; an unknown
		// service and a schema failure both come back as aiohttp's bare page.
		msg, explained := message(resp.Body)
		if !explained {
			return errBadRequest
		}
		return fmt.Errorf("%s: %s", resp.Status, c.redact(msg))
	default:
		msg, _ := message(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, c.redact(msg))
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// message reads a failed response's explanation: HA's {"message": ...} when
// the body is one, else the bounded raw body, which a proxy may have written.
func message(r io.Reader) (string, bool) {
	raw, _ := io.ReadAll(io.LimitReader(r, errBody))
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &m) == nil && m.Message != "" {
		return m.Message, true
	}
	return strings.TrimSpace(string(raw)), false
}

// redact keeps the token out of an error even if a misconfigured proxy
// echoes the request headers back.
func (c *Client) redact(s string) string {
	return strings.ReplaceAll(s, c.cfg.Token, "[redacted]")
}
