package vllm_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/teagan42/chorus/internal/provider/capability"
	"github.com/teagan42/chorus/internal/provider/capability/capabilitytest"
	"github.com/teagan42/chorus/internal/provider/vllm"
	"github.com/teagan42/chorus/internal/session"
)

// A barge-in cancels whatever the turn engine is doing; a declaration that
// it cancels is proved against a server that never answers (ADR-0102). Its
// streaming claims are held by speak_test.go
// (TestTheWordsAreReadOutOfArgumentsStillArriving).
//
// verifies SPEC §10
func TestTheVLLMEngineIsHeldToItsDeclaration(t *testing.T) {
	url, arrived := capabilitytest.Hung(t)
	p, err := vllm.New(vllm.Config{BaseURL: url + "/v1", Model: "Qwen/Qwen3-32B-AWQ"})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Capabilities()
	if want := capability.Declared[capability.RoleLLM]["vllm"]; !reflect.DeepEqual(d, want) {
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
