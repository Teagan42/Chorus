//go:build models

// Service tier: needs a reachable Home Assistant. Run with `task test:models`,
// pointing it at one:
//
//	go test -tags=models ./internal/hass/ -hass-url http://127.0.0.1:8123 -hass-token "$HASS_TOKEN"
//
// Both are flags with no default so no household address or token lives in
// the repo. Without them these skip.
//
// What is tested here is the half the hermetic tier cannot reach: that HA
// still answers in the shapes doubles_test.go was copied from, and what a
// call costs. The shapes were recorded against Home Assistant 2026.10.0 stood
// up like this, which is enough to rerun this file:
//
//	uv venv --python 3.14 venv
//	VIRTUAL_ENV=$PWD/venv uv pip install homeassistant==2026.10.0 pip numpy av PyTurboJPEG
//	# configuration.yaml: `api:` and `demo:`, and no `http:` block
//	venv/bin/hass -c ./config
//	POST /api/onboarding/users {client_id,name,username,password,language} -> {auth_code}
//	POST /auth/token  form: grant_type=authorization_code&code=<auth_code>&client_id=<client_id>
//	POST /api/onboarding/core_config, /analytics, /integration  (bearer: the access_token)
//	ws /api/websocket: {type:auth,access_token} then
//	  {id:1,type:"auth/long_lived_access_token",client_name,lifespan:365} -> result is the token
//
// `demo:` provides the light these act on. Its camera platform imports
// `stream`, whose requirements HA does not install on its own, hence the
// three extra packages. No `http:` block because 2026.10 stages one as a
// five-minute trial that exits HA with code 100 unless promoted over the
// websocket (`http/config/promote`); the default binding is enough here.
//
// The bad-token cases are failed logins to HA's IP ban: three per run. Its
// threshold is off by default; a household instance with one set will count
// them.
//
// Area targeting is not covered here, on purpose. /api/states carries no
// area and REST cannot create one; checked by hand over the websocket
// (config/area_registry/create, then config/device_registry/update with the
// area_id, since a demo entity has no name of its own), a top-level area_id
// reaches the light and the changed list names it, as ha_call_service assumes.
package hass_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/hass"
	"github.com/teaganglenn/chorus/internal/session"
)

var (
	endpoint    = flag.String("hass-url", "", "Home Assistant base URL; skips when empty")
	accessToken = flag.String("hass-token", "", "Home Assistant long-lived access token; skips when empty")
)

// callBudget is well above what a light costs: a timeout here is a dead
// instance, not a slow one.
const callBudget = 10 * time.Second

func toolsAt(t *testing.T) map[string]session.Tool {
	t.Helper()
	if *endpoint == "" || *accessToken == "" {
		t.Skip("no -hass-url or -hass-token")
	}
	c, err := hass.New(hass.Config{BaseURL: *endpoint, Token: *accessToken})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return hass.Tools(c)
}

// invoke runs one tool and logs what it cost. Logged every run: ADR-0027
// declared the tool fast on the strength of HA answering a light in well
// under a second, and a regression there is invisible in a pass/fail.
func invoke(t *testing.T, tools map[string]session.Tool, name, args string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callBudget)
	defer cancel()
	start := time.Now()
	out, err := tools[name].Invoke(ctx, args)
	took := time.Since(start).Round(time.Millisecond)
	if err != nil {
		t.Logf("%s %s -> error in %v: %v", name, args, took, err)
	} else {
		t.Logf("%s %s -> %s in %v", name, args, out, took)
	}
	return out, err
}

func mustInvoke(t *testing.T, tools map[string]session.Tool, name, args string) string {
	t.Helper()
	out, err := invoke(t, tools, name, args)
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	return out
}

type found struct {
	Entities []struct {
		EntityID string `json:"entity_id"`
		Name     string `json:"name"`
		State    string `json:"state"`
	} `json:"entities"`
	Truncated bool `json:"truncated"`
}

