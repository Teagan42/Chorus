package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// fixtureProviders is the household's stack as provider.cue exports it:
// the default ear, the two turn engines, and the voice.
func fixtureProviders(t *testing.T) providerSchema {
	t.Helper()
	var s providerSchema
	raw := `{
		"roles": ["stt", "llm", "tts"],
		"providers": {
			"stt": {"speaches": {"name": "speaches", "role": "stt", "description": "Parakeet on speaches.", "cancels": true, "languages": ["en"]},
			        "wyoming": {"name": "wyoming", "role": "stt", "description": "Home Assistant's whisper.", "cancels": true, "languages": []}},
			"llm": {"ollama": {"name": "ollama", "role": "llm", "description": "Qwen3 on Ollama.", "cancels": true, "tool_calls": true, "streams_text": true, "streams_tool_arguments": false},
			        "vllm": {"name": "vllm", "role": "llm", "description": "Qwen3 on vLLM.", "cancels": true, "tool_calls": true, "streams_text": true, "streams_tool_arguments": true}},
			"tts": {"kokoro": {"name": "kokoro", "role": "tts", "description": "Kokoro-82M.", "cancels": true}}
		},
		"needs": {
			"stt": {"languages": {"level": "required", "consequence": "every turn answers words nobody said", "source": "SPEC §10"}},
			"llm": {"tool_calls": {"level": "required", "consequence": "the model could say nothing", "source": "SPEC §4.1"},
			        "streams_tool_arguments": {"level": "degraded", "consequence": "first audio waits for the whole call", "source": "SPEC §11"}}
		}
	}`
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// The Go a declaration becomes is what chorusd checks its pipeline against,
// so the generated file must compile and carry every provider's claims.
//
// verifies SPEC §10
func TestRenderProvidersGoIsValidGo(t *testing.T) {
	src, err := renderProvidersGo(fixtureProviders(t))
	if err != nil {
		t.Fatal(err)
	}
	parses(t, src)
	for _, want := range []string{
		`RoleSTT Role = "stt"`,
		`RoleLLM Role = "llm"`,
		`StreamsToolArguments Capability = "streams_tool_arguments"`,
		"Languages []string",
		`"speaches": {Name: "speaches", Role: RoleSTT, Description: "Parakeet on speaches.", Capabilities: Capabilities{Cancels: true, Languages: []string{"en"}}}`,
		`"wyoming": {Name: "wyoming", Role: RoleSTT, Description: "Home Assistant's whisper.", Capabilities: Capabilities{Cancels: true, Languages: nil}}`,
		"StreamsToolArguments: false",
		"{Role: RoleLLM, Capability: StreamsToolArguments, Level: Degraded,",
		"case ToolCalls:\n\t\treturn c.ToolCalls",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated Go lacks %q:\n%s", want, src)
		}
	}
	// A list is matched, not had: Has never reads Languages.
	if strings.Contains(src, "return c.Languages") {
		t.Errorf("Has reads the language list as a flag:\n%s", src)
	}
}

// Needs are listed in the order a turn meets their roles, so the startup
// log reads the ear's problems before the voice's.
func TestNeedsFollowTheRolesOrder(t *testing.T) {
	src, err := renderProvidersGo(fixtureProviders(t))
	if err != nil {
		t.Fatal(err)
	}
	ear := strings.Index(src, "{Role: RoleSTT, Capability: Languages")
	engine := strings.Index(src, "{Role: RoleLLM, Capability: StreamsToolArguments")
	if ear < 0 || engine < 0 || ear > engine {
		t.Errorf("the ear's need should come first: stt at %d, llm at %d", ear, engine)
	}
}

// cancels is every role's, so it leads every table and the struct.
func TestCancelsComesFirst(t *testing.T) {
	names, _, err := fixtureProviders(t).capabilities()
	if err != nil {
		t.Fatal(err)
	}
	if names[0] != "cancels" {
		t.Errorf("capabilities = %v, want cancels first", names)
	}
}

func TestANeedNoProviderCanDeclareIsRefused(t *testing.T) {
	s := fixtureProviders(t)
	s.Needs["tts"] = map[string]Need{"streams_audio": {Level: "degraded", Consequence: "a sentence plays only once rendered", Source: "SPEC §11"}}
	if _, err := renderProvidersGo(s); err == nil || !strings.Contains(err.Error(), "needs.tts.streams_audio") {
		t.Errorf("err = %v, want it to name needs.tts.streams_audio", err)
	}
}

func TestACapabilityIsOneShapeEverywhere(t *testing.T) {
	s := fixtureProviders(t)
	s.Providers["tts"]["kokoro"].Capabilities["languages"] = json.RawMessage("true")
	if _, err := renderProvidersGo(s); err == nil || !strings.Contains(err.Error(), "languages") {
		t.Errorf("err = %v, want it to name languages", err)
	}
}

// The reference is what a household reads to choose a provider: a refusal
// stands out, and an ear with no declared languages says its model decides
// rather than reading as one that hears nothing.
func TestProviderDocsSayWhatEachRoleNeedsAndEachProviderDeclares(t *testing.T) {
	doc, err := renderProviderDocs(fixtureProviders(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"DO NOT EDIT",
		"| stt | `languages` | **refused**: every turn answers words nobody said | SPEC §10 |",
		"| llm | `streams_tool_arguments` | degraded: first audio waits for the whole call | SPEC §11 |",
		"| Provider | `cancels` | `streams_text` | `streams_tool_arguments` | `tool_calls` |",
		"| `ollama` | yes | yes | no | yes |",
		"| `speaches` | yes | en |",
		"| `wyoming` | yes | its model's |",
		"- `vllm`: Qwen3 on vLLM.",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs lack %q:\n%s", want, doc)
		}
	}
	// The voice's table has only the column its providers declare.
	if strings.Contains(doc, "## tts\n\n| Provider | `cancels` | `languages`") {
		t.Errorf("tts table carries the ear's column:\n%s", doc)
	}
}

func TestRoleNameSpellsInitialismsInCapitals(t *testing.T) {
	for in, want := range map[string]string{"stt": "STT", "llm": "LLM", "tts": "TTS", "speaker_id": "SpeakerID", "endpointing": "Endpointing"} {
		if got := roleName(in); got != want {
			t.Errorf("roleName(%q) = %q, want %q", in, got, want)
		}
	}
}
