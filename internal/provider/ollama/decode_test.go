package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/session"
)

// Fixtures are real /api/chat streams captured from Ollama against the
// generated tool schema, so a wire change breaks a test rather than the house.
func replay(t *testing.T, name string, d decoder) ([]session.Action, error) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	return collect(t, d, f)
}

// collect drains concurrently, as the session does. An unread channel would
// deadlock: decode emits per content delta and a long turn outruns any buffer.
func collect(t *testing.T, d decoder, r io.Reader) ([]session.Action, error) {
	t.Helper()
	return collectCtx(t, context.Background(), d, r)
}

func collectCtx(t *testing.T, ctx context.Context, d decoder, r io.Reader) ([]session.Action, error) {
	t.Helper()
	out := make(chan session.Action)
	done := make(chan []session.Action, 1)
	go func() {
		var got []session.Action
		for a := range out {
			got = append(got, a)
		}
		done <- got
	}()
	err := d.decode(ctx, r, out)
	close(out)
	return <-done, err
}

func speechOf(acts []session.Action) []session.SpeechDelta {
	var out []session.SpeechDelta
	for _, a := range acts {
		if s, ok := a.(session.SpeechDelta); ok {
			out = append(out, s)
		}
	}
	return out
}

func callsOf(acts []session.Action) []session.ToolCall {
	var out []session.ToolCall
	for _, a := range acts {
		if c, ok := a.(session.ToolCall); ok {
			out = append(out, c)
		}
	}
	return out
}

func kinds(acts []session.Action) []string {
	out := make([]string, len(acts))
	for i, a := range acts {
		switch v := a.(type) {
		case session.SpeechDelta:
			out[i] = "speech"
		case session.ToolCall:
			out[i] = "call:" + v.Tool
		case session.TurnEnd:
			out[i] = "end"
		}
	}
	return out
}