// demoLight finds the demo integration's kitchen light the way the model
// would: by domain and a room name, never by a guessed id.
func demoLight(t *testing.T, tools map[string]session.Tool) (id, name string) {
	t.Helper()
	var got found
	if err := json.Unmarshal([]byte(mustInvoke(t, tools, "ha_find_entities", `{"domain":"light","name":"kitchen"}`)), &got); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if len(got.Entities) == 0 {
		t.Fatal("no kitchen light; is `demo:` in configuration.yaml?")
	}
	e := got.Entities[0]
	if !strings.HasPrefix(e.EntityID, "light.") || e.Name == "" || e.State == "" {
		t.Fatalf("entity = %+v, want a light with a name and a state", e)
	}
	return e.EntityID, e.Name
}

func TestFindEntitiesFindsADemoLight(t *testing.T) {
	tools := toolsAt(t)
	id, name := demoLight(t, tools)
	if !strings.Contains(strings.ToLower(name), "kitchen") {
		t.Errorf("found %s named %q, want the kitchen light", id, name)
	}

	// The same light is reachable without a domain, and every hit is a light.
	var got found
	if err := json.Unmarshal([]byte(mustInvoke(t, tools, "ha_find_entities", `{"domain":"light"}`)), &got); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	seen := false
	for _, e := range got.Entities {
		if !strings.HasPrefix(e.EntityID, "light.") {
			t.Errorf("%s is not a light", e.EntityID)
		}
		seen = seen || e.EntityID == id
	}
	if !seen && !got.Truncated {
		t.Errorf("%s is missing from the unfiltered light page", id)
	}
}

// What the model reads back is scalars only: HA's light carries list and
// null attributes, and none of them reaches the result.
func TestGetStateReadsTheDemoLight(t *testing.T) {
	tools := toolsAt(t)
	id, name := demoLight(t, tools)

	var got struct {
		EntityID    string         `json:"entity_id"`
		State       string         `json:"state"`
		Attributes  map[string]any `json:"attributes"`
		LastChanged string         `json:"last_changed"`
	}
	if err := json.Unmarshal([]byte(mustInvoke(t, tools, "ha_get_state", `{"entity_id":"`+id+`"}`)), &got); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if got.EntityID != id || (got.State != "on" && got.State != "off") || got.LastChanged == "" {
		t.Errorf("state = %+v, want %s on or off with a last_changed", got, id)
	}
	if got.Attributes["friendly_name"] != name {
		t.Errorf("friendly_name = %v, want %q", got.Attributes["friendly_name"], name)
	}
	for k, v := range got.Attributes {
		switch v.(type) {
		case string, float64, bool:
		default:
			t.Errorf("attribute %s = %v (%T) is not a scalar", k, v, v)
		}
	}
}

type changed struct {
	Changed []struct {
		EntityID string `json:"entity_id"`
		State    string `json:"state"`
	} `json:"changed"`
}

func parseChanged(t *testing.T, out string) changed {
	t.Helper()
	var got changed
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("result %s is not JSON: %v", out, err)
	}
	return got
}

