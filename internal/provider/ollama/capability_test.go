package ollama_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/teagan42/chorus/internal/provider/capability"
	"github.com/teagan42/chorus/internal/provider/capability/capabilitytest"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/session"
)

// A barge-in cancels whatever the turn engine is doing; a declaration that
// it cancels is proved against a server that never answers (ADR-0102). Its
// streaming claims are held by decode_test.go: a speak call arrives whole
// (TestASpeakCallBecomesOneFinalDelta).
//
// verifies SPEC §10
func TestTheOllamaEngineIsHeldToItsDeclaration(t *testing.T) {
	url, arrived := capabilitytest.Hung(t)
	p, err := ollama.New(ollama.Config{BaseURL: url, Model: "qwen3:32b"})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Capabilities()
	if want := capability.Declared[capability.RoleLLM]["ollama"]; !reflect.DeepEqual(d, want) {
		t.Fatalf("Capabilities() = %+v, want schema/provider.cue's %+v", d, want)
	}
	capabilitytest.Cancels(t, d, arrived, func(ctx context.Context) error {
		ch, err := p.Turn(ctx, session.Input{ConversationID: "conv-kitchen", Speaker: "alan", Room: "kitchen", Text: "turn off the kitchen lights"})
		if err != nil {
			return err
		}
		return failed(ch)
	})
}

// A barge-in cancels whatever recall's embedder is doing; a declaration
// that it cancels is proved against a server that never answers (ADR-0102).
//
// verifies SPEC §10
func TestTheOllamaEmbedderIsHeldToItsDeclaration(t *testing.T) {
	url, arrived := capabilitytest.Hung(t)
	p, err := ollama.NewEmbedder(ollama.EmbedConfig{BaseURL: url, Model: "nomic-embed-text"})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Capabilities()
	if want := capability.Declared[capability.RoleEmbedding]["ollama"]; !reflect.DeepEqual(d, want) {
		t.Fatalf("Capabilities() = %+v, want schema/provider.cue's %+v", d, want)
	}
	capabilitytest.Cancels(t, d, arrived, func(ctx context.Context) error {
		_, err := p.Embed(ctx, []string{"alan takes his coffee black"})
		return err
	})
}

// failed drains a turn, as the session must, and is its failure: a turn
// that ends on an error reports it in its TurnEnd, not from Turn.
func failed(ch <-chan session.Action) error {
	var err error
	for a := range ch {
		if end, ok := a.(session.TurnEnd); ok && end.FinishReason == session.FinishError {
			err = errors.New(end.Completion)
		}
	}
	return err
}
