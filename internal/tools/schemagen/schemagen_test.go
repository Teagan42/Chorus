package main

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func fixtureTools() map[string]Tool {
	return map[string]Tool{
		"media_search": {
			Name:        "media_search",
			Description: "Search the media library.",
			OnInterrupt: "detach",
			Scope:       "household",
			TimeoutMS:   20000,
			Latency:     "slow",
			Params: []Param{
				{Name: "query", Type: "string", Description: "Free-text search.", Required: true},
				{Name: "limit", Type: "integer", Description: "Maximum results."},
				{Name: "kind", Type: "string", Description: "What to search.", Enum: []string{"music", "film"}},
			},
		},
		"remember": {
			Name:           "remember",
			Description:    "Store a durable fact.",
			OnInterrupt:    "cancel",
			Scope:          "person",
			TimeoutMS:      10000,
			Latency:        "fast",
			UnknownSpeaker: "deny",
			Params: []Param{
				{Name: "fact", Type: "string", Description: "The fact.", Required: true},
			},
		},
		"end_session": {
			Name: "end_session", Description: "Close the conversation.",
			OnInterrupt: "cancel", Scope: "household", TimeoutMS: 10000, Latency: "fast",
		},
		"front_door": {
			Name: "front_door", Description: "Lock or unlock the front door.",
			OnInterrupt: "detach", Scope: "household", TimeoutMS: 10000, Latency: "fast",
			Params: []Param{
				{Name: "action", Type: "string", Description: "lock or unlock.", Required: true},
			},
			ConfirmWhen: []ConfirmRule{{Args: map[string]string{"action": "unlock"}}},
		},
		"garage_opener": {
			Name: "garage_opener", Description: "Press the garage door opener.",
			OnInterrupt: "uninterruptible", Scope: "household", TimeoutMS: 10000, Latency: "fast",
			RequiresConfirmation: true,
		},
	}
}

func fixtureEvents() map[string]Event {
	return map[string]Event{
		"speech_truncated": {
			Name: "speech_truncated", Actor: "speaking",
			Description:     "Barge-in cut speech short.",
			HasAudio:        true,
			TrainingSignal:  true,
			RequiresVersion: true,
			Fields: []Param{
				{Name: "spoken_text", Type: "string", Description: "Heard.", Required: true},
				{Name: "frames_played", Type: "integer", Description: "DAC frames.", Required: true},
			},
		},
		"session_opened": {
			Name: "session_opened", Actor: "session", Description: "A conversation begins.",
			Fields: []Param{
				{Name: "satellite", Type: "string", Description: "Device.", Required: true},
				{Name: "speaker_id", Type: "string", Description: "Person."},
			},
		},
		"speech_discarded": {
			Name: "speech_discarded", Actor: "speaking", Description: "Generated, never played.",
			TrainingSignal: true, RequiresVersion: true,
			Fields: []Param{
				{Name: "reason", Type: "string", Description: "Why.", Required: true, Enum: []string{"barge_in", "preempted"}},
			},
		},
	}
}

func parses(t *testing.T, src string) *token.FileSet {
	t.Helper()
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "gen.go", src, parser.AllErrors); err != nil {
		t.Fatalf("generated code does not parse: %v\n%s", err, src)
	}
	return fset
}

