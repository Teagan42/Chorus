package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/hass"
	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// alice is the second voice the kitchen knows, in the tests that enroll her.
var alice = axis(1)

// alanAndAlice enrolls both, so the kitchen can tell Alice's yes from Alan's.
func alanAndAlice(t *testing.T, emb identity.Embedder) *identity.Resolver {
	t.Helper()
	ids := identity.New(emb)
	for name, voice := range map[string][]float32{"alan": alan, "alice": alice} {
		if err := ids.Enroll(name, strings.ToUpper(name[:1])+name[1:], [][]float32{voice, voice, voice}); err != nil {
			t.Fatalf("enroll %s: %v", name, err)
		}
	}
	r, err := identity.NewResolver(emb, ids, identity.Thresholds{})
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	return r
}

// askerEngine unlocks the front door when asked, asks whenever the call is
// held, and calls again with the newest nonce whenever anyone says yes, each
// call under its own id.
func askerEngine(unlock, question, answer string) func(session.Input) []session.Action {
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	n := 1
	return func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		switch {
		case last.Kind == journal.EntryHeard && last.Text == "unlock the front door":
			return []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: unlock}, done}
		case last.Kind == journal.EntryResult && last.Outcome == "confirmation_required":
			return []session.Action{session.SpeechDelta{CallID: "call_s" + strconv.Itoa(len(in.Dialogue)), Text: question, Last: true}, done}
		case last.Kind == journal.EntryHeard && last.Text == "yes":
			var held struct {
				Nonce string `json:"nonce"`
			}
			for _, e := range in.Dialogue {
				if e.Outcome == "confirmation_required" {
					_ = json.Unmarshal([]byte(e.Result), &held)
				}
			}
			n++
			args := strings.TrimSuffix(unlock, "}") + `,"confirmation":"` + held.Nonce + `"}`
			return []session.Action{session.ToolCall{ID: "call_c" + strconv.Itoa(n), Tool: "ha_call_service", Args: args}, done}
		case last.Kind == journal.EntryResult && last.Outcome == "ok":
			return []session.Action{session.SpeechDelta{CallID: "call_s_done", Text: answer, Last: true}, done}
		}
		return []session.Action{done}
	}
}

// someoneElseSaysYes runs Alan's front door with another voice answering
// first: nothing reaches Home Assistant on that yes, the model is told why,
// and Alan's own yes then unlocks the door. It returns the log's utterances.
func someoneElseSaysYes(t *testing.T, other []float32, refusal string) []journal.Event {
	t.Helper()
	const (
		unlock   = `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`
		question = "Unlock the front door?"
		answer   = "Done, the front door is unlocked."
	)
	ha := &homeAssistant{}
	client, err := hass.New(hass.Config{
		BaseURL: "http://homeassistant.invalid:8123", Token: "ha-test-token",
		HTTP: &http.Client{Transport: ha},
	})
	if err != nil {
		t.Fatalf("hass: %v", err)
	}
	emb := newEmbedder()
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = &scriptEngine{decide: askerEngine(unlock, question, answer)}
		d.tools = hass.Tools(client)
		d.speakers = alanAndAlice(t, emb)
		d.household = []string{"alan", "alice"}
	})
	// The voices the test scripts are the ones the daemon's resolver hears.
	r.emb = emb
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("unlock the front door", alan))
	dev.AwaitTTS(t, 2*len(question))
	dev.PlayAll(t)
	r.spoken(t, "kitchen", 1)

	r.utter(t, dev, r.line("yes", other))
	dev.AwaitTTS(t, 4*len(question))
	dev.PlayAll(t)
	r.spoken(t, "kitchen", 2)
	if got := ha.paths(); len(got) != 0 {
		t.Fatalf("home assistant was asked %q on a yes that was not Alan's", got)
	}
	refused := r.store.ofKind(journal.KindConfirmationRequested)
	if len(refused) != 2 || refused[1].Fields["refused"] != refusal || refused[1].Fields["presented"] != refused[0].Fields["nonce"] {
		t.Fatalf("confirmation_requested = %v, want the first nonce refused as %s and a fresh one handed out", refused, refusal)
	}

	r.utter(t, dev, r.line("yes", alan))
	dev.AwaitTTS(t, 4*len(question)+2*len(answer))
	dev.PlayAll(t)
	r.store.awaitKind(t, journal.KindSpeechSpoken, 3)

	if got := ha.paths(); len(got) != 1 || got[0] != "POST /api/services/lock/unlock" {
		t.Fatalf("home assistant was asked %q, want the front door unlocked once, on Alan's yes", got)
	}
	if body := ha.sent()[0]; body != `{"entity_id":"lock.front_door"}` {
		t.Errorf("unlock body = %s, want only the front door: no nonce", body)
	}
	var spoken []string
	for _, e := range r.store.ofKind(journal.KindSpeechSpoken) {
		spoken = append(spoken, e.Fields["text"])
	}
	if want := []string{question, question, answer}; !slices.Equal(spoken, want) {
		t.Errorf("spoken = %q, want %q", spoken, want)
	}
	given := r.store.ofKind(journal.KindConfirmationGiven)
	if len(given) != 1 || given[0].Fields["nonce"] != refused[1].Fields["nonce"] || given[0].Fields["call_id"] != "call_c3" {
		t.Errorf("confirmation_given = %v, want call_c3 to redeem the fresh nonce", given)
	}
	return r.store.ofKind(journal.KindUtteranceTranscribed)
}