// A speak call is one whole utterance, so it is one delta and it is the last.
//
// verifies SPEC §4.1
func TestASpeakCallBecomesOneFinalDelta(t *testing.T) {
	acts, err := replay(t, "reply.ndjson", decoder{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sp := speechOf(acts)
	if len(sp) != 1 {
		t.Fatalf("got %d deltas, want 1: %v", len(sp), kinds(acts))
	}
	if sp[0].Text != "Hello!" {
		t.Errorf("text = %q", sp[0].Text)
	}
	if !sp[0].Last {
		t.Error("a whole-utterance call must mark the delta final")
	}
	if sp[0].CallID == "" {
		t.Error("the call's own id must group the utterance")
	}
	if sp[0].Mode != session.ModeQueue {
		t.Errorf("mode = %q, want queue", sp[0].Mode)
	}
}

// The model is asked to speak before a slow tool so the user is not left in
// silence. Arrival order is the dispatch order, so the decoder must not
// reorder or buffer.
//
// verifies SPEC §4.1, §14
func TestSpeechReachesTheSessionBeforeTheSlowToolItCovers(t *testing.T) {
	acts, err := replay(t, "speak_then_slow_tool.ndjson", decoder{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, want := kinds(acts), []string{"speech", "call:media_search", "end"}; !equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}

	call := callsOf(acts)[0]
	// Arguments arrive as a JSON object and must stay valid JSON for the
	// registry, which invokes tools with the raw string.
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Args), &args); err != nil {
		t.Fatalf("args %q are not JSON: %v", call.Args, err)
	}
	if args["query"] == "" || args["query"] == nil {
		t.Errorf("query missing from %v", args)
	}
	if call.ID == "" {
		t.Error("a tool call needs its id to correlate the result")
	}
}

// An out-of-enum mode must not reach the speech channel: preempt and interject
// discard or duck speech the user is still hearing.
//
// verifies SPEC §4.2
func TestAnUnknownModeFallsBackToQueue(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "mode_out_of_enum.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	// Guards the fixture itself: it is only evidence if the model really did
	// emit a mode outside the schema's enum.
	if !strings.Contains(string(raw), `"mode":"filler"`) {
		t.Fatal("fixture no longer carries an out-of-enum mode")
	}

	acts, err := replay(t, "mode_out_of_enum.ndjson", decoder{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sp := speechOf(acts)
	if len(sp) != 1 {
		t.Fatalf("got %d deltas, want 1", len(sp))
	}
	if sp[0].Mode != session.ModeQueue {
		t.Errorf("mode = %q, want queue", sp[0].Mode)
	}
}

// Reasoning emitted as ordinary content must never become speech. This fixture
// is a model reasoning aloud in content with think:false and calling nothing.
//
// verifies SPEC §4.1
func TestReasoningInContentIsNotSpoken(t *testing.T) {
	acts, err := replay(t, "content_leak.ndjson", decoder{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sp := speechOf(acts); len(sp) != 0 {
		t.Fatalf("spoke %d deltas of the model's reasoning, e.g. %q", len(sp), sp[0].Text)
	}

	end, ok := acts[len(acts)-1].(session.TurnEnd)
	if !ok {
		t.Fatalf("last action is %T, want TurnEnd", acts[len(acts)-1])
	}
	// Retained for the journal even though it is never spoken: replay cannot
	// regenerate the raw completion (SPEC §8).
	if !strings.Contains(end.Completion, "the user is asking") {
		t.Error("the raw completion must keep content the turn did not speak")
	}
	if end.FinishReason != "length" {
		t.Errorf("finish reason = %q, want length", end.FinishReason)
	}
}

// Opting in is what SPEC §4.1 describes for templates that emit content beside
// tool_calls. The same fixture then speaks, which is why the default is off.
//
// verifies SPEC §4.1
func TestInlineContentIsSpokenOnlyWhenEnabled(t *testing.T) {
	acts, err := replay(t, "content_leak.ndjson", decoder{speakInlineContent: true})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sp := speechOf(acts)
	if len(sp) == 0 {
		t.Fatal("opting in produced no speech")
	}
	// No call id: the session assigns the implicit one.
	if sp[0].CallID != "" {
		t.Errorf("inline content must carry no call id, got %q", sp[0].CallID)
	}
	if sp[0].Mode != session.ModeQueue {
		t.Errorf("mode = %q, want queue", sp[0].Mode)
	}
	// Without a final delta the speech channel never closes the utterance, so
	// the stream stays open and no playback is ever reported.
	if last := sp[len(sp)-1]; !last.Last {
		t.Error("streamed inline content never closes its utterance")
	}
	for _, s := range sp[:len(sp)-1] {
		if s.Last {
			t.Error("only the closing delta may be final")
		}
	}
}

// A turn that speaks nothing inline must not invent an empty utterance: the
// session would open and journal a speak that never had any text.
//
// verifies SPEC §4.1
func TestNoInlineContentOpensNoUtterance(t *testing.T) {
	acts, err := replay(t, "speak_then_slow_tool.ndjson", decoder{speakInlineContent: true})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The one delta is the speak call's own, which already closes itself.
	if got := len(speechOf(acts)); got != 1 {
		t.Fatalf("got %d deltas, want only the speak call's: %v", got, kinds(acts))
	}
}

// Reasoning the endpoint separates into its own field is not speech either,
// and must not be mistaken for the utterance beside it.
//
// verifies SPEC §4.1
func TestSeparateThinkingIsNotSpoken(t *testing.T) {
	acts, err := replay(t, "thinking.ndjson", decoder{speakInlineContent: true})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, s := range speechOf(acts) {
		if strings.Contains(s.Text, "user") || len(s.Text) > 64 {
			t.Errorf("reasoning reached speech: %q", s.Text)
		}
	}
	if len(speechOf(acts)) != 1 {
		t.Fatalf("got %d deltas, want the one spoken utterance", len(speechOf(acts)))
	}
}

// A tool-only turn still has a completion. completion_json is a required
// journal field, so an empty one fails an otherwise successful turn
// (internal/journal/journal.go), and content alone is empty whenever the model
// did nothing but call tools.
//
// verifies SPEC §8
func TestAToolOnlyTurnStillRecordsItsCompletion(t *testing.T) {
	acts, err := replay(t, "speak_then_slow_tool.ndjson", decoder{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	end, ok := acts[len(acts)-1].(session.TurnEnd)
	if !ok {
		t.Fatalf("last action is %T, want TurnEnd", acts[len(acts)-1])
	}
	if end.Completion == "" {
		t.Fatal("empty completion: the journal rejects the event and the turn fails")
	}

	var got struct {
		Content   string `json:"content"`
		ToolCalls []struct {
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(end.Completion), &got); err != nil {
		t.Fatalf("completion %q is not JSON: %v", end.Completion, err)
	}
	if got.Content != "" {
		t.Errorf("content = %q, want empty for this fixture", got.Content)
	}
	// The calls are the turn: dropping them loses what the model actually did.
	var names []string
	for _, c := range got.ToolCalls {
		names = append(names, c.Function.Name)
		if len(c.Function.Arguments) == 0 {
			t.Errorf("%s recorded with no arguments", c.Function.Name)
		}
	}
	if want := []string{"speak", "media_search"}; !equal(names, want) {
		t.Errorf("recorded calls = %v, want %v", names, want)
	}
}

// Reasoning the endpoint separates out is not spoken, but it is still part of
// the raw completion the journal keeps.
//
// verifies SPEC §8
func TestThinkingIsRecordedInTheCompletion(t *testing.T) {
	acts, err := replay(t, "thinking.ndjson", decoder{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	end := acts[len(acts)-1].(session.TurnEnd)
	var got struct {
		Thinking string `json:"thinking"`
	}
	if err := json.Unmarshal([]byte(end.Completion), &got); err != nil {
		t.Fatalf("completion is not JSON: %v", err)
	}
	if got.Thinking == "" {
		t.Error("the completion dropped the model's reasoning")
	}
}

// A connection that drops mid-utterance must still close it. speechChannel.play
// waits on the session context, not the turn's, so an unclosed utterance keeps
// the channel busy and blocks waitIdle until the session itself ends.
//
// verifies SPEC §4.1
func TestADroppedStreamStillClosesInlineSpeech(t *testing.T) {
	body := `{"message":{"role":"assistant","content":"one moment"},"done":false}` + "\n"
	acts, err := collect(t, decoder{speakInlineContent: true}, strings.NewReader(body))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want unexpected EOF", err)
	}
	sp := speechOf(acts)
	if len(sp) == 0 {
		t.Fatal("no speech at all")
	}
	if last := sp[len(sp)-1]; !last.Last {
		t.Error("a dropped stream left the utterance open, hanging the session")
	}
}

// A turn that dies mid-stream still has to appear in the log. Turn's error
// return is spent before the first chunk, so a dropped connection would
// otherwise leave a turn with speech recorded and no model_completed at all.
//
// verifies SPEC §7, §8
func TestAFailedTurnIsStillRecorded(t *testing.T) {
	body := `{"message":{"role":"assistant","content":"half a th"},"done":false}` + "\n"
	acts, err := collect(t, decoder{}, strings.NewReader(body))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want the failure to still be reported", err)
	}
	end, ok := acts[len(acts)-1].(session.TurnEnd)
	if !ok {
		t.Fatalf("last action is %T, want a TurnEnd for the failed turn", acts[len(acts)-1])
	}
	if end.FinishReason != "error" {
		t.Errorf("finish reason = %q, want error", end.FinishReason)
	}
	var got struct {
		Content string `json:"content"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(end.Completion), &got); err != nil {
		t.Fatalf("completion %q is not JSON: %v", end.Completion, err)
	}
	// Both halves: what the model produced, and why it stopped.
	if got.Content != "half a th" {
		t.Errorf("partial content = %q, want it kept", got.Content)
	}
	if got.Error == "" {
		t.Error("the completion does not say why the turn stopped")
	}
}

// Cancellation is how a turn normally dies -- barge-in cancels it -- so the
// terminal event cannot be guarded by the cancelled context that caused it. A
// select between the send and ctx.Done() picks either when both are ready, so
// the only record of the turn would be lost about half the time.
//
// verifies SPEC §7, §8
func TestACancelledTurnStillReportsItsEnd(t *testing.T) {
	body := `{"message":{"role":"assistant","content":"half a th"},"done":false}` + "\n"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Repeated because what this guards is a race: a single run can win the
	// coin flip and report a passing turn on broken code.
	for i := range 64 {
		acts, err := collectCtx(t, ctx, decoder{speakInlineContent: true}, strings.NewReader(body))
		if err == nil {
			t.Fatalf("run %d: a truncated stream must still fail", i)
		}
		if len(acts) == 0 {
			t.Fatalf("run %d: the turn vanished entirely", i)
		}
		end, ok := acts[len(acts)-1].(session.TurnEnd)
		if !ok {
			t.Fatalf("run %d: last action is %T, want TurnEnd", i, acts[len(acts)-1])
		}
		if end.FinishReason != finishError {
			t.Fatalf("run %d: finish reason = %q, want %q", i, end.FinishReason, finishError)
		}
		// Speech itself may be dropped here -- a cancelled turn is not heard,
		// and the session discards post-cancel deltas anyway. What must not
		// happen is an utterance left open: speechChannel would keep the
		// session busy for as long as it lives.
		if sp := speechOf(acts); len(sp) > 0 && !sp[len(sp)-1].Last {
			t.Fatalf("run %d: the cancelled turn left its utterance open", i)
		}
	}
}

// A turn that ended normally must not also report a failure.
func TestASuccessfulTurnReportsOneEnd(t *testing.T) {
	acts, err := replay(t, "reply.ndjson", decoder{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var ends int
	for _, a := range acts {
		if e, ok := a.(session.TurnEnd); ok {
			ends++
			if e.FinishReason == "error" {
				t.Error("a successful turn reported an error finish")
			}
		}
	}
	if ends != 1 {
		t.Errorf("got %d TurnEnds, want 1: %v", ends, kinds(acts))
	}
}

// A stream that ends without done is a dropped connection. Reporting success
// would record a turn that never finished as complete.
func TestATruncatedStreamIsAnError(t *testing.T) {
	body := `{"message":{"role":"assistant","content":""},"done":false}` + "\n"
	_, err := collect(t, decoder{}, strings.NewReader(body))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want unexpected EOF", err)
	}
}

// An error arrives in-band on a 200 response, so it has to be read off the
// stream rather than the status code.
func TestAnInBandErrorIsReported(t *testing.T) {
	body := `{"error":"model requires more system memory"}` + "\n"
	_, err := collect(t, decoder{}, strings.NewReader(body))
	if err == nil || !strings.Contains(err.Error(), "more system memory") {
		t.Fatalf("err = %v, want the endpoint's message", err)
	}
}

func TestMalformedJSONIsReported(t *testing.T) {
	_, err := collect(t, decoder{}, strings.NewReader("{not json\n"))
	if err == nil || !strings.Contains(err.Error(), "decode chat chunk") {
		t.Fatalf("err = %v, want a decode failure", err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
