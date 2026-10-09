package rerun_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/provider/ollama"
	"github.com/teaganglenn/chorus/internal/rerun"
	"github.com/teaganglenn/chorus/internal/session"
)

func record(kind journal.Kind, fields ...string) journal.Record {
	r := journal.Record{Kind: kind, Fields: map[string]string{}}
	for i := 0; i+1 < len(fields); i += 2 {
		r.Fields[fields[i]] = fields[i+1]
	}
	if journal.Meta[kind].HasAudio {
		r.AudioRef = "blob://" + string(kind)
	}
	return r
}

// garageLog is Teagan in the kitchen: two errands in one breath, the answer
// cut by "just close it", and the turn that closed the door.
func garageLog(t *testing.T, extra ...journal.Record) []journal.Event {
	t.Helper()
	return logOf(t, append(garageRecords(), extra...))
}

func garageRecords() []journal.Record {
	return []journal.Record{
		record(journal.KindSessionOpened, "satellite", "kitchen", "speaker_id", "teagan", "resumed", "false"),
		record(journal.KindUtteranceTranscribed, "text", "turn off the kitchen lights and is the garage door closed", "speaker_id", "teagan"),
		record(journal.KindToolCalled, "tool", "speak", "call_id", "s1", "args_json", `{"mode":"queue","streamed":true}`),
		record(journal.KindSpeechSpoken, "text", "One sec.", "frames_played", "11200"),
		record(journal.KindToolCalled, "tool", "ha_call_service", "call_id", "c1", "args_json", `{"domain":"light","service":"turn_off","area_id":"kitchen"}`),
		record(journal.KindToolCalled, "tool", "ha_get_state", "call_id", "c2", "args_json", `{"entity_id":"cover.garage_door"}`),
		record(journal.KindToolResult, "call_id", "c1", "outcome", "ok", "result_json", `{}`),
		record(journal.KindToolResult, "call_id", "c2", "outcome", "ok", "result_json", `{"state":"open"}`),
		record(journal.KindToolCalled, "tool", "speak", "call_id", "s2", "args_json", `{"mode":"queue","streamed":true}`),
		record(journal.KindSpeechTruncated, "spoken_text", "Kitchen lights are off. The garage door is", "unspoken_text", " open, want me to close it?", "frames_played", "38400", "tts_position_ms", "2400"),
		record(journal.KindModelCompleted, "completion_json", "{}", "finish_reason", "stop"),
		record(journal.KindUtteranceTranscribed, "text", "just close it", "speaker_id", "teagan"),
		record(journal.KindToolCalled, "tool", "ha_call_service", "call_id", "c3", "args_json", `{"domain":"cover","service":"close_cover","entity_id":"cover.garage_door"}`),
		record(journal.KindToolCalled, "tool", "speak", "call_id", "s3", "args_json", `{"mode":"queue","streamed":true}`),
		record(journal.KindSpeechSpoken, "text", "Closing the garage door.", "frames_played", "24000"),
		record(journal.KindModelCompleted, "completion_json", "{}", "finish_reason", "stop"),
	}
}