// Alan asks the kitchen satellite to unlock the front door, and Alice says
// yes from across the room. The question was Alan's, so her yes unlocks
// nothing and the model is told someone else answered; Alan's yes does.
//
// verifies SPEC §5, §6
func TestAlicesYesDoesNotUnlockWhatAlanAsked(t *testing.T) {
	heard := someoneElseSaysYes(t, alice, journal.RefusedWrongPerson)
	var who []string
	for _, e := range heard {
		who = append(who, e.Fields["speaker_id"]+"/"+e.Fields["speaker_match"])
	}
	if want := []string{"alan/identified", "alice/identified", "alan/identified"}; !slices.Equal(who, want) {
		t.Errorf("utterances = %q, want %q", who, want)
	}
}

// A dinner guest says yes to Alan's question. Their voice matched nobody the
// household enrolled, so their yes unlocks nothing; Alan's does.
//
// verifies SPEC §5, §6
func TestAGuestsYesDoesNotUnlockTheFrontDoor(t *testing.T) {
	heard := someoneElseSaysYes(t, stranger, journal.RefusedGuest)
	var who []string
	for _, e := range heard {
		who = append(who, e.Fields["speaker_id"]+"/"+e.Fields["speaker_match"])
	}
	if want := []string{"alan/identified", "/below_threshold", "alan/identified"}; !slices.Equal(who, want) {
		t.Errorf("utterances = %q, want %q", who, want)
	}
}

// Alan asks the kitchen satellite to unlock the front door. Nothing reaches
// Home Assistant until he has heard the question and said yes; then the
// daemon sends the unlock it would have sent without the gate, and says so.
//
// verifies SPEC §6
func TestTheFrontDoorUnlocksOnlyAfterAlanSaysYes(t *testing.T) {
	const (
		unlock   = `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`
		question = "Unlock the front door?"
		answer   = "Done, the front door is unlocked."
	)
	ha := &homeAssistant{}
	client, err := hass.New(hass.Config{
		BaseURL: "http://homeassistant.invalid:8123", Token: "ha-test-token",
		HTTP: &http.Client{Transport: ha},
	})
	if err != nil {
		t.Fatalf("hass: %v", err)
	}
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	eng := &scriptEngine{decide: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		switch {
		case last.Kind == journal.EntryHeard && last.Text == "unlock the front door":
			return []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: unlock}, done}
		case last.Kind == journal.EntryResult && last.Outcome == "confirmation_required":
			return []session.Action{session.SpeechDelta{CallID: "call_s1", Text: question, Last: true}, done}
		case last.Kind == journal.EntryHeard && last.Text == "yes":
			var held struct {
				Nonce string `json:"nonce"`
			}
			for _, e := range in.Dialogue {
				if e.Outcome == "confirmation_required" {
					_ = json.Unmarshal([]byte(e.Result), &held)
				}
			}
			args := strings.TrimSuffix(unlock, "}") + `,"confirmation":"` + held.Nonce + `"}`
			return []session.Action{session.ToolCall{ID: "call_c2", Tool: "ha_call_service", Args: args}, done}
		case last.Kind == journal.EntryResult && last.Outcome == "ok":
			return []session.Action{session.SpeechDelta{CallID: "call_s2", Text: answer, Last: true}, done}
		}
		return []session.Action{done}
	}}
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = eng
		d.tools = hass.Tools(client)
	})
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("unlock the front door", alan))
	dev.AwaitTTS(t, 2*len(question))
	dev.PlayAll(t)
	r.spoken(t, "kitchen", 1)
	if got := ha.paths(); len(got) != 0 {
		t.Fatalf("home assistant was asked %q before Alan said yes", got)
	}

	r.utter(t, dev, r.line("yes", alan))
	dev.AwaitTTS(t, 2*len(question)+2*len(answer))
	dev.PlayAll(t)
	r.store.awaitKind(t, journal.KindSpeechSpoken, 2)

	if got := ha.paths(); len(got) != 1 || got[0] != "POST /api/services/lock/unlock" {
		t.Fatalf("home assistant was asked %q, want the front door unlocked once", got)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(ha.sent()[0]), &body); err != nil {
		t.Fatalf("unlock body %q: %v", ha.sent()[0], err)
	}
	if len(body) != 1 || body["entity_id"] != "lock.front_door" {
		t.Errorf("unlock body = %v, want only the front door: no nonce", body)
	}

	var spoken []string
	for _, e := range r.store.ofKind(journal.KindSpeechSpoken) {
		spoken = append(spoken, e.Fields["text"])
	}
	if len(spoken) != 2 || spoken[0] != question || spoken[1] != answer {
		t.Errorf("spoken = %q, want %q then %q", spoken, question, answer)
	}
	requested := r.store.ofKind(journal.KindConfirmationRequested)
	given := r.store.ofKind(journal.KindConfirmationGiven)
	if len(requested) != 1 || len(given) != 1 || given[0].Fields["nonce"] != requested[0].Fields["nonce"] {
		t.Errorf("requested %d, given %d: want the one nonce handed out and redeemed", len(requested), len(given))
	}
}