// The changed list is the only thing the model has to know what the home
// did, so it has to name the light on every real transition and nothing on a
// call that changed nothing.
func TestCallServiceReportsWhatChanged(t *testing.T) {
	tools := toolsAt(t)
	id, _ := demoLight(t, tools)
	target := `"entity_id":"` + id + `"`

	// The light's starting state is whatever the last run left; this call
	// settles it without asserting on what it reports.
	mustInvoke(t, tools, "ha_call_service", `{"domain":"light","service":"turn_off",`+target+`}`)

	got := parseChanged(t, mustInvoke(t, tools, "ha_call_service",
		`{"domain":"light","service":"turn_on",`+target+`,"data":{"brightness_pct":50}}`))
	if len(got.Changed) != 1 || got.Changed[0].EntityID != id || got.Changed[0].State != "on" {
		t.Errorf("turn_on changed %+v, want %s on", got.Changed, id)
	}

	var st struct {
		State      string         `json:"state"`
		Attributes map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal([]byte(mustInvoke(t, tools, "ha_get_state", `{`+target+`}`)), &st); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	// 50% of HA's 0-255 scale, as HA rounds it.
	if st.State != "on" || st.Attributes["brightness"] != 128.0 {
		t.Errorf("after turn_on: state %s brightness %v, want on at 128", st.State, st.Attributes["brightness"])
	}

	got = parseChanged(t, mustInvoke(t, tools, "ha_call_service", `{"domain":"light","service":"turn_off",`+target+`}`))
	if len(got.Changed) != 1 || got.Changed[0].EntityID != id || got.Changed[0].State != "off" {
		t.Errorf("turn_off changed %+v, want %s off", got.Changed, id)
	}

	if out := mustInvoke(t, tools, "ha_call_service", `{"domain":"light","service":"turn_off",`+target+`}`); out != `{"changed":[]}` {
		t.Errorf("a second turn_off reported %s, want nothing changed", out)
	}
}

// Each failure is the error the hermetic tier promises, so the model hears
// the same thing from a real home as from the fake.
//
// verifies SPEC §7
func TestHAFailuresAreTheErrorsTheHermeticTierPromises(t *testing.T) {
	tools := toolsAt(t)
	id, _ := demoLight(t, tools)

	t.Run("unknown entity", func(t *testing.T) {
		_, err := invoke(t, tools, "ha_get_state", `{"entity_id":"light.nope"}`)
		if !errors.Is(err, hass.ErrUnknownEntity) || !strings.Contains(err.Error(), "unknown entity light.nope") {
			t.Errorf("err = %v, want ErrUnknownEntity naming light.nope", err)
		}
	})
	t.Run("unknown service", func(t *testing.T) {
		_, err := invoke(t, tools, "ha_call_service", `{"domain":"light","service":"explode","entity_id":"`+id+`"}`)
		if !errors.Is(err, hass.ErrUnknownService) || !strings.Contains(err.Error(), "unknown service light.explode") {
			t.Errorf("err = %v, want ErrUnknownService naming light.explode", err)
		}
	})
	t.Run("unknown domain", func(t *testing.T) {
		_, err := invoke(t, tools, "ha_call_service", `{"domain":"nope","service":"turn_on","entity_id":"`+id+`"}`)
		if !errors.Is(err, hass.ErrUnknownService) || !strings.Contains(err.Error(), "nope.turn_on") {
			t.Errorf("err = %v, want ErrUnknownService naming nope.turn_on", err)
		}
	})
	t.Run("rejected service data", func(t *testing.T) {
		_, err := invoke(t, tools, "ha_call_service", `{"domain":"light","service":"turn_on","entity_id":"`+id+`","data":{"brightness_pct":999}}`)
		if err == nil || errors.Is(err, hass.ErrUnknownService) || !strings.Contains(err.Error(), "rejected the service data") {
			t.Errorf("err = %v, want the data blamed", err)
		}
	})
	t.Run("explained service data", func(t *testing.T) {
		// The demo number entity is the one case HA explains in its own words.
		_, err := invoke(t, tools, "ha_call_service", `{"domain":"number","service":"set_value","entity_id":"number.volume","data":{"value":99999}}`)
		if err == nil || !strings.Contains(err.Error(), "outside valid range") {
			t.Errorf("err = %v, want HA's explanation quoted", err)
		}
	})
	// A target HA does not know is a call that landed on nothing, which the
	// declaration says is an empty list, not an error.
	t.Run("unknown target changes nothing", func(t *testing.T) {
		for _, args := range []string{
			`{"domain":"light","service":"turn_on","entity_id":"light.nope"}`,
			`{"domain":"light","service":"turn_on","area_id":"no_such_area"}`,
		} {
			if out := mustInvoke(t, tools, "ha_call_service", args); out != `{"changed":[]}` {
				t.Errorf("%s -> %s, want nothing changed", args, out)
			}
		}
	})
}

// verifies SPEC §7
func TestAWrongTokenIsErrUnauthorized(t *testing.T) {
	if *endpoint == "" || *accessToken == "" {
		t.Skip("no -hass-url or -hass-token")
	}
	c, err := hass.New(hass.Config{BaseURL: *endpoint, Token: "not-a-token"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = invoke(t, hass.Tools(c), "ha_get_state", `{"entity_id":"light.kitchen_lights"}`)
	if !errors.Is(err, hass.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), "not-a-token") {
		t.Errorf("err quotes the token: %v", err)
	}
}

// The wire shapes doubles_test.go is built on, read raw, so an HA release
// that starts explaining a 400, moves an unknown entity off 404, or answers
// a bad token with 403 fails here instead of silently changing what the
// model is told.
func TestTheRESTContractStillHolds(t *testing.T) {
	if *endpoint == "" || *accessToken == "" {
		t.Skip("no -hass-url or -hass-token")
	}
	raw := func(method, path, body, tok string) (int, string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), callBudget)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(*endpoint, "/")+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return resp.StatusCode, resp.Header.Get("Content-Type"), strings.TrimSpace(string(b))
	}

	_, _, cfg := raw(http.MethodGet, "/api/config", "", *accessToken)
	var version struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal([]byte(cfg), &version)
	t.Logf("home assistant %s at %s", version.Version, *endpoint)

	if status, ctype, body := raw(http.MethodPost, "/api/services/light/explode", `{"entity_id":"light.kitchen_lights"}`, *accessToken); status != http.StatusBadRequest || strings.HasPrefix(ctype, "application/json") || body != badRequest {
		t.Errorf("unknown service: %d %s %q; the fake expects a bare %q", status, ctype, body, badRequest)
	}
	if status, _, body := raw(http.MethodPost, "/api/services/light/turn_on", `{"entity_id":"light.kitchen_lights","brightness_pct":999}`, *accessToken); status != http.StatusBadRequest || body != badRequest {
		t.Errorf("rejected data: %d %q; the fake expects the same bare page as an unknown service", status, body)
	}
	if status, ctype, body := raw(http.MethodGet, "/api/states/light.nope", "", *accessToken); status != http.StatusNotFound || !strings.HasPrefix(ctype, "application/json") || body != entityNotFound {
		t.Errorf("unknown entity: %d %s %q; the fake expects %q", status, ctype, body, entityNotFound)
	}
	if status, _, body := raw(http.MethodGet, "/api/states", "", "not-a-token"); status != http.StatusUnauthorized || body != unauthorized {
		t.Errorf("bad token: %d %q; the fake expects 401 %q", status, body, unauthorized)
	}
	if status, _, body := raw(http.MethodPost, "/api/services/light/turn_off", `{"entity_id":"light.nope"}`, *accessToken); status != http.StatusOK || !strings.HasPrefix(body, "[") {
		t.Errorf("service call: %d %q; the client decodes a list of states", status, body)
	}
}

