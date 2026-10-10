package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

const (
	// replayedGarage is Teagan's "is the garage door closed", re-run.
	replayedGarage = convGarage + "/2/replay"

	// contactCheck is the call the brief re-run makes instead: the contact
	// sensor, which answers, and nothing said until it has.
	contactCheck = `[{"tool":"ha_get_state","args":"{\"entity_id\":\"binary_sensor.garage_door_contact\"}"}]`
)

// A re-run that only calls a different sensor can be promoted, and the pair
// it makes says nothing on its chosen side and calls the contact sensor.
//
// verifies SPEC §9.2
func TestAReRunThatOnlyCallsCanBePromoted(t *testing.T) {
	s, decisions := householdReplayServer(t)
	run := mustPost(t, s, "/replays/"+convGarage, url.Values{"model": {"qwen3-32b@1"}, "prompt": {ollama.DefaultPrompt + briefPrompt}})
	if !strings.Contains(run, `hx-post="/replays/`+convGarage+`/runs/1/turns/2/promote"`) {
		t.Fatalf("the garage turn's call-only re-run offers no promotion:\n%s", run)
	}
	cell := mustPost(t, s, "/replays/"+convGarage+"/runs/1/turns/2/promote", nil)
	if !strings.Contains(cell, "promoted · qwen3-32b@1 · sys@edited") {
		t.Errorf("the cell does not say it was promoted:\n%s", cell)
	}
	promos, err := decisions.Promotions(context.Background(), convGarage)
	if err != nil {
		t.Fatal(err)
	}
	if calls, _ := json.Marshal(promos[2].Calls); string(calls) != contactCheck {
		t.Errorf("promoted calls = %s, want the kept run's %s", calls, contactCheck)
	}

	p, ok := find(mustPairs(t, s), replayedGarage)
	if !ok || p.Status != "accepted" || p.Chosen != "" || p.Rejected != "I couldn't reach the garage door sensor." {
		t.Fatalf("replay pair = %+v (found %v)", p.Pair, ok)
	}
	h := get(t, s, pairHref(replayedGarage, "all"))
	for _, want := range []string{
		`<span class="pair-actions__call">ha_get_state {&#34;entity_id&#34;:&#34;cover.garage_door&#34;}</span>`,
		`<span class="pair-actions__call">ha_get_state {&#34;entity_id&#34;:&#34;binary_sensor.garage_door_contact&#34;}</span>`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("Curate does not show %s", want)
		}
	}

	if !strings.Contains(h, `+ ha_get_state {&#34;entity_id&#34;:&#34;binary_sensor.garage_door_contact&#34;}`) {
		t.Error("Curate's list names the calls-only chosen side by nothing")
	}

	line := exportRow(t, s, replayedGarage)
	for _, want := range []string{
		`"chosen":[{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"ha_get_state","arguments":{"entity_id":"binary_sensor.garage_door_contact"}}}]}]`,
		`"rejected":[{"role":"assistant","content":"I couldn't reach the garage door sensor.","tool_calls":[{"type":"function","function":{"name":"ha_get_state","arguments":{"entity_id":"cover.garage_door"}}}]}]`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the garage row is missing %s:\n%s", want, line)
		}
	}
}

// A kept take that neither says nor calls anything is still refused.
//
// verifies SPEC §9.2
func TestAPromotionThatDoesNothingIsRefused(t *testing.T) {
	s, decisions := householdReplayServer(t)
	if _, err := decisions.AddRerun(context.Background(), curation.Rerun{
		ConversationID: convGarage, Versions: journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@edited", ToolSchema: "tools@8"},
		SystemPrompt: ollama.DefaultPrompt + briefPrompt, ToolSchema: ollama.ToolSchema(registry.Offered()),
		Takes: []curation.Take{{Seq: 2, Finish: "stop"}}, RanAt: household.ReviewedAt(),
	}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, s, "/replays/"+convGarage+"/runs/1/turns/2/promote", nil)
	if code != 400 || !strings.Contains(body, "said or called") {
		t.Errorf("promote = %d %q, want 400 asking for speech or a call", code, body)
	}
}

// Curate shows what each side calls, as the export writes it: a note keeps
// the turn's search on both sides, and "wrong tool" leaves both bare.
//
// verifies SPEC §9.2
func TestCurateShowsWhatEachSideCalls(t *testing.T) {
	s, _ := newHouseholdServer(t)
	mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/misunderstood_intent"), url.Values{"should_have": {shouldHaveZeppel}})
	h := get(t, s, pairHref(annotatedZeppel, "all"))
	search := `<span class="pair-actions__call">media_search {&#34;query&#34;:&#34;Led Zeppelin&#34;,&#34;media_type&#34;:&#34;album&#34;,&#34;limit&#34;:5}</span>`
	if n := strings.Count(h, search); n != 2 {
		t.Errorf("the search shows %d times, want once on each side", n)
	}

	mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/wrong_tool"), nil)
	if h := get(t, s, pairHref(annotatedZeppel, "all")); strings.Contains(h, "pair-actions__call") {
		t.Error("a wrong-tool pair shows calls nobody said were right")
	}
}

// "Wrong tool" takes the calls off both sides, so a pair accepted with them
// asks again; taking it off puts them back and asks again too.
//
// verifies SPEC §9.2
func TestWrongToolAsksForANewVerdict(t *testing.T) {
	s, decisions := newHouseholdServer(t)
	mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/misunderstood_intent"), url.Values{"should_have": {shouldHaveZeppel}})
	for _, step := range []string{"adding", "removing"} {
		mustPost(t, s, "/pairs/"+annotatedZeppel+"/accept", nil)
		mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/wrong_tool"), nil)
		if _, ok, _ := decisions.Get(context.Background(), annotatedZeppel); ok {
			t.Errorf("%s wrong tool kept the verdict on the pair's old calls", step)
		}
	}
}

// harvest reads "wrong tool" by its stored name; the two must not drift.
//
// verifies SPEC §9.2
func TestHarvestReadsTheWrongToolLabelCurationStores(t *testing.T) {
	p := harvest.Pair{Source: harvest.SourceAnnotation, Labels: []string{string(curation.LabelWrongTool)}}
	if _, _, withCalls := p.TextCalls(); withCalls {
		t.Errorf("%q does not make a pair speech-only", curation.LabelWrongTool)
	}
}
