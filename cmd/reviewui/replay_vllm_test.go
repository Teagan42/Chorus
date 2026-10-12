package main

import (
	"testing"

	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/provider/vllm"
	"github.com/teagan42/chorus/internal/registry"
)

// Replay re-runs a turn on the engine chorusd is configured with: the same
// CHORUS_LLM_PROVIDER, and that provider's endpoint, so a vLLM house is not
// re-run on an Ollama it does not have (ADR-0077). Unconfigured, it offers
// nothing to ask.
//
// verifies SPEC §9.2
func TestReplayAsksTheEngineChorusdIsConfiguredWith(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

	if llmEngines(env(nil)) != nil {
		t.Error("an unconfigured review UI offers an engine")
	}
	if llmEngines(env(map[string]string{llmProviderEnv: "vllm", ollamaURLEnv: "http://ollama.lan:11434", ollamaModelEnv: "qwen3:32b"})) != nil {
		t.Error("a vLLM house with no vLLM endpoint is re-run on Ollama")
	}

	onOllama := llmEngines(env(map[string]string{ollamaURLEnv: "http://ollama.lan:11434", ollamaModelEnv: "qwen3:32b"}))
	if onOllama == nil {
		t.Fatal("an Ollama house offers no engine")
	}
	if e, _, err := onOllama("qwen3:32b", ollama.DefaultPrompt, registry.Offered()); err != nil {
		t.Fatal(err)
	} else if _, ok := e.(*ollama.Engine); !ok {
		t.Errorf("engine is %T, want Ollama's", e)
	}

	onVLLM := llmEngines(env(map[string]string{
		llmProviderEnv: "vllm", vllmURLEnv: "http://vllm.lan:8000", vllmModelEnv: "Qwen/Qwen3-32B-AWQ", vllmKeyEnv: "sk-household-7f3a",
	}))
	if onVLLM == nil {
		t.Fatal("a vLLM house offers no engine")
	}
	e, v, err := onVLLM("Qwen/Qwen3-14B", ollama.DefaultPrompt, registry.Offered())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.(*vllm.Engine); !ok {
		t.Errorf("engine is %T, want vLLM's", e)
	}
	if v.Model != "Qwen/Qwen3-14B" || v.ToolSchema != ollama.ToolSchemaVersion(registry.Offered()) {
		t.Errorf("versions = %+v, want the reviewer's model and the offered tools", v)
	}
}
