package main

import (
	"encoding/json"
	"go/parser"
	"go/token"
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
	if len(got) != 3 {
		t.Fatalf("got %d schemas, want 3", len(got))
	}
	if got[0]["name"] != "end_session" {
		t.Errorf("schemas must be name-sorted, first is %v", got[0]["name"])
	}

	byName := map[string]map[string]any{}
	for _, s := range got {
		byName[s["name"].(string)] = s
	}

	ms := byName["media_search"]["input_schema"].(map[string]any)
	if req := ms["required"].([]string); len(req) != 1 || req[0] != "query" {
		t.Errorf("required = %v, want [query]", req)
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
		if !strings.Contains(doc, "`"+name+"`") {
			t.Errorf("docs missing row for %s", name)
		}
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
