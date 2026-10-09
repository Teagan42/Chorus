package main

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/teaganglenn/chorus/internal/hass"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

// homeAssistant answers the REST calls the ha_* tools make, in-process: the
// garage door is open this morning, and the front door unlocks when told to.
// It keeps the paths it was asked for and the bodies it was sent. answers
// overrides the reply to a "METHOD path" with a body HA answers 200.
type homeAssistant struct {
	answers map[string]string

	mu     sync.Mutex
	asked  []string
	bodies []string
}

func (h *homeAssistant) RoundTrip(r *http.Request) (*http.Response, error) {
	var sent []byte
	if r.Body != nil {
		sent, _ = io.ReadAll(r.Body)
	}
	h.mu.Lock()
	h.asked = append(h.asked, r.Method+" "+r.URL.Path)
	h.bodies = append(h.bodies, string(sent))
	h.mu.Unlock()
	body, status := `{"message":"Entity not found."}`, http.StatusNotFound
	answer, answered := h.answers[r.Method+" "+r.URL.Path]
	switch {
	case answered:
		body, status = answer, http.StatusOK
	case r.Method == http.MethodGet && r.URL.Path == "/api/states/cover.garage_door":
		body = `{"entity_id":"cover.garage_door","state":"open","attributes":{"friendly_name":"Garage Door","device_class":"garage","current_position":100},"last_changed":"2026-10-09T06:51:12+00:00"}`
		status = http.StatusOK
	case r.Method == http.MethodPost && r.URL.Path == "/api/services/lock/unlock":
		body = `[{"entity_id":"lock.front_door","state":"unlocked","attributes":{"friendly_name":"Front Door"}}]`
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func (h *homeAssistant) paths() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.asked...)
}

func (h *homeAssistant) sent() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.bodies...)
}

// Alan asks the kitchen satellite whether the garage is shut. The model says
// it is checking and reads the cover through the real Home Assistant tools;
// the daemon asks it again with the state, and the answer reaches the
// speaker in the same turn.
//
// verifies SPEC §4.1, §4.4
func TestAnAnswerFromHomeAssistantIsSpokenInTheSameTurn(t *testing.T) {
	const (
		checking = "Let me check."
		answer   = "No, the garage door is open."
	)
	ha := &homeAssistant{}
	client, err := hass.New(hass.Config{
		BaseURL: "http://homeassistant.invalid:8123", Token: "ha-test-token",
		HTTP: &http.Client{Transport: ha},
	})
	if err != nil {
		t.Fatalf("hass: %v", err)
	}
	eng := &scriptEngine{
		acts: []session.Action{
			session.SpeechDelta{CallID: "call_s1", Text: checking, Last: true},
			session.ToolCall{ID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`},
			session.TurnEnd{FinishReason: "stop", Completion: "{}"},
		},
		// A model that answers from what it was given, and only from that.
		answer: func(r journal.Entry) []session.Action {
			if r.Tool != "ha_get_state" || !strings.Contains(r.Result, `"state":"open"`) {
				return []session.Action{session.TurnEnd{FinishReason: "stop", Completion: "{}"}}
			}
			return []session.Action{
				session.SpeechDelta{CallID: "call_s2", Text: answer, Last: true},
				session.TurnEnd{FinishReason: "stop", Completion: "{}"},
			}
		},
	}
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = eng
		d.tools = hass.Tools(client)
	})
	dev := r.join(t, kitchenIP)
	garage := r.line("is the garage door closed", alan)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, garage)

	dev.AwaitTTS(t, 2*len(checking))
	dev.PlayAll(t)
	dev.AwaitTTS(t, 2*len(checking)+2*len(answer))
	dev.PlayAll(t)
	r.store.awaitKind(t, journal.KindSpeechSpoken, 2)

	var spoken []string
	for _, e := range r.store.ofKind(journal.KindSpeechSpoken) {
		spoken = append(spoken, e.Fields["text"])
	}
	if len(spoken) != 2 || spoken[0] != checking || spoken[1] != answer {
		t.Errorf("spoken = %q, want %q then %q", spoken, checking, answer)
	}
	if got := ha.paths(); len(got) != 1 || got[0] != "GET /api/states/cover.garage_door" {
		t.Errorf("home assistant was asked %q, want the garage door's state once", got)
	}

	asks := eng.heard()
	if len(asks) != 2 {
		t.Fatalf("model asked %d times, want 2", len(asks))
	}
	last := asks[1].Dialogue[len(asks[1].Dialogue)-1]
	if last.Kind != journal.EntryResult || last.CallID != "call_c1" || !strings.Contains(last.Result, "garage") {
		t.Errorf("follow-up ended on %+v, want the cover's state", last)
	}
	if n := len(r.store.ofKind(journal.KindModelCompleted)); n != 2 {
		t.Errorf("%d completions journalled, want 2", n)
	}
}
