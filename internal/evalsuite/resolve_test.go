package evalsuite_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/evalsuite"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/rerun"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// day is the household's Thursday, as chorusd journalled it.
func day(t *testing.T) *journal.MemStore {
	t.Helper()
	store, err := household.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// daysContexts resolves the day's made-up labels, then a build's.
func daysContexts() contexts.Source { return contexts.Chain{household.Contexts(), contexts.Default()} }

func parse(t *testing.T, yaml string) evalsuite.Suite {
	t.Helper()
	s, err := evalsuite.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return s
}

// A ref the log does not hold is a mistake in the suite, so the run stops
// and says what the log does hold there.
//
// verifies SPEC §8, §13
func TestARefTheLogDoesNotHoldIsRefusedWithTheReason(t *testing.T) {
	s := parse(t, `
name: refs
cases:
  - {id: gone, ref: conv-0101-attic/2, expect: {says: {max_words: 5}}}
  - {id: not-a-turn, ref: conv-1858-kitchen/4, expect: {says: {max_words: 5}}}
  - {id: past-the-end, ref: conv-2104-office/99, expect: {says: {max_words: 5}}}
  - {id: fine, ref: conv-2104-office/2, expect: {says: {max_words: 5}}}
`)
	_, err := evalsuite.Resolve(context.Background(), day(t), daysContexts(), s)
	if err == nil {
		t.Fatal("resolved")
	}
	for _, want := range []string{
		"case gone: conv-0101-attic has no log",
		"case not-a-turn: conv-1858-kitchen/4 is tool_called, not an utterance",
		`#2 "unlock the front door"`,
		"case past-the-end: conv-2104-office has no event #99",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v\nwant it to say %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "case fine") {
		t.Errorf("err = %v, names a case that resolves", err)
	}
}

// A case is asked what the live session's first ask was: what Replay asks
// (internal/rerun), plus the dialogue the reducer folded through the
// utterance. "yes" means nothing without the question before it.
//
// verifies SPEC §4.4, §8, §13
func TestACaseIsAskedWhatRerunAsksWithTheDialogue(t *testing.T) {
	store := day(t)
	s := parse(t, `
name: parity
cases:
  - {id: door, ref: conv-1858-kitchen/2, expect: {asks_confirmation: true}}
  - {id: door-yes, ref: conv-1858-kitchen/12, expect: {asks_confirmation: false}}
  - {id: pasta-cancel, ref: conv-1748-kitchen/10, expect: {tool_called: [{tool: timer_cancel}]}}
`)
	got, err := evalsuite.Resolve(context.Background(), store, daysContexts(), s)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		ref := r.Case.Turn()
		events, _ := store.Events(context.Background(), ref.Conversation)
		turns, err := rerun.Turns(events)
		if err != nil {
			t.Fatal(err)
		}
		var turn rerun.Turn
		for _, tn := range turns {
			if tn.Seq == ref.Seq {
				turn = tn
			}
		}
		if !reflect.DeepEqual(r.Turn, turn) {
			t.Errorf("%s: turn = %+v, want rerun's %+v", r.Case.ID, r.Turn, turn)
		}
		want := rerun.Input(ref.Conversation, turn)
		want.Dialogue = turn.Dialogue
		if !reflect.DeepEqual(r.Input, want) {
			t.Errorf("%s: input = %+v\nwant rerun's with its dialogue %+v", r.Case.ID, r.Input, want)
		}
	}
	yes := got[1].Input
	if len(yes.Dialogue) < 4 || yes.Dialogue[0].Text != "unlock the front door" || yes.Text != "yes" {
		t.Errorf("the yes was asked with %+v, want the door's question before it", yes.Dialogue)
	}
	if door := got[0].Input; len(door.Memories) != 2 || len(door.Summaries) != 1 {
		t.Errorf("the door was asked with memories %+v, summaries %+v", door.Memories, door.Summaries)
	}
}