// Home Assistant's covers as the daemon finds them this evening: the garage
// door shut, the living-room blinds down, and each opening when told to.
func eveningCovers() *homeAssistant {
	return &homeAssistant{answers: map[string]string{
		"GET /api/states/cover.garage_door":        `{"entity_id":"cover.garage_door","state":"closed","attributes":{"is_closed":true,"device_class":"garage","friendly_name":"Garage Door","supported_features":3},"last_changed":"2026-10-09T18:02:44+00:00"}`,
		"GET /api/states/cover.living_room_blinds": `{"entity_id":"cover.living_room_blinds","state":"closed","attributes":{"current_position":0,"device_class":"blind","friendly_name":"Living Room Blinds","supported_features":15},"last_changed":"2026-10-09T06:30:02+00:00"}`,
	}}
}

// coverModel opens the cover Alan names, asks when the call is held, calls
// again with the nonce once he says yes, and says so when it has run.
func coverModel(args, question, answer string) func(session.Input) []session.Action {
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	return func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		switch {
		case last.Kind == journal.EntryHeard && strings.HasPrefix(last.Text, "open the"):
			return []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: args}, done}
		case last.Kind == journal.EntryResult && last.Outcome == "confirmation_required":
			return []session.Action{session.SpeechDelta{CallID: "call_s1", Text: question, Last: true}, done}
		case last.Kind == journal.EntryHeard && last.Text == "yes":
			var held struct {
				Nonce string `json:"nonce"`
			}
			for _, e := range in.Dialogue {
				if e.Outcome == "confirmation_required" {
					_ = json.Unmarshal([]byte(e.Result), &held)
				}
			}
			again := strings.TrimSuffix(args, "}") + `,"confirmation":"` + held.Nonce + `"}`
			return []session.Action{session.ToolCall{ID: "call_c2", Tool: "ha_call_service", Args: again}, done}
		case last.Kind == journal.EntryResult && last.Outcome == "ok":
			return []session.Action{session.SpeechDelta{CallID: "call_s2", Text: answer, Last: true}, done}
		}
		return []session.Action{done}
	}
}

