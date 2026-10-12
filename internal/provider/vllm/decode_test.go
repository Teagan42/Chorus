package vllm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/teagan42/chorus/internal/session"
)

// Fixtures are SSE streams in the shape vLLM's OpenAI server sends with
// --enable-auto-tool-choice --tool-call-parser hermes, arguments split where
// a tokenizer splits them, so a wire change breaks a test rather than the
// house.
func replay(t *testing.T, name string) ([]session.Action, error) {
	t.Helper()
	return collect(t, context.Background(), strings.NewReader(fixture(t, name)))
}

// Alan asks for the kitchen and porch lights off. The model says what it is
// doing and makes both calls in one turn. The words stream as they arrive,
// before the speak call's arguments have closed, and each light's call
// dispatches as soon as its own arguments close, ahead of the next one's,
// in the order the model made them.
//
// verifies SPEC §4.1
func TestSpeechStreamsAndEachCallDispatchesAsItsJSONCloses(t *testing.T) {
	acts, err := replay(t, "speak_and_two_lights.sse")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []string{"speech", "speech", "speech", "speech:last", "call:ha_call_service", "call:ha_call_service", "end:tool_calls"}
	if got := kinds(acts); !slices.Equal(got, want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	sp := speechOf(acts)
	if got := []string{sp[0].Text, sp[1].Text, sp[2].Text, sp[3].Text}; !slices.Equal(got, []string{"Turning off the kit", "chen and por", "ch lights", "."}) {
		t.Errorf("deltas = %q, want the words as they arrived", got)
	}
	for _, d := range sp {
		if d.CallID != "chatcmpl-tool-5d1e0b9a4c7f4e2b" || d.Mode != session.ModeQueue {
			t.Errorf("delta %+v, want the speak call's id and its queue mode", d)
		}
	}
	calls := callsOf(acts)
	for i, want := range []struct{ id, entity string }{
		{"chatcmpl-tool-a83f61c09e2d47d5", "light.kitchen"},
		{"chatcmpl-tool-0c9b7e5f3a6148e1", "light.porch"},
	} {
		var args struct {
			Domain, Service string
			EntityID        string `json:"entity_id"`
		}
		if err := json.Unmarshal([]byte(calls[i].Args), &args); err != nil {
			t.Fatalf("call %d args %q are not JSON: %v", i, calls[i].Args, err)
		}
		if calls[i].ID != want.id || args.EntityID != want.entity || args.Service != "turn_off" {
			t.Errorf("call %d = %+v, want %s on %s", i, calls[i], want.id, want.entity)
		}
	}
}

// A call is acted on the moment its JSON closes, not when the next call
// starts or the turn ends: the first light's call is out before a byte of
// the second has been read.
//
// verifies SPEC §4.1
func TestACallDispatchesBeforeAnythingAfterItArrives(t *testing.T) {
	stream := fixture(t, "speak_and_two_lights.sse")
	// Everything up to and including the fragment that closes the kitchen
	// light's arguments, then nothing more for now.
	cut := strings.Index(stream, `chen\"}`)
	cut += strings.Index(stream[cut:], "\n\n") + 2
	pr, pw := io.Pipe()
	out := make(chan session.Action)
	go func() {
		defer close(out)
		_ = decoder{}.decode(context.Background(), pr, out)
	}()
	go func() { _, _ = io.WriteString(pw, stream[:cut]) }()

	for a := range out {
		if c, ok := a.(session.ToolCall); ok {
			if c.ID != "chatcmpl-tool-a83f61c09e2d47d5" {
				t.Errorf("first call out is %+v, want the kitchen's", c)
			}
			break
		}
	}
	go func() { _, _ = io.WriteString(pw, stream[cut:]); _ = pw.Close() }()
	rest := drain(t, out)
	if got := kinds(rest); !slices.Equal(got, []string{"call:ha_call_service", "end:tool_calls"}) {
		t.Errorf("after the kitchen's call: %v", got)
	}
}

// The forecast's degree sign arrives as a \u escape cut in two, and the
// accented name as raw UTF-8 that a reader returning one byte at a time cuts
// mid-rune. Neither reaches the voice as half a character.
//
// verifies SPEC §4.1
func TestWordsSplitMidEscapeOrMidRuneArriveWhole(t *testing.T) {
	acts, err := collect(t, context.Background(), iotest.OneByteReader(strings.NewReader(fixture(t, "degrees_and_accents.sse"))))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sp := speechOf(acts)
	if got, want := said(sp, "chatcmpl-tool-4a8c1e6f0b3d4972"), "It's 18°C in the kitchen, and Sigur Rós is next."; got != want {
		t.Errorf("said %q, want %q", got, want)
	}
	for _, d := range sp {
		if strings.ContainsRune(d.Text, '�') || strings.Contains(d.Text, `\u`) {
			t.Errorf("a delta carried half a character: %q", d.Text)
		}
	}
	if sp[0].Text != "It's 18" {
		t.Errorf("first delta = %q, want the words before the cut escape", sp[0].Text)
	}
}

// An emoji outside the BMP arrives as a surrogate pair, the pair split
// across fragments; the voice is handed the character once, whole.
//
// verifies SPEC §4.1
func TestASurrogatePairSplitAcrossFragmentsArrivesWhole(t *testing.T) {
	stream := sse(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"chatcmpl-tool-eggs","type":"function","index":0,"function":{"name":"speak"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"text\": \"Your eggs are ready \\ud83c"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\\udf73"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	acts, err := collect(t, context.Background(), strings.NewReader(stream))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sp := speechOf(acts)
	if got := said(sp, "chatcmpl-tool-eggs"); got != "Your eggs are ready 🍳" {
		t.Errorf("said %q", got)
	}
	if sp[0].Text != "Your eggs are ready " {
		t.Errorf("first delta = %q, want the high surrogate held back", sp[0].Text)
	}
}

// The words arrive before the mode, and the mode is interject. An utterance
// is placed in the speech channel by its first delta, so it is queued: words
// already streaming cannot be moved, and holding them for a mode that may
// never come would lose what streaming is for (ADR-0077).
//
// verifies SPEC §4.1, §4.2
func TestAModeWrittenAfterTheWordsBeganDoesNotMoveThem(t *testing.T) {
	acts, err := replay(t, "text_before_mode.sse")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sp := speechOf(acts)
	if len(sp) < 2 || said(sp, sp[0].CallID) != "It's eighteen degrees and sunny." {
		t.Fatalf("speech = %+v", sp)
	}
	for _, d := range sp {
		if d.Mode != session.ModeQueue {
			t.Errorf("delta %q has mode %q, want queue throughout", d.Text, d.Mode)
		}
	}
	// What the model wrote is still the record of what it asked for.
	if end := endOf(t, acts); !strings.Contains(end.Completion, `\"mode\": \"interject\"`) {
		t.Errorf("completion lost the mode the model wrote: %s", end.Completion)
	}
}

// A mode declared before the words is the utterance's from its first delta.
//
// verifies SPEC §4.2
func TestAModeWrittenBeforeTheWordsIsKept(t *testing.T) {
	stream := sse(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"chatcmpl-tool-oven","type":"function","index":0,"function":{"name":"speak"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"mode\": \"preempt\", \"text\": \"Actually, the oven"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":" is still on.\"}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	acts, err := collect(t, context.Background(), strings.NewReader(stream))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, d := range speechOf(acts) {
		if d.Mode != session.ModePreempt {
			t.Errorf("delta %q has mode %q, want preempt", d.Text, d.Mode)
		}
	}
}

// An out-of-enum mode must not reach the speech channel: preempt and
// interject discard or pause speech the person is still hearing.
//
// verifies SPEC §4.2
func TestAnUnknownModeFallsBackToQueue(t *testing.T) {
	stream := sse(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"chatcmpl-tool-filler","type":"function","index":0,"function":{"name":"speak","arguments":"{\"mode\": \"filler\", \"text\": \"One moment.\"}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	acts, err := collect(t, context.Background(), strings.NewReader(stream))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sp := speechOf(acts)
	if len(sp) != 1 || sp[0].Mode != session.ModeQueue || !sp[0].Last || sp[0].Text != "One moment." {
		t.Errorf("speech = %+v, want one queued, final delta", sp)
	}
}

// A long answer cut off by max_tokens partway through its words: what
// arrived was said, the utterance is closed, and the turn ends with length.
//
// verifies SPEC §4.1, §8
func TestAnAnswerCutByLengthIsClosedWhereItStopped(t *testing.T) {
	acts, err := replay(t, "cut_by_length.sse")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := kinds(acts); !slices.Equal(got, []string{"speech", "speech", "speech:last", "end:length"}) {
		t.Fatalf("actions = %v", got)
	}
	if got := said(speechOf(acts), "chatcmpl-tool-c6e2a9f4b1d84e35"); got != "The Beatles recorded Abbey Road in 1969, and it was the last album" {
		t.Errorf("said %q", got)
	}
}

// The follow-up says it is done and ends the session; end_session has no
// parameters and is dispatched with them as {}.
//
// verifies SPEC §4.1, §4.5
func TestEndSessionDispatchesWithEmptyArguments(t *testing.T) {
	acts, err := replay(t, "done_and_end.sse")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	calls := callsOf(acts)
	if len(calls) != 1 || calls[0].Tool != "end_session" || calls[0].Args != "{}" {
		t.Errorf("calls = %+v, want end_session with {}", calls)
	}
	if got := said(speechOf(acts), "chatcmpl-tool-e41c7a2d905b4f6a"); got != "Done, both are off." {
		t.Errorf("said %q", got)
	}
}

// A parameterless call some servers send with no arguments at all is
// dispatched with {} too, once the turn says it has finished.
//
// verifies SPEC §4.1
func TestACallWithNoArgumentsGoesAsAnEmptyObject(t *testing.T) {
	stream := sse(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"chatcmpl-tool-bye","type":"function","index":0,"function":{"name":"end_session","arguments":""}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	acts, err := collect(t, context.Background(), strings.NewReader(stream))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if calls := callsOf(acts); len(calls) != 1 || calls[0].Args != "{}" {
		t.Errorf("calls = %+v", calls)
	}
}

// Broken arguments go as written: the call fails as a result the model is
// shown, rather than vanishing from the turn (ADR-0037).
//
// verifies SPEC §7
func TestBrokenArgumentsAreDispatchedAsWritten(t *testing.T) {
	stream := sse(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"chatcmpl-tool-timer","type":"function","index":0,"function":{"name":"timer_start","arguments":"{\"minutes\": 12"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	acts, err := collect(t, context.Background(), strings.NewReader(stream))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if calls := callsOf(acts); len(calls) != 1 || calls[0].Args != `{"minutes": 12` {
		t.Errorf("calls = %+v", calls)
	}
}

// Asked what day it is with thinking on, Qwen3 reasons in its own field and
// answers in content, calling nothing: the answer is spoken once the turn
// has finished, as one implicit utterance (ADR-0046).
//
// verifies SPEC §4.1
func TestAnAnswerInContentIsSpokenWhenTheTurnCallsNothing(t *testing.T) {
	acts, err := replay(t, "day_in_content.sse")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := kinds(acts); !slices.Equal(got, []string{"speech:last", "end:stop"}) {
		t.Fatalf("actions = %v", got)
	}
	sp := speechOf(acts)[0]
	if sp.Text != "Today is Friday, 9 October 2026." || sp.CallID != "" || sp.Mode != session.ModeQueue {
		t.Errorf("spoke %+v, want the day as one implicit, queued utterance", sp)
	}
	var comp struct{ Content, Reasoning string }
	if err := json.Unmarshal([]byte(endOf(t, acts).Completion), &comp); err != nil || comp.Reasoning == "" {
		t.Errorf("completion = %s (%v), want the reasoning kept", endOf(t, acts).Completion, err)
	}
}

// Content is not an answer beside a call, cut off by length, or with no
// reasoning of its own -- with thinking off, content cannot be told from
// reasoning, so it stays unheard (ADR-0046).
//
// verifies SPEC §4.1
func TestContentThatIsNotAnAnswerIsNotSpoken(t *testing.T) {
	const (
		think = `{"choices":[{"index":0,"delta":{"reasoning":"Alan wants the kitchen lights off."},"finish_reason":null}]}`
		words = `{"choices":[{"index":0,"delta":{"content":"Turning off the kitchen lights."},"finish_reason":null}]}`
		call  = `{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"chatcmpl-tool-k1","type":"function","index":0,"function":{"name":"ha_call_service","arguments":"{\"domain\": \"light\", \"service\": \"turn_off\", \"entity_id\": \"light.kitchen\"}"}}]},"finish_reason":null}]}`
		stop  = `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
		tools = `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`
		cut   = `{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`
	)
	for why, stream := range map[string]string{
		"content beside a call":   sse(think, words, call, tools),
		"cut off by length":       sse(think, words, cut),
		"no reasoning of its own": sse(words, stop),
	} {
		acts, err := collect(t, context.Background(), strings.NewReader(stream))
		if err != nil {
			t.Fatalf("%s: decode: %v", why, err)
		}
		if sp := speechOf(acts); len(sp) != 0 {
			t.Errorf("%s: spoke %+v", why, sp)
		}
	}
}

// The server fails partway through "Let me check the garage door" and says
// so in-band, on the 200 it already sent. What was said stays said and its
// utterance is closed; the turn ends in error with vLLM's own words and the
// partial completion (SPEC §7, ADR-0051).
//
// verifies SPEC §7, §8
func TestAServerErrorMidStreamEndsTheTurnWithWhatItSaid(t *testing.T) {
	acts, err := replay(t, "error_mid_stream.sse")
	if err == nil || !strings.Contains(err.Error(), "EngineCore encountered an issue") {
		t.Fatalf("err = %v, want vLLM's message", err)
	}
	if got := kinds(acts); !slices.Equal(got, []string{"speech", "speech", "speech:last", "end:error"}) {
		t.Fatalf("actions = %v", got)
	}
	end := endOf(t, acts)
	if !strings.Contains(end.Error, "EngineCore") || !strings.Contains(end.Completion, "Let me check the gar") {
		t.Errorf("end = %+v, want the error and the partial completion", end)
	}
}

// The older servers' error is the error object itself.
func TestAnOlderServersErrorIsReported(t *testing.T) {
	stream := sse(`{"object":"error","message":"The model does not support tools","type":"BadRequestError","param":null,"code":400}`)
	_, err := collect(t, context.Background(), strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "does not support tools") {
		t.Fatalf("err = %v", err)
	}
}

// A stream that stops without a finish reason is a dropped connection;
// [DONE] before one is a server that gave up. Neither is recorded as a
// finished turn.
//
// verifies SPEC §7, §8
func TestAStreamThatNeverFinishesIsAnError(t *testing.T) {
	speakOpen := `{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"chatcmpl-tool-cut","type":"function","index":0,"function":{"name":"speak","arguments":"{\"text\": \"The porch light is"}}]},"finish_reason":null}]}`
	for name, c := range map[string]struct {
		stream string
		is     error
		says   string
	}{
		"dropped":          {stream: sse(speakOpen), is: io.ErrUnexpectedEOF},
		"done too soon":    {stream: sse(speakOpen, "[DONE]"), says: "before the model finished"},
		"aborted":          {stream: sse(speakOpen, `{"choices":[{"index":0,"delta":{},"finish_reason":"abort"}]}`), says: `"abort"`},
		"half a JSON line": {stream: "data: {\"choices\":[{\"index\":0,\n\n", says: "decode chat chunk"},
	} {
		acts, err := collect(t, context.Background(), strings.NewReader(c.stream))
		if c.is != nil && !errors.Is(err, c.is) || c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)) {
			t.Errorf("%s: err = %v", name, err)
		}
		end := endOf(t, acts)
		if end.FinishReason != session.FinishError {
			t.Errorf("%s: finish = %q, want error", name, end.FinishReason)
		}
		if sp := speechOf(acts); len(sp) > 0 && !sp[len(sp)-1].Last {
			t.Errorf("%s: the utterance was left open", name)
		}
	}
}

// A barge-in cancels the turn mid-speech. Whatever the stream does next,
// the utterance it opened is closed and the turn still reports its end, so
// the session is not left waiting on either. Run many times: the cancel
// races the stream.
//
// verifies SPEC §4.4
func TestACancelledTurnClosesItsUtteranceAndReportsItsEnd(t *testing.T) {
	stream := fixture(t, "speak_and_two_lights.sse")
	for i := range 50 {
		ctx, cancel := context.WithCancel(context.Background())
		pr, pw := io.Pipe()
		go func() {
			_, _ = io.WriteString(pw, stream[:strings.Index(stream, "por")])
			<-ctx.Done()
			_ = pw.CloseWithError(ctx.Err())
		}()
		out := make(chan session.Action)
		go func() {
			defer close(out)
			_ = decoder{}.decode(ctx, pr, out)
		}()
		var acts []session.Action
		for a := range out {
			acts = append(acts, a)
			if len(acts) == 1 {
				cancel()
			}
		}
		if end := endOf(t, acts); end.FinishReason != session.FinishError {
			t.Fatalf("run %d: finish = %q, want error", i, end.FinishReason)
		}
		if sp := speechOf(acts); len(sp) == 0 || !sp[len(sp)-1].Last {
			t.Fatalf("run %d: the cancelled turn left its utterance open: %v", i, kinds(acts))
		}
		cancel()
	}
}

// A turn that ended normally reports one end and no failure.
func TestASuccessfulTurnReportsOneEnd(t *testing.T) {
	acts, err := replay(t, "reply.sse")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var ends int
	for _, a := range acts {
		if _, ok := a.(session.TurnEnd); ok {
			ends++
		}
	}
	if ends != 1 || endOf(t, acts).FinishReason != "tool_calls" {
		t.Errorf("ends = %d, %v", ends, kinds(acts))
	}
}

// sse frames payloads as server-sent events.
func sse(payloads ...string) string {
	var b strings.Builder
	for _, p := range payloads {
		b.WriteString("data: " + p + "\n\n")
	}
	return b.String()
}
