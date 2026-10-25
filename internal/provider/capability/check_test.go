package capability_test

import (
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/provider/capability"
)

// theHousesStack is docker-compose's: Parakeet on speaches, Smart Turn,
// TitaNet, Qwen3 on Ollama with its embedding model, and Kokoro.
func theHousesStack(llm string) []capability.Declaration {
	return []capability.Declaration{
		capability.Of(capability.RoleSTT, "speaches"),
		capability.Of(capability.RoleEndpointing, "smartturn"),
		capability.Of(capability.RoleSpeakerID, "speakerid"),
		capability.Of(capability.RoleEmbedding, "ollama"),
		capability.Of(capability.RoleLLM, llm),
		capability.Of(capability.RoleTTS, "kokoro"),
	}
}

// The default stack runs, and says once what Ollama costs it: a speak
// call's words wait for the call to close (SPEC §4.1, §11).
//
// verifies SPEC §10
func TestTheDefaultStackRunsAndSaysWhatOllamaCostsIt(t *testing.T) {
	r := capability.Check(capability.Pipeline{Providers: theHousesStack("ollama")})
	if err := r.Err(); err != nil {
		t.Fatalf("the default stack was refused: %v", err)
	}
	if len(r.Degraded) != 1 || r.Degraded[0].Provider != "ollama" || r.Degraded[0].Capability != capability.StreamsToolArguments {
		t.Fatalf("degraded = %+v, want Ollama's unstreamed arguments alone", r.Degraded)
	}
	line := r.Degraded[0].String()
	for _, want := range []string{"llm provider ollama lacks streams_tool_arguments", "first audio waits", "CHORUS_LLM_PROVIDER=vllm", "SPEC §4.1"} {
		if !strings.Contains(line, want) {
			t.Errorf("finding %q does not say %q", line, want)
		}
	}
}

// verifies SPEC §10
func TestOnVLLMTheStackNeedsNothingItLacks(t *testing.T) {
	r := capability.Check(capability.Pipeline{Providers: theHousesStack("vllm")})
	if len(r.Refused)+len(r.Degraded) != 0 {
		t.Errorf("findings = %+v, want none", r)
	}
}

// Parakeet hears English only and ignores the language it is sent, so a
// household asking it for German would get German heard as English: every
// turn answering words nobody said. That is refused at startup (SPEC §10).
//
// verifies SPEC §10
func TestAnEarIsNotAskedForALanguageItCannotHear(t *testing.T) {
	r := capability.Check(capability.Pipeline{Providers: theHousesStack("vllm"), Language: "de"})
	err := r.Err()
	if err == nil {
		t.Fatal("Parakeet was asked for German and the pipeline ran")
	}
	for _, want := range []string{"stt provider speaches hears en, not \"de\"", "words nobody said", "SPEC §10", "docs/reference/providers.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not say %q: %v", want, err)
		}
	}
}

func TestARegionalTagIsItsLanguage(t *testing.T) {
	for _, lang := range []string{"en", "en-GB", "EN_us", " en "} {
		if err := capability.Check(capability.Pipeline{Providers: theHousesStack("vllm"), Language: lang}).Err(); err != nil {
			t.Errorf("Language %q: %v", lang, err)
		}
	}
}

// A Wyoming whisper hears what its model hears, which only the server
// knows, so asking it for German is the household's call.
func TestAnEarThatDeclaresNoLanguagesIsLeftToItsModel(t *testing.T) {
	for _, ear := range []string{"wyoming", "openai"} {
		stack := theHousesStack("vllm")
		stack[0] = capability.Of(capability.RoleSTT, ear)
		if err := capability.Check(capability.Pipeline{Providers: stack, Language: "de"}).Err(); err != nil {
			t.Errorf("%s asked for German: %v", ear, err)
		}
	}
}

// A provider declared at runtime is held to the same needs, so one built
// later (a hosted voice, an engine without tools) is refused by
// declaring what it lacks, and every refusal is named in one error.
//
// verifies SPEC §10
func TestEveryRefusalIsNamedAtOnce(t *testing.T) {
	stack := theHousesStack("ollama")
	stack[4] = capability.Declaration{Name: "llamacpp", Role: capability.RoleLLM, Capabilities: capability.Capabilities{Cancels: true, StreamsText: true}}
	stack[5] = capability.Declaration{Name: "cloudvoice", Role: capability.RoleTTS}
	r := capability.Check(capability.Pipeline{Providers: stack})
	if len(r.Refused) != 2 {
		t.Fatalf("refused = %+v, want the engine's tool calls and the voice's cancel", r.Refused)
	}
	err := r.Err().Error()
	for _, want := range []string{"llm provider llamacpp lacks tool_calls", "speak is a tool", "tts provider cloudvoice lacks cancels", "a barge-in or a deadline cannot stop it"} {
		if !strings.Contains(err, want) {
			t.Errorf("refusal does not say %q:\n%s", want, err)
		}
	}
	// In Needs' order: the engine before the voice, as a turn meets them.
	if strings.Index(err, "llamacpp") > strings.Index(err, "cloudvoice") {
		t.Errorf("refusals out of order:\n%s", err)
	}
}

// An optional sidecar left unset has no provider, and so no needs: a house
// without Smart Turn is degraded by its own log line, not refused here.
func TestARoleWithNoProviderHasNoNeeds(t *testing.T) {
	r := capability.Check(capability.Pipeline{Providers: []capability.Declaration{
		capability.Of(capability.RoleSTT, "speaches"),
		capability.Of(capability.RoleLLM, "vllm"),
		capability.Of(capability.RoleTTS, "kokoro"),
	}})
	if len(r.Refused)+len(r.Degraded) != 0 {
		t.Errorf("findings = %+v, want none", r)
	}
}

// Every provider the schema declares can run on its own: a declaration that
// failed a required need would be a provider chorusd can never start with.
//
// verifies SPEC §10
func TestEveryDeclaredProviderMeetsEveryRequiredNeed(t *testing.T) {
	for _, role := range capability.Roles {
		for name, d := range capability.Declared[role] {
			if err := capability.Check(capability.Pipeline{Providers: []capability.Declaration{d}}).Err(); err != nil {
				t.Errorf("%s %s: %v", role, name, err)
			}
		}
	}
}

func TestEveryRoleHasAProviderAndANeed(t *testing.T) {
	needed := map[capability.Role]bool{}
	for _, n := range capability.Needs {
		needed[n.Role] = true
	}
	for _, role := range capability.Roles {
		if len(capability.Declared[role]) == 0 {
			t.Errorf("no provider declares %s", role)
		}
		if !needed[role] {
			t.Errorf("nothing is needed of %s", role)
		}
	}
}

func TestOfPanicsOnAProviderTheSchemaDoesNotDeclare(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "piper") {
			t.Errorf("recovered %v, want a panic naming piper", r)
		}
	}()
	capability.Of(capability.RoleTTS, "piper")
}