// Alan asks the kitchen satellite to open the garage door. It opens with the
// same cover.open_cover as the blinds, but Home Assistant classes it a
// garage, so nothing is opened until he has heard the question and said yes.
//
// verifies SPEC §6
func TestTheGarageDoorOpensOnlyAfterAlanSaysYes(t *testing.T) {
	const (
		open     = `{"domain":"cover","service":"open_cover","entity_id":"cover.garage_door"}`
		question = "Open the garage door?"
		answer   = "The garage door is opening."
	)
	ha := eveningCovers()
	ha.answers["POST /api/services/cover/open_cover"] = `[{"entity_id":"cover.garage_door","state":"opening","attributes":{"is_closed":false,"device_class":"garage","friendly_name":"Garage Door","supported_features":3}}]`
	client, err := hass.New(hass.Config{
		BaseURL: "http://homeassistant.invalid:8123", Token: "ha-test-token",
		HTTP: &http.Client{Transport: ha},
	})
	if err != nil {
		t.Fatalf("hass: %v", err)
	}
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = &scriptEngine{decide: coverModel(open, question, answer)}
		d.tools = hass.Tools(client)
	})
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("open the garage door", alan))
	dev.AwaitTTS(t, 2*len(question))
	dev.PlayAll(t)
	r.spoken(t, "kitchen", 1)
	if got := ha.paths(); len(got) != 1 || got[0] != "GET /api/states/cover.garage_door" {
		t.Fatalf("home assistant was asked %q before Alan said yes, want only what the cover is", got)
	}

	r.utter(t, dev, r.line("yes", alan))
	dev.AwaitTTS(t, 2*len(question)+2*len(answer))
	dev.PlayAll(t)
	r.store.awaitKind(t, journal.KindSpeechSpoken, 2)

	got := ha.paths()
	if len(got) != 2 || got[1] != "POST /api/services/cover/open_cover" {
		t.Fatalf("home assistant was asked %q, want the garage door opened once, after his yes", got)
	}
	if body := ha.sent()[1]; body != `{"entity_id":"cover.garage_door"}` {
		t.Errorf("open body = %s, want only the garage door: no nonce", body)
	}
	var spoken []string
	for _, e := range r.store.ofKind(journal.KindSpeechSpoken) {
		spoken = append(spoken, e.Fields["text"])
	}
	if len(spoken) != 2 || spoken[0] != question || spoken[1] != answer {
		t.Errorf("spoken = %q, want %q then %q", spoken, question, answer)
	}
	requested := r.store.ofKind(journal.KindConfirmationRequested)
	given := r.store.ofKind(journal.KindConfirmationGiven)
	if len(requested) != 1 || len(given) != 1 || given[0].Fields["nonce"] != requested[0].Fields["nonce"] {
		t.Errorf("requested %d, given %d: want the one nonce handed out and redeemed", len(requested), len(given))
	}
}

// Alan asks the kitchen satellite to open the living-room blinds. The same
// service as the garage, but a blind lets nobody in: they open at once, and
// nobody is asked anything.
//
// verifies SPEC §6
func TestTheLivingRoomBlindsOpenWithoutAQuestion(t *testing.T) {
	const (
		open   = `{"domain":"cover","service":"open_cover","entity_id":"cover.living_room_blinds"}`
		answer = "The blinds are going up."
	)
	ha := eveningCovers()
	ha.answers["POST /api/services/cover/open_cover"] = `[{"entity_id":"cover.living_room_blinds","state":"opening","attributes":{"current_position":0,"device_class":"blind","friendly_name":"Living Room Blinds","supported_features":15}}]`
	client, err := hass.New(hass.Config{
		BaseURL: "http://homeassistant.invalid:8123", Token: "ha-test-token",
		HTTP: &http.Client{Transport: ha},
	})
	if err != nil {
		t.Fatalf("hass: %v", err)
	}
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = &scriptEngine{decide: coverModel(open, "Open the blinds?", answer)}
		d.tools = hass.Tools(client)
	})
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("open the living room blinds", alan))
	dev.AwaitTTS(t, 2*len(answer))
	dev.PlayAll(t)
	r.store.awaitKind(t, journal.KindSpeechSpoken, 1)

	want := []string{"GET /api/states/cover.living_room_blinds", "POST /api/services/cover/open_cover"}
	if got := ha.paths(); !slices.Equal(got, want) {
		t.Errorf("home assistant was asked %q, want %q", got, want)
	}
	if spoken := r.store.ofKind(journal.KindSpeechSpoken); len(spoken) != 1 || spoken[0].Fields["text"] != answer {
		t.Errorf("spoken = %+v, want only %q", spoken, answer)
	}
	if n := len(r.store.ofKind(journal.KindConfirmationRequested)); n != 0 {
		t.Errorf("%d confirmations requested for the blinds", n)
	}
}