func TestRenderToolsGoIsValidGo(t *testing.T) {
	src := renderToolsGo(fixtureTools())
	parses(t, src)

	for _, want := range []string{
		`"media_search": {`,
		`Timeout: 20000 * time.Millisecond`,
		`Slow: true`,
		`UnknownSpeaker: "deny"`,
		`OnInterrupt: "detach"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in generated tools", want)
		}
	}
	// A household-scoped tool has no unknown-speaker policy to emit.
	if strings.Contains(src, `"end_session"`) && strings.Count(src, "UnknownSpeaker") != 2 {
		t.Errorf("UnknownSpeaker should appear once in the field and once for remember")
	}
}

// A provider has to build its own request shape, and it cannot read the JSON
// artifact: go:embed has no `../`, so schema/json is unreachable from a
// provider package. The declaration therefore has to reach Go.
//
// verifies SPEC §6, §12
func TestParamsReachTheGeneratedRegistry(t *testing.T) {
	src := renderToolsGo(fixtureTools())
	parses(t, src)

	for _, want := range []string{
		`{Name: "query", Type: "string", Description: "Free-text search.", Required: true}`,
		`{Name: "limit", Type: "integer", Description: "Maximum results.", Required: false}`,
		// The closed enum is what lets a provider reject an out-of-enum value
		// the endpoint accepted; without it that check has to be hand-written.
		`Enum: []string{"music", "film"}`,
		// What the model is told, beside what the docs show, so a provider
		// cannot send a slow tool's description without its latency hint.
		`ModelDescription: "Search the media library.` + slowHint + `"`,
		`Description: "Search the media library.",`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in generated tools", want)
		}
	}
	// A no-param tool emits no empty slice to range over.
	if strings.Contains(src, "Params: []ParamSpec{\n\t\t},") {
		t.Error("end_session emitted an empty Params slice")
	}
}

// A deferred tool keeps its policy and its docs, but nothing runs it yet, so
// the model's view leaves it out and the registry says why (SPEC §14).
//
// verifies SPEC §6, §14
func TestADeferredToolIsDeclaredButNotOffered(t *testing.T) {
	tools := fixtureTools()
	ms := tools["media_search"]
	ms.Deferred = true
	tools["media_search"] = ms

	src := renderToolsGo(tools)
	parses(t, src)
	if !strings.Contains(src, "Deferred: true") || strings.Count(src, "Deferred: true") != 1 {
		t.Errorf("want Deferred: true on media_search alone in:\n%s", src)
	}
	for _, s := range LLMToolSchemas(tools) {
		if s["name"] == "media_search" {
			t.Error("the model's tool schemas offer a deferred tool")
		}
	}
	if got := len(LLMToolSchemas(tools)); got != len(tools)-1 {
		t.Errorf("got %d schemas, want every tool but the deferred one", got)
	}
	if doc := renderToolDocs(tools); !strings.Contains(doc, "Deferred: not offered to the model") {
		t.Error("the docs do not say media_search is deferred")
	}
}

func TestRenderEventsGoIsValidGo(t *testing.T) {
	src := renderEventsGo(fixtureEvents())
	parses(t, src)

	for _, want := range []string{
		"KindSpeechTruncated Kind = \"speech_truncated\"",
		"KindSessionOpened Kind = \"session_opened\"",
		`RequiresVersions: true`,
		`RequiredFields: []string{"spoken_text", "frames_played"}`,
		"AllKinds = []Kind{",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in generated events", want)
		}
	}
}

// Generation must be stable: a map iteration leak would make `task gen:check`
// fail at random and train everyone to ignore it.
func TestRenderIsDeterministic(t *testing.T) {
	tools, events := fixtureTools(), fixtureEvents()

	// Compare against a first rendering rather than two calls inline, so the
	// comparison survives any future compiler-side common subexpression work.
	wantTools := renderToolsGo(tools)
	wantEvents := renderEventsGo(events)
	wantJSON := string(mustJSON(LLMToolSchemas(tools)))

	for i := range 20 {
		if got := renderToolsGo(tools); got != wantTools {
			t.Fatalf("renderToolsGo is not deterministic (iteration %d)", i)
		}
		if got := renderEventsGo(events); got != wantEvents {
			t.Fatalf("renderEventsGo is not deterministic (iteration %d)", i)
		}
		if got := string(mustJSON(LLMToolSchemas(tools))); got != wantJSON {
			t.Fatalf("LLMToolSchemas is not deterministic (iteration %d)", i)
		}
	}
}

func TestLLMToolSchemasShape(t *testing.T) {
	got := LLMToolSchemas(fixtureTools())
	if len(got) != len(fixtureTools()) {
		t.Fatalf("got %d schemas, want one per tool, %d", len(got), len(fixtureTools()))
	}
	if got[0]["name"] != "end_session" {
		t.Errorf("schemas must be name-sorted, first is %v", got[0]["name"])
	}

	byName := map[string]map[string]any{}
	for _, s := range got {
		byName[s["name"].(string)] = s
	}

	ms := byName["media_search"]["input_schema"].(map[string]any)
	// The acknowledgement is required too: media_search is slow.
	if req := ms["required"].([]string); len(req) != 2 || req[0] != "query" || req[1] != "acknowledgement" {
		t.Errorf("required = %v, want [query acknowledgement]", req)
	}
	props := ms["properties"].(map[string]any)
	if props["limit"].(map[string]any)["type"] != "integer" {
		t.Errorf("limit type = %v", props["limit"])
	}

	// A no-param tool still needs a valid object schema, not a null one.
	es := byName["end_session"]["input_schema"].(map[string]any)
	if es["type"] != "object" {
		t.Errorf("end_session schema type = %v", es["type"])
	}
	if _, ok := es["required"]; ok {
		t.Error("end_session must omit required rather than emit an empty list")
	}

	// Policy fields are deliberately absent: the model sees capability, the
	// orchestrator enforces policy from the same declaration.
	for _, s := range got {
		for _, leaked := range []string{"on_interrupt", "scope", "timeout_ms", "requires_confirmation"} {
			if _, ok := s[leaked]; ok {
				t.Errorf("%v leaks policy field %q to the model", s["name"], leaked)
			}
		}
	}

	if !json.Valid(mustJSON(got)) {
		t.Error("tool schemas are not valid JSON")
	}
}

// A slow tool must say so in the description, because that is the only field
// the model reads. Measured: without the hint, models call a slow tool alone
// and leave the user in silence; with it they volunteer a speak alongside.
//
// verifies SPEC §6, §14
func TestSlowToolsTellTheModelTheyAreSlow(t *testing.T) {
	byName := map[string]string{}
	for _, s := range LLMToolSchemas(fixtureTools()) {
		byName[s["name"].(string)] = s["description"].(string)
	}

	if slow := byName["media_search"]; !strings.Contains(slow, slowHint) {
		t.Errorf("slow tool description %q omits the latency hint", slow)
	}
	// The hint must not become a generic suffix: a fast tool the model thinks
	// is slow earns an unnecessary filler before every call.
	for _, name := range []string{"remember", "end_session"} {
		if got := byName[name]; strings.Contains(got, slowHint) {
			t.Errorf("fast tool %s claims to be slow: %q", name, got)
		}
	}
	if got := byName["media_search"]; !strings.HasPrefix(got, "Search the media library.") {
		t.Errorf("hint replaced the description instead of extending it: %q", got)
	}
}

// A held call is not a failure the model should apologise for: it has to be
// told how to finish it, and offered the argument that does.
//
// verifies SPEC §6
func TestConfirmableToolsOfferTheNonce(t *testing.T) {
	byName := map[string]map[string]any{}
	for _, s := range LLMToolSchemas(fixtureTools()) {
		byName[s["name"].(string)] = s
	}
	props := func(name string) map[string]any {
		return byName[name]["input_schema"].(map[string]any)["properties"].(map[string]any)
	}
	for name, hint := range map[string]string{"front_door": confirmSomeHint, "garage_opener": confirmAllHint} {
		if _, ok := props(name)["confirmation"]; !ok {
			t.Errorf("%s does not offer the confirmation argument", name)
		}
		if d := byName[name]["description"].(string); !strings.HasSuffix(d, hint) {
			t.Errorf("%s description %q does not say how to finish a held call", name, d)
		}
	}
	if _, ok := props("remember")["confirmation"]; ok {
		t.Error("remember is never held, so it should not offer a nonce")
	}
	if req := byName["front_door"]["input_schema"].(map[string]any)["required"]; len(req.([]string)) != 1 {
		t.Errorf("required = %v: the nonce is only for a call that was held", req)
	}

	src := renderToolsGo(fixtureTools())
	parses(t, src)
	if !strings.Contains(src, `ConfirmWhen: []ConfirmRule{`) || !strings.Contains(src, `{Args: map[string]string{"action": "unlock"}},`) {
		t.Error("the generated registry does not carry front_door's confirm_when")
	}
	doc := renderToolDocs(fixtureTools())
	for _, want := range []string{"| `front_door` | detach | household | 10000ms | **some calls** |", "- `action` `unlock`", "| `garage_opener` | uninterruptible | household | 10000ms | **yes** |", "| `confirmation` | string |"} {
		if !strings.Contains(doc, want) {
			t.Errorf("tool docs are missing %q", want)
		}
	}
}

// A slow tool cannot be called without saying something: the words the
// person hears while it works are a required argument, not a speak call the
// model may or may not volunteer beside it.
//
// verifies SPEC §4.1, §11
func TestSlowToolsRequireAnAcknowledgement(t *testing.T) {
	byName := map[string]map[string]any{}
	for _, s := range LLMToolSchemas(fixtureTools()) {
		byName[s["name"].(string)] = s
	}
	schema := func(name string) (map[string]any, []string) {
		in := byName[name]["input_schema"].(map[string]any)
		req, _ := in["required"].([]string)
		return in["properties"].(map[string]any), req
	}
	props, req := schema("media_search")
	if _, ok := props["acknowledgement"]; !ok || !slices.Contains(req, "acknowledgement") {
		t.Errorf("media_search offers %v requiring %v: want a required acknowledgement", props, req)
	}
	for _, fast := range []string{"remember", "end_session", "front_door"} {
		if props, _ := schema(fast); props["acknowledgement"] != nil {
			t.Errorf("%s is fast, so it should not ask for an acknowledgement", fast)
		}
	}
	if !strings.Contains(renderToolDocs(fixtureTools()), "| `acknowledgement` | string | yes |") {
		t.Error("tool docs do not show the acknowledgement as required")
	}
}

func TestToolDocsCoverEveryTool(t *testing.T) {
	doc := renderToolDocs(fixtureTools())
	for name := range fixtureTools() {
		if !strings.Contains(doc, "## `"+name+"`") {
			t.Errorf("docs missing section for %s", name)
		}
	}
	if !strings.Contains(doc, "DO NOT EDIT") {
		t.Error("generated docs must warn against editing")
	}
	if !strings.Contains(doc, "No parameters.") {
		t.Error("param-less tools need an explicit statement, not a blank section")
	}
}

func TestEventDocsCoverEveryEvent(t *testing.T) {
	doc := renderEventDocs(fixtureEvents())
	for name := range fixtureEvents() {
		if !strings.Contains(doc, "| `"+name+"` |") {
			t.Errorf("docs missing row for %s", name)
		}
		if !strings.Contains(doc, "## `"+name+"`") {
			t.Errorf("docs missing section for %s", name)
		}
	}
	if !strings.Contains(doc, "DO NOT EDIT") {
		t.Error("generated docs must warn against editing")
	}
}

// The kind table names an event; its fields were documented nowhere a reader
// looks. Each event gets the table its tool counterpart already has, plus the
// flags the journal enforces from the same declaration (SPEC §8).
func TestEventDocsRenderFieldsAndFlagsPerEvent(t *testing.T) {
	doc := renderEventDocs(fixtureEvents())
	for _, want := range []string{
		"## `speech_truncated`\n\nBarge-in cut speech short.\n\n" +
			"Actor: `speaking`. `has_audio`: yes. `training_signal`: yes. `speculative`: no. `requires_versions`: yes.\n\n" +
			"| Field | Type | Required | Description |\n|---|---|---|---|\n" +
			"| `spoken_text` | string | yes | Heard. |\n" +
			"| `frames_played` | integer | yes | DAC frames. |\n",
		// A false flag is said, not blanked: the table's blank cell is for scanning.
		"Actor: `session`. `has_audio`: no. `training_signal`: no. `speculative`: no. `requires_versions`: no.",
		"| `speaker_id` | string |  | Person. |",
		// Enums render as the tool docs render them.
		"| `reason` | string | yes | Why. One of: `barge_in`, `preempted`. |",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q in event docs:\n%s", want, doc)
		}
	}
	// Sections follow the table, in the table's order.
	table := strings.Index(doc, "| `speech_truncated` |")
	first := strings.Index(doc, "## `session_opened`")
	last := strings.Index(doc, "## `speech_truncated`")
	if !(table < first && first < last) {
		t.Errorf("sections out of order: table %d, session_opened %d, speech_truncated %d", table, first, last)
	}
	if strings.Contains(doc, "No fields.") {
		t.Error("every fixture event declares fields; none should be stated empty")
	}

	bare := renderEventDocs(map[string]Event{
		"device_lost": {Name: "device_lost", Actor: "device", Description: "Stream ended."},
	})
	if !strings.Contains(bare, "## `device_lost`\n\nStream ended.\n\nActor: `device`.") || !strings.Contains(bare, "No fields.\n") {
		t.Errorf("field-less events need an explicit statement, not a blank section:\n%s", bare)
	}
}

func TestGoName(t *testing.T) {
	cases := map[string]string{
		"session_opened":        "SessionOpened",
		"utterance_transcribed": "UtteranceTranscribed",
		"tool.result":           "ToolResult",
		"wake_rejected":         "WakeRejected",
	}
	for in, want := range cases {
		if got := goName(in); got != want {
			t.Errorf("goName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The garage opens with the same cover.open_cover as the living-room blinds,
// so its entry carries the classes it holds. They reach the registry as
// classes, never as an argument a call would have to match (ADR-0041).
//
// verifies SPEC §6
func TestATargetClassReachesTheRegistryAsAClassNotAnArgument(t *testing.T) {
	// As `cue export` writes ha_call_service's entry.
	var tool Tool
	exported := `{"name":"ha_call_service","description":"Act on the home.","on_interrupt":"detach","scope":"household","timeout_ms":10000,"latency":"fast",
		"params":[{"name":"domain","type":"string","description":"Service domain.","required":true},{"name":"service","type":"string","description":"Service.","required":true}],
		"confirm_when":[{"domain":"lock","service":"unlock"},{"target_class":["door","garage","gate"],"domain":"cover","service":"open_cover"}]}`
	if err := json.Unmarshal([]byte(exported), &tool); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []ConfirmRule{
		{Args: map[string]string{"domain": "lock", "service": "unlock"}},
		{Args: map[string]string{"domain": "cover", "service": "open_cover"}, TargetClass: []string{"door", "garage", "gate"}},
	}
	if !reflect.DeepEqual(tool.ConfirmWhen, want) {
		t.Errorf("confirm_when = %+v, want %+v", tool.ConfirmWhen, want)
	}

	tools := map[string]Tool{"ha_call_service": tool}
	src := renderToolsGo(tools)
	parses(t, src)
	for _, line := range []string{
		`{Args: map[string]string{"domain": "lock", "service": "unlock"}},`,
		`{Args: map[string]string{"domain": "cover", "service": "open_cover"}, TargetClass: []string{"door", "garage", "gate"}},`,
	} {
		if !strings.Contains(src, line) {
			t.Errorf("the generated registry is missing %s", line)
		}
	}
	doc := renderToolDocs(tools)
	for _, line := range []string{
		"- `domain` `lock`, `service` `unlock`\n",
		"- `domain` `cover`, `service` `open_cover`, on a target of class `door`, `garage`, `gate`\n",
	} {
		if !strings.Contains(doc, line) {
			t.Errorf("tool docs are missing %q", line)
		}
	}

	if err := json.Unmarshal([]byte(`{"target_class":"garage","domain":"cover"}`), new(ConfirmRule)); err == nil {
		t.Error("a target_class that is not a list was accepted")
	}
}