// The demo integration's garage door is classed "garage" and its windows
// are classed nothing, which is what the hermetic tier's bodies assume. The
// garage is read, never opened: a household instance's garage is real.
//
// verifies SPEC §6
func TestTheDemoGarageSaysItIsAGarage(t *testing.T) {
	tools := toolsAt(t)
	c, ok := tools["ha_call_service"].(session.Classifier)
	if !ok {
		t.Fatal("ha_call_service cannot say what a call acts on")
	}
	ctx, cancel := context.WithTimeout(context.Background(), callBudget)
	defer cancel()
	for entity, want := range map[string][]string{
		"cover.garage_door":        {"garage"},
		"cover.living_room_window": nil,
	} {
		start := time.Now()
		got, err := c.Classify(ctx, `{"domain":"cover","service":"open_cover","entity_id":"`+entity+`"}`)
		t.Logf("classify %s -> %q, %v in %v", entity, got, err, time.Since(start).Round(time.Millisecond))
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("Classify(%s) = %q, %v; want %q", entity, got, err, want)
		}
	}
	if got, err := c.Classify(ctx, `{"domain":"cover","service":"open_cover","entity_id":"cover.nope"}`); err == nil {
		t.Errorf("Classify(cover.nope) = %q, want an error", got)
	}
}