// logOf writes records through a real journal, so they carry the stamps a
// session's log would.
func logOf(t *testing.T, recs []journal.Record) []journal.Event {
	t.Helper()
	store := journal.NewMemStore()
	v := journal.Versions{Model: "qwen3:32b", Prompt: "sys@3", ToolSchema: "tools@7"}
	j := journal.New(store, journal.FixedClock(time.Unix(1_760_000_000, 0)), v)
	for _, r := range recs {
		if _, err := j.Append(context.Background(), "conv-garage", r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
	events, err := store.Events(context.Background(), "conv-garage")
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func turns(t *testing.T, events []journal.Event) []rerun.Turn {
	t.Helper()
	ts, err := rerun.Turns(events)
	if err != nil {
		t.Fatalf("turns: %v", err)
	}
	return ts
}

// scripted is a turn engine that answers each transcript from a script, the
// way a model would stream it: speech deltas under a call id, tool calls,
// then the end of the turn. It keeps what it was asked.
type scripted struct {
	answers map[string][]session.Action
	asked   []session.Input
}

func (s *scripted) Turn(_ context.Context, in session.Input) (<-chan session.Action, error) {
	s.asked = append(s.asked, in)
	out := make(chan session.Action, len(s.answers[in.Text])+1)
	for _, a := range s.answers[in.Text] {
		out <- a
	}
	close(out)
	return out, nil
}

func say(id string, parts ...string) []session.Action {
	var out []session.Action
	for i, p := range parts {
		out = append(out, session.SpeechDelta{CallID: id, Text: p, Mode: session.ModeQueue, Last: i == len(parts)-1})
	}
	return out
}

func then(groups ...[]session.Action) []session.Action {
	var out []session.Action
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func call(id, tool, args string) []session.Action {
	return []session.Action{session.ToolCall{ID: id, Tool: tool, Args: args}}
}

func end(reason string) []session.Action {
	return []session.Action{session.TurnEnd{FinishReason: reason, Completion: "{}"}}
}

// verifies SPEC §9.2
func TestEachUtteranceIsATurnWithWhatTheAssistantDid(t *testing.T) {
	ts := turns(t, garageLog(t))
	if len(ts) != 2 {
		t.Fatalf("got %d turns, want 2", len(ts))
	}
	first := ts[0]
	if first.Seq != 2 || first.Speaker != "teagan" || first.Text != "turn off the kitchen lights and is the garage door closed" {
		t.Errorf("first turn = #%d %s %q", first.Seq, first.Speaker, first.Text)
	}
	if first.Versions.Prompt != "sys@3" || first.Versions.Model != "qwen3:32b" {
		t.Errorf("versions = %+v, want the ones the turn ran under", first.Versions)
	}
	want := []rerun.Speech{{Text: "One sec."}, {Text: "Kitchen lights are off. The garage door is", Unheard: " open, want me to close it?"}}
	if len(first.Recorded.Speech) != 2 || first.Recorded.Speech[0] != want[0] || first.Recorded.Speech[1] != want[1] {
		t.Errorf("speech = %+v, want %+v", first.Recorded.Speech, want)
	}
	// speak is how it talks, not something it did.
	if got := first.Recorded.Calls; len(got) != 2 || got[0].Tool != "ha_call_service" || got[1].Tool != "ha_get_state" {
		t.Errorf("calls = %+v, want the light and the garage door", got)
	}
	if first.Recorded.Finish != "stop" {
		t.Errorf("finish = %q", first.Recorded.Finish)
	}
	if ts[1].Text != "just close it" || len(ts[1].Recorded.Calls) != 1 {
		t.Errorf("second turn = %+v", ts[1])
	}
}

// A late tool result from the first turn lands in the second; it is not
// something the second turn decided.
//
// verifies SPEC §9.2
func TestResultsAndSpeculationAreNotPartOfATake(t *testing.T) {
	spec := record(journal.KindToolCalled, "tool", "media_search", "call_id", "x1", "args_json", `{"query":"garage door"}`)
	events := garageLog(t)
	n := uint64(len(events))
	events = append(events, journal.Event{
		Seq: n + 1, At: events[0].At, ConversationID: "conv-garage", Kind: spec.Kind, Fields: spec.Fields,
		Versions: events[0].Versions, Speculative: true,
	})
	ts := turns(t, events)
	if got := ts[1].Recorded.Calls; len(got) != 1 || got[0].Tool != "ha_call_service" {
		t.Errorf("second turn's calls = %+v, want only the cover it closed", got)
	}
}

// verifies SPEC §9.2
func TestTheSpeakerIsWhoeverTheTurnHeard(t *testing.T) {
	events := garageLog(t,
		record(journal.KindUtteranceTranscribed, "text", "and the porch light", "speaker_id", "alan"),
		record(journal.KindUtteranceTranscribed, "text", "thanks", "speaker_id", ""),
	)
	ts := turns(t, events)
	if ts[2].Speaker != "alan" {
		t.Errorf("third turn spoke to %q, want alan", ts[2].Speaker)
	}
	// An abstaining speaker ID keeps the attribution the state already had.
	if ts[3].Speaker != "alan" {
		t.Errorf("fourth turn spoke to %q, want alan still", ts[3].Speaker)
	}
}

// verifies SPEC §9.2
func TestARerunUnderTheRecordedBehaviourChangesNothing(t *testing.T) {
	ts := turns(t, garageLog(t))
	eng := &scripted{answers: map[string][]session.Action{
		"turn off the kitchen lights and is the garage door closed": then(
			say("s1", "One ", "sec."),
			// Same call, keys in a different order: models do that.
			call("c1", "ha_call_service", `{"area_id":"kitchen","service":"turn_off","domain":"light"}`),
			call("c2", "ha_get_state", `{"entity_id":"cover.garage_door"}`),
			say("s2", "Kitchen lights are off. ", "The garage door is open, want me to close it?"),
			end("stop"),
		),
	}}
	got, err := rerun.Run(context.Background(), eng, "conv-garage", ts[0])
	if err != nil {
		t.Fatal(err)
	}
	if c := rerun.Compare(ts[0].Recorded, got); c.Speech || c.Calls {
		t.Errorf("change = %+v, want none: recorded %+v, replayed %+v", c, ts[0].Recorded, got)
	}
	if in := eng.asked[0]; in.Speaker != "teagan" || in.ConversationID != "conv-garage" {
		t.Errorf("engine asked with %+v", in)
	}
}

// verifies SPEC §9.2
func TestAShorterAnswerIsASpeechChangeOnly(t *testing.T) {
	ts := turns(t, garageLog(t))
	eng := &scripted{answers: map[string][]session.Action{
		"turn off the kitchen lights and is the garage door closed": then(
			call("c1", "ha_call_service", `{"domain":"light","service":"turn_off","area_id":"kitchen"}`),
			call("c2", "ha_get_state", `{"entity_id":"cover.garage_door"}`),
			say("s1", "Lights off. Garage is open."),
			end("stop"),
		),
	}}
	got, err := rerun.Run(context.Background(), eng, "conv-garage", ts[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.Said() != "Lights off. Garage is open." {
		t.Errorf("said %q", got.Said())
	}
	if c := rerun.Compare(ts[0].Recorded, got); !c.Speech || c.Calls {
		t.Errorf("change = %+v, want speech only", c)
	}
}

// verifies SPEC §9.2
func TestCallingADifferentEntityIsACallChange(t *testing.T) {
	ts := turns(t, garageLog(t))
	eng := &scripted{answers: map[string][]session.Action{
		"just close it": then(
			call("c3", "ha_call_service", `{"domain":"light","service":"turn_off","area_id":"kitchen"}`),
			say("s3", "Closing the garage door."),
			end("stop"),
		),
	}}
	got, err := rerun.Run(context.Background(), eng, "conv-garage", ts[1])
	if err != nil {
		t.Fatal(err)
	}
	if c := rerun.Compare(ts[1].Recorded, got); c.Speech || !c.Calls {
		t.Errorf("change = %+v, want the calls only", c)
	}
}

// A stream that breaks after the model started answering is a failed
// re-run, not a different answer: the endpoint said 200, then died.
//
// verifies SPEC §7
func TestAModelThatFailsMidAnswerIsAFailedRerun(t *testing.T) {
	ts := turns(t, garageLog(t))
	eng := &scripted{answers: map[string][]session.Action{"just close it": then(
		call("c3", "ha_call_service", `{"domain":"cover","service":"close_cover","entity_id":"cover.garage_door"}`),
		[]session.Action{session.TurnEnd{FinishReason: "error", Completion: `{"content":"","error":"read chat stream: unexpected EOF"}`}},
	)}}
	got, err := rerun.Run(context.Background(), eng, "conv-garage", ts[1])
	var failed *rerun.Failed
	if !errors.As(err, &failed) || failed.Reason != "read chat stream: unexpected EOF" {
		t.Fatalf("err = %v, want a failed re-run naming the broken stream", err)
	}
	// What it managed before it died is kept, for whoever reads the error.
	if len(got.Calls) != 1 || got.Finish != "error" {
		t.Errorf("partial take = %+v", got)
	}
}

// replies serves one canned /api/chat stream in-process, the shape a real
// Ollama answers with, and keeps the request it was sent.
type replies struct {
	stream string
	sent   []byte
}

func (r *replies) RoundTrip(req *http.Request) (*http.Response, error) {
	r.sent, _ = io.ReadAll(req.Body)
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{}, Request: req,
		Body: io.NopCloser(strings.NewReader(r.stream)),
	}, nil
}

// The take a real decoder makes of a real stream: the edited prompt is what
// the model is sent, speak becomes speech, and the light is the same call.
//
// verifies SPEC §9.2
func TestARerunThroughTheOllamaEngineSendsTheEditedPrompt(t *testing.T) {
	ts := turns(t, garageLog(t))
	rt := &replies{stream: strings.Join([]string{
		`{"model":"qwen3:32b","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"index":0,"name":"ha_call_service","arguments":{"domain":"light","service":"turn_off","area_id":"kitchen"}}}]},"done":false}`,
		`{"model":"qwen3:32b","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_2","function":{"index":1,"name":"ha_get_state","arguments":{"entity_id":"cover.garage_door"}}}]},"done":false}`,
		`{"model":"qwen3:32b","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_3","function":{"index":2,"name":"speak","arguments":{"mode":"queue","text":"Lights off. Garage is open."}}}]},"done":false}`,
		`{"model":"qwen3:32b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`,
	}, "\n") + "\n"}
	prompt := ollama.DefaultPrompt + "\n\nConfirm what you did in five words or fewer."
	eng, err := ollama.New(ollama.Config{
		BaseURL: "http://ollama.invalid", Model: "qwen3:32b", Prompt: prompt,
		HTTP: &http.Client{Transport: rt},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := rerun.Run(context.Background(), eng, "conv-garage", ts[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rt.sent), "five words or fewer") || !strings.Contains(string(rt.sent), "You are speaking with teagan.") {
		t.Errorf("request did not carry the edited prompt and the speaker:\n%s", rt.sent)
	}
	if got.Said() != "Lights off. Garage is open." || got.Finish != "stop" {
		t.Errorf("take = %+v", got)
	}
	if c := rerun.Compare(ts[0].Recorded, got); !c.Speech || c.Calls {
		t.Errorf("change = %+v, want speech only", c)
	}
}

// Ollama answers 200 and then reports the runner dying in the stream.
//
// verifies SPEC §7
func TestAnOllamaRunnerThatDiesMidStreamFailsTheRerun(t *testing.T) {
	ts := turns(t, garageLog(t))
	rt := &replies{stream: strings.Join([]string{
		`{"model":"qwen3:32b","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"index":0,"name":"ha_call_service","arguments":{"domain":"light","service":"turn_off","area_id":"kitchen"}}}]},"done":false}`,
		`{"error":"llama runner process has terminated: signal: killed"}`,
	}, "\n") + "\n"}
	eng, err := ollama.New(ollama.Config{BaseURL: "http://ollama.invalid", Model: "qwen3:32b", HTTP: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = rerun.Run(context.Background(), eng, "conv-garage", ts[0])
	if err == nil || !strings.Contains(err.Error(), "llama runner process has terminated") {
		t.Errorf("err = %v, want the runner's death", err)
	}
}

// Teagan's garage question under the session that asks again: the first ask
// said it was checking and read the cover; the follow-up answered from the
// state. A re-run asks with the transcript alone, so only the first ask is
// what it can be compared with.
//
// verifies SPEC §9.2
func TestATakeIsTheTurnsFirstAsk(t *testing.T) {
	events := garageLog(t,
		record(journal.KindUtteranceTranscribed, "text", "is the porch light on", "speaker_id", "teagan"),
		record(journal.KindToolCalled, "tool", "speak", "call_id", "s4", "args_json", `{"mode":"queue","streamed":true,"text":"Let me check."}`),
		record(journal.KindToolCalled, "tool", "ha_get_state", "call_id", "c4", "args_json", `{"entity_id":"light.porch"}`),
		record(journal.KindModelCompleted, "completion_json", "{}", "finish_reason", "stop"),
		record(journal.KindToolResult, "call_id", "c4", "outcome", "ok", "result_json", `{"state":"on"}`),
		record(journal.KindSpeechSpoken, "text", "Let me check.", "frames_played", "19200", "call_id", "s4"),
		record(journal.KindToolResult, "call_id", "s4", "outcome", "ok"),
		record(journal.KindToolCalled, "tool", "speak", "call_id", "s5", "args_json", `{"mode":"queue","streamed":true,"text":"Yes, the porch light is on."}`),
		record(journal.KindToolCalled, "tool", "ha_call_service", "call_id", "c5", "args_json", `{"domain":"light","service":"turn_off","entity_id":"light.porch"}`),
		record(journal.KindModelCompleted, "completion_json", "{}", "finish_reason", "length"),
		record(journal.KindSpeechSpoken, "text", "Yes, the porch light is on.", "frames_played", "38400", "call_id", "s5"),
	)
	porch := turns(t, events)[2]
	if got := porch.Recorded.Speech; len(got) != 1 || got[0].Text != "Let me check." {
		t.Errorf("speech = %+v, want only the first ask's", got)
	}
	if got := porch.Recorded.Calls; len(got) != 1 || got[0].Tool != "ha_get_state" {
		t.Errorf("calls = %+v, want only the first ask's", got)
	}
	if porch.Recorded.Finish != "stop" {
		t.Errorf("finish = %q, want the first ask's", porch.Recorded.Finish)
	}
}