// The baseline is the context the turn ran under, by the labels it
// recorded: the prompt's text and the tools as the wire offered them.
//
// verifies SPEC §13
func TestTheRecordedContextIsResolvedByItsLabels(t *testing.T) {
	s := parse(t, "name: x\ncases: [{id: list, ref: conv-2104-office/2, expect: {says: {max_words: 5}}}]")
	got, err := evalsuite.Resolve(context.Background(), day(t), daysContexts(), s)
	if err != nil {
		t.Fatal(err)
	}
	rc := got[0].Recorded
	if rc.Versions.Prompt != "sys@3" || rc.Versions.ToolSchema != "tools@7" || rc.Versions.Model != "qwen3-32b@1" {
		t.Errorf("versions = %+v, want the day's", rc.Versions)
	}
	if rc.Prompt != ollama.DefaultPrompt {
		t.Errorf("prompt is not sys@3's text")
	}
	if ollama.ToolSchema(rc.Tools) != ollama.ToolSchema(registry.Specs) {
		t.Errorf("tools are not tools@7's: %d tools", len(rc.Tools))
	}
	if _, ok := rc.Tools["media_search"]; !ok {
		t.Errorf("tools@7 offered media_search; the resolved tools do not")
	}
	if got[0].Skipped != "" {
		t.Errorf("skipped: %s", got[0].Skipped)
	}
}

// A turn that offered an MCP server's tools ran under a schema nothing kept
// (ADR-0075), so it cannot be asked as it was: skipped with the reason, as
// the export says context_missing, never failed.
//
// verifies SPEC §13
func TestACaseWhoseContextIsMissingIsSkipped(t *testing.T) {
	store := journal.NewMemStore()
	v := journal.Versions{Model: "qwen3:32b", Prompt: ollama.PromptVersion(ollama.DefaultPrompt), ToolSchema: "sha256:0000mcp00000"}
	j := journal.New(store, journal.FixedClock(time.Date(2026, 10, 8, 7, 42, 0, 0, time.UTC)), v)
	for _, r := range []journal.Record{
		{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "office", "speaker_id": "teagan", "resumed": "false"}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/cal", Fields: map[string]string{"text": "what's on my calendar tomorrow", "speaker_id": "teagan"}},
		{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "mcp_calendar_list_events", "call_id": "c1", "args_json": `{"day":"tomorrow"}`}},
		{Kind: journal.KindModelCompleted, Fields: map[string]string{"completion_json": "{}", "finish_reason": "stop"}},
	} {
		if _, err := j.Append(context.Background(), "conv-mcp-office", r); err != nil {
			t.Fatal(err)
		}
	}
	s := parse(t, "name: x\ncases: [{id: calendar, ref: conv-mcp-office/2, expect: {tool_called: [{tool: mcp_calendar_list_events}]}}]")
	got, err := evalsuite.Resolve(context.Background(), store, contexts.Default(), s)
	if err != nil {
		t.Fatalf("resolve: %v; a missing context is a skip, not a refusal", err)
	}
	if got[0].Skipped != "context_missing: tool_schema" {
		t.Errorf("skipped = %q, want context_missing: tool_schema", got[0].Skipped)
	}
}

// The evidence a turn is judged against is what the log showed it: the
// entities its results and its working calls named, and the nonces handed
// out before it.
//
// verifies SPEC §6, §13
func TestTheEvidenceIsWhatTheLogShowedTheTurn(t *testing.T) {
	s := parse(t, `
name: x
cases:
  - {id: zeppelin, ref: conv-0853-kitchen/14, expect: {no_invented_entities: true}}
  - {id: door-yes, ref: conv-1858-kitchen/12, expect: {asks_confirmation: false}}
  - {id: door, ref: conv-1858-kitchen/2, expect: {asks_confirmation: true}}
`)
	got, err := evalsuite.Resolve(context.Background(), day(t), daysContexts(), s)
	if err != nil {
		t.Fatal(err)
	}
	if ev := got[0].Evidence; !ev.Entities["media_player.kitchen"] || ev.Entities["media_player.living_room"] {
		t.Errorf("zeppelin's evidence = %v, want the kitchen's player and not the living room's, which came later", ev.Entities)
	}
	if ev := got[1].Evidence; !ev.Nonces[household.DoorNonce] || !ev.Entities["lock.front_door"] {
		t.Errorf("the yes's evidence = %+v, want the door's nonce and lock", ev)
	}
	if ev := got[2].Evidence; len(ev.Nonces) != 0 {
		t.Errorf("the door's evidence = %+v, want no nonce: none was handed out before it", ev)
	}
}
