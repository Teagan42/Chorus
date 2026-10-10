package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

const (
	// zeppelinAsk is Alice's "play something by zeppelin", the turn whose
	// list she cut off; doorYes is Teagan's "yes" to the held unlock.
	zeppelinAsk = 2
	doorYes     = 11

	// A reviewer's pairs, keyed conversation/turn/source.
	annotatedZeppel = convZeppel + "/2/annotation"
	replayedZeppel  = convZeppel + "/2/replay"
)

// What Alice should have heard, in the reviewer's words.
const shouldHaveZeppel = "I found three albums. Want Led Zeppelin one?"

func turnURL(conv string, seq int, rest string) string {
	return "/conversations/" + conv + "/turns/" + strconv.Itoa(seq) + "/" + rest
}

func mustPost(t *testing.T, s *server, target string, form url.Values) string {
	t.Helper()
	code, body := post(t, s, target, form)
	if code != http.StatusOK {
		t.Fatalf("POST %s = %d: %s", target, code, body)
	}
	return body
}

// Every kind chorusd journals says something on the Conversation page. The
// four that rendered blank (recall, summary, both halves of a confirmation)
// are in the household's day, so a fixture edit cannot hide them again.
//
// verifies SPEC §5, §6, §8
func TestEveryEventKindSaysSomethingOnTheConversationPage(t *testing.T) {
	for _, k := range journal.AllKinds {
		if _, ok := kindTones[k]; !ok {
			t.Errorf("%s has no tone", k)
		}
	}
	store := householdJournal(t)
	seen := map[journal.Kind]bool{}
	for id := range household.Logs() {
		events, err := store.Events(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		lc := logContext{loc: time.UTC}
		for _, e := range events {
			row := logRowOf(e, events[0], &lc)
			lc.see(e)
			seen[e.Kind] = true
			if strings.TrimSpace(row.Text+row.Unheard) == "" {
				t.Errorf("%s #%d (%s) renders a blank row", id, e.Seq, e.Kind)
			}
		}
	}
	for _, k := range []journal.Kind{journal.KindMemoryRecalled, journal.KindConversationSummarized, journal.KindConfirmationRequested, journal.KindConfirmationGiven} {
		if !seen[k] {
			t.Errorf("the household's day has no %s", k)
		}
	}
}

// The door's whole audit on one page: what the turn was told it remembers,
// the held call and its nonce, the yes it was redeemed after, and the
// summary written when the conversation closed.
//
// verifies SPEC §5, §6
func TestTheConversationPageShowsWhatTheModelWasToldAndTheYes(t *testing.T) {
	s, _ := newHouseholdServer(t)
	h := get(t, s, "/conversations/"+convDoor)
	for _, want := range []string{
		"told 2 memories and 1 earlier conversation",
		"Book club meets here on Thursdays at seven.",
		"Leave the porch light on when guests are coming. · alice&#39;s, shared",
		"Wed 8 Oct 22:30 · Teagan locked the front door for the night.",
		"held ha_call_service for the person&#39;s yes",
		"nonce " + household.DoorNonce + " · c1",
		"ha_call_service ran on a yes",
		"redeemed after #11, teagan: “yes”",
		"Teagan let book club in: the front door was unlocked after a yes.",
		"kept for teagan",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("the door's page is missing %q", want)
		}
	}
}

// A summary lands after the close, as long after as the model took. Browse
// measures the conversation to the close.
//
// verifies SPEC §8
func TestASummaryDoesNotStretchTheConversation(t *testing.T) {
	s, _ := newHouseholdServer(t)
	h := get(t, s, "/conversations?day=2025-10-09")
	i := strings.Index(h, "unlock the front door")
	if i < 0 {
		t.Fatal("Browse does not list the door")
	}
	row := h[i:min(len(h), i+800)]
	if !strings.Contains(row, ">14s<") {
		t.Errorf("the door lasts 14 s to its close; Browse says otherwise:\n%s", row)
	}
}

// Every turn carries SPEC §9.2's eight labels and the free-text note, under
// the utterance that opens it.
//
// verifies SPEC §9.2
func TestEveryTurnOffersTheEightLabelsAndANote(t *testing.T) {
	s, _ := newHouseholdServer(t)
	h := get(t, s, "/conversations/"+convDoor)
	for _, seq := range []string{"2", "11"} {
		if !strings.Contains(h, `id="turn-`+seq+`-labels"`) {
			t.Errorf("turn #%s has no labels", seq)
		}
	}
	for _, l := range curation.Labels {
		if n := strings.Count(h, "/labels/"+string(l)+`"`); n != 2 {
			t.Errorf("label %s is offered on %d turns, want both", l, n)
		}
	}
	for _, want := range []string{"Transcript wrong", "Spoke when it shouldn&#39;t", "Good — exemplar", "What it should have done"} {
		if !strings.Contains(h, want) {
			t.Errorf("the vocabulary is missing %q", want)
		}
	}
}

// A label and what it should have done turn the turn into a pair in Curate:
// the recorded take rejected, the reviewer's words chosen. Accepted, it
// exports with its labels.
//
// verifies SPEC §9.2
func TestALabelledTurnBecomesAPairThatExportsWithItsLabels(t *testing.T) {
	s, _ := newHouseholdServer(t)
	body := mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/misunderstood_intent"), nil)
	if !strings.Contains(body, `aria-pressed="true"`) || strings.Contains(body, "In Curate") {
		t.Errorf("a label alone should be on, with no pair yet:\n%s", body)
	}
	mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/spoke_when_it_shouldnt"), nil)
	body = mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "note"), url.Values{"should_have": {"  " + shouldHaveZeppel + " "}})
	if !strings.Contains(body, shouldHaveZeppel+"</textarea>") || !strings.Contains(body, "In Curate ›") {
		t.Errorf("the note should be kept and the pair linked:\n%s", body)
	}

	h := get(t, s, pairHref(annotatedZeppel, "all"))
	for _, want := range []string{
		"DPO pair · annotation · " + convZeppel,
		"I found three",
		"labelled “misunderstood intent”, “spoke when it shouldn&#39;t”",
		shouldHaveZeppel,
		"authored by the reviewer",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("Curate is missing %q", want)
		}
	}
	mustPost(t, s, "/pairs/"+annotatedZeppel+"/accept", nil)
	line := exportRow(t, s, annotatedZeppel)
	if !strings.Contains(line, `"source":"annotation"`) || !strings.Contains(line, `"labels":["misunderstood_intent","spoke_when_it_shouldnt"]`) ||
		!strings.Contains(line, `"chosen":[{"role":"assistant","content":"`+shouldHaveZeppel+`","tool_calls":[{"type":"function","function":{"name":"media_search"`) {
		t.Errorf("the annotation's row is not what the reviewer said:\n%s", line)
	}
}

// A label pressed while the note is half-written saves the draft with it,
// rather than swapping it away.
//
// verifies SPEC §9.2
func TestALabelKeepsTheNoteBeingWritten(t *testing.T) {
	s, decisions := newHouseholdServer(t)
	body := mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/misunderstood_intent"), url.Values{"should_have": {shouldHaveZeppel}})
	if !strings.Contains(body, shouldHaveZeppel) || !strings.Contains(body, "In Curate") {
		t.Errorf("the swapped annotation lost the draft:\n%s", body)
	}
	if !strings.Contains(body, `hx-include="#turn-2-should-have"`) {
		t.Error("a label does not send the note with it")
	}
	annos, err := decisions.Annotations(context.Background(), convZeppel)
	if err != nil {
		t.Fatal(err)
	}
	if a := annos[zeppelinAsk]; a.ShouldHave != shouldHaveZeppel || !a.Faulted() {
		t.Errorf("stored annotation = %+v", a)
	}
}

// A good turn is a positive, not a pair, and a fault with nothing said about
// what instead has no chosen side to train.
//
// verifies SPEC §9.2
func TestAnExemplarOrABareFaultMakesNoPair(t *testing.T) {
	s, _ := newHouseholdServer(t)
	mustPost(t, s, turnURL(convDoor, doorYes, "labels/exemplar"), nil)
	mustPost(t, s, turnURL(convDoor, doorYes, "note"), url.Values{"should_have": {"Exactly this."}})
	mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/too_slow"), nil)
	for _, p := range mustPairs(t, s) {
		if p.H.Source != "barge-in" {
			t.Errorf("Curate holds %s, which nobody said what to do instead of", p.ID)
		}
	}
	h := get(t, s, "/conversations/"+convDoor)
	if !strings.Contains(h, "Exactly this.</textarea>") {
		t.Error("the exemplar's note is not kept")
	}
}

// Taking the fault off drops the pair; writing a different note asks for a
// new verdict, since the accepted chosen side is not the one now written.
//
// verifies SPEC §9.2
func TestChangingAnAnnotationAsksForANewVerdict(t *testing.T) {
	s, decisions := newHouseholdServer(t)
	mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/misunderstood_intent"), url.Values{})
	mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "note"), url.Values{"should_have": {shouldHaveZeppel}})
	mustPost(t, s, "/pairs/"+annotatedZeppel+"/accept", nil)

	mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/too_slow"), nil)
	if _, ok, _ := decisions.Get(context.Background(), annotatedZeppel); !ok {
		t.Error("adding a second label to a pair already accepted dropped its verdict")
	}
	mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "note"), url.Values{"should_have": {"Three albums. Led Zeppelin one?"}})
	if _, ok, _ := decisions.Get(context.Background(), annotatedZeppel); ok {
		t.Error("a new chosen side kept the verdict on the old one")
	}
	if p, ok := find(mustPairs(t, s), annotatedZeppel); !ok || p.Status != "unreviewed" || p.Chosen != "Three albums. Led Zeppelin one?" {
		t.Errorf("pair = %+v, want unreviewed with the new note", p.Pair)
	}
	mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/misunderstood_intent"), nil)
	mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/too_slow"), nil)
	if _, ok := find(mustPairs(t, s), annotatedZeppel); ok {
		t.Error("a turn with no fault left is still a pair")
	}
}

// Only a turn can be labelled, and only with the vocabulary.
//
// verifies SPEC §9.2
func TestAnnotatingSomethingThatIsNotATurnIsRefused(t *testing.T) {
	s, _ := newHouseholdServer(t)
	for _, target := range []string{
		turnURL(convDoor, 4, "labels/wrong_tool"),           // the held call, not a turn
		turnURL(convDoor, 99, "note"),                       // past the end of the log
		turnURL("conv-0000-attic", 2, "labels/wrong_tool"),  // no such conversation
		turnURL(convDoor, doorYes, "labels/rude"),           // not in SPEC §9.2
		"/conversations/" + convDoor + "/turns/eleven/note", // not a seq
	} {
		if code, _ := post(t, s, target, url.Values{"should_have": {"Unlock it."}}); code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", target, code)
		}
	}
}

// householdReplayServer is the day with the scripted household model behind
// Replay, as the demo runs it.
func householdReplayServer(t *testing.T) (*server, curation.Store) {
	t.Helper()
	s, decisions := newHouseholdServer(t)
	s.engineFor = household.NewModel(s.journal).Engines
	return s, decisions
}

// briefPrompt is the default prompt with the line that makes the household's
// model lead with the count.
const briefPrompt = "\nWhen there are several results, say how many, offer the first, and stop."

// A re-run that changed a turn can be promoted: the recorded take rejected,
// the re-run's chosen, accepted, and exported with where it came from.
//
// verifies SPEC §9.2
func TestPromotingAReRunMakesAnAcceptedReplayPair(t *testing.T) {
	s, decisions := householdReplayServer(t)
	run := mustPost(t, s, "/replays/"+convZeppel, url.Values{"model": {"qwen3-32b@1"}, "prompt": {ollama.DefaultPrompt + briefPrompt}})
	if !strings.Contains(run, `hx-post="/replays/`+convZeppel+`/turns/2/promote"`) {
		t.Fatalf("the changed turn offers no promotion:\n%s", run)
	}
	if strings.Contains(run, "/turns/10/promote") {
		t.Error("a turn the re-run left alone offers a promotion")
	}

	cell := mustPost(t, s, "/replays/"+convZeppel+"/turns/2/promote", url.Values{
		"model": {"qwen3-32b@1"}, "prompt": {ollama.DefaultPrompt + briefPrompt},
		"speech": {shouldHaveZeppel},
		"calls":  {`[{"tool":"media_search","args":"{\"query\":\"Led Zeppelin\",\"media_type\":\"album\",\"limit\":5}"}]`},
	})
	if !strings.Contains(cell, "promoted · qwen3-32b@1 · sys@edited") || !strings.Contains(cell, `id="promote-2"`) {
		t.Errorf("the cell does not say it was promoted:\n%s", cell)
	}
	promos, err := decisions.Promotions(context.Background(), convZeppel)
	if err != nil {
		t.Fatal(err)
	}
	if p := promos[zeppelinAsk]; p.SystemPrompt != ollama.DefaultPrompt+briefPrompt || len(p.Calls) != 1 || p.Versions.Prompt != "sys@edited" {
		t.Errorf("stored promotion = %+v", p)
	}

	p, ok := find(mustPairs(t, s), replayedZeppel)
	if !ok || p.Status != "accepted" || p.Chosen != shouldHaveZeppel || p.Rejected != "I found three" {
		t.Errorf("replay pair = %+v (found %v)", p.Pair, ok)
	}
	line := exportRow(t, s, replayedZeppel)
	for _, want := range []string{`"source":"replay"`, `"chosen_versions":{"model":"qwen3-32b@1","prompt":"sys@edited"`, `"chosen_calls":[{`} {
		if !strings.Contains(line, want) {
			t.Errorf("the replay row is missing %s:\n%s", want, line)
		}
	}
	if h := get(t, s, "/replays/"+convZeppel); !strings.Contains(h, "promoted · qwen3-32b@1 · sys@edited") {
		t.Error("Replay forgets the promotion on the next visit")
	}
}

// A take re-run without media_search is promoted with the version the server
// computes from the cut schema, and the declarations it was offered.
//
// verifies SPEC §9.2
func TestAPromotionCarriesTheToolSchemaItRanUnder(t *testing.T) {
	s, decisions := householdReplayServer(t)
	cell := mustPost(t, s, "/replays/"+convZeppel+"/turns/2/promote", url.Values{
		"model": {"qwen3-32b@1"}, "prompt": {ollama.DefaultPrompt}, "tools": {withoutTool(t, "media_search")},
		"speech": {shouldHaveZeppel},
	})
	if !strings.Contains(cell, "promoted · qwen3-32b@1 · sys@3 · tools@edited") {
		t.Errorf("the cell does not say what it ran under:\n%s", cell)
	}
	promos, err := decisions.Promotions(context.Background(), convZeppel)
	if err != nil {
		t.Fatal(err)
	}
	if p := promos[zeppelinAsk]; p.Versions.ToolSchema != "tools@edited" || strings.Contains(p.ToolSchema, "media_search") || !strings.Contains(p.ToolSchema, "ha_get_state") {
		t.Errorf("stored promotion ran under %+v, offered %d bytes of tools", p.Versions, len(p.ToolSchema))
	}
	line := exportRow(t, s, replayedZeppel)
	if !strings.Contains(line, `"tool_schema":"tools@edited"`) {
		t.Errorf("the replay row does not name the cut schema:\n%s", line)
	}
}

// convGarageCheck is Teagan asking about the garage from the office: the
// first ask checks the sensor, the follow-up answers with what it said.
const convGarageCheck = "conv-1930-office"

func withGarageCheck(t *testing.T, store *journal.MemStore) uint64 {
	t.Helper()
	clk := &stepClock{}
	j := journal.New(store, clk, household.Versions())
	start := household.Day().Add(19*time.Hour + 30*time.Minute)
	var ask uint64
	for _, r := range []struct {
		sec    float64
		kind   journal.Kind
		audio  string
		fields []string
	}{
		{0, journal.KindSessionOpened, "", []string{"satellite", "office", "speaker_id", "teagan", "resumed", "false"}},
		{0.3, journal.KindUtteranceTranscribed, "blob://mic/garage-check", []string{"text", "did I leave the garage open", "speaker_id", "teagan"}},
		{0.9, journal.KindToolCalled, "", []string{"tool", "speak", "call_id", "s1", "args_json", `{"mode":"queue","streamed":true}`}},
		{1.0, journal.KindToolCalled, "", []string{"tool", "ha_get_state", "call_id", "c1", "args_json", `{"entity_id":"cover.garage_door"}`}},
		{1.1, journal.KindModelCompleted, "", []string{"completion_json", "{}", "finish_reason", "tool_calls"}},
		{1.6, journal.KindToolResult, "", []string{"call_id", "c1", "outcome", "ok", "result_json", `{"state":"closed"}`}},
		{2.0, journal.KindSpeechSpoken, "blob://tts/garage-checking", []string{"text", "Checking the garage.", "frames_played", "16000", "call_id", "s1"}},
		{2.1, journal.KindToolResult, "", []string{"call_id", "s1", "outcome", "ok"}},
		{2.4, journal.KindToolCalled, "", []string{"tool", "speak", "call_id", "s2", "args_json", `{"mode":"queue","streamed":true}`}},
		{4.2, journal.KindSpeechSpoken, "blob://tts/garage-closed", []string{"text", "No, the garage door is closed.", "frames_played", "28800", "call_id", "s2"}},
		{4.3, journal.KindToolResult, "", []string{"call_id", "s2", "outcome", "ok"}},
		{4.4, journal.KindModelCompleted, "", []string{"completion_json", "{}", "finish_reason", "stop"}},
		{9, journal.KindSessionClosed, "", []string{"reason", "model_ended", "satellite", "office"}},
	} {
		clk.now = start.Add(time.Duration(r.sec * float64(time.Second)))
		rec := journal.Record{Kind: r.kind, AudioRef: r.audio, Fields: map[string]string{}}
		for i := 0; i+1 < len(r.fields); i += 2 {
			rec.Fields[r.fields[i]] = r.fields[i+1]
		}
		e, err := j.Append(context.Background(), convGarageCheck, rec)
		if err != nil {
			t.Fatalf("append %s: %v", r.kind, err)
		}
		if r.kind == journal.KindUtteranceTranscribed {
			ask = e.Seq
		}
	}
	return ask
}

// A promoted re-run is weighed against the take Replay showed beside it: the
// turn's first ask, not what the follow-up said once the sensor answered.
//
// verifies SPEC §9.2
func TestAReplayPairRejectsTheFirstAskReplayCompared(t *testing.T) {
	store := householdJournal(t)
	ask := withGarageCheck(t, store)
	decisions := curation.NewMemStore()
	s := newServer(store, decisions, householdBlobs(t), household.ReviewedAt)
	edited := journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@edited", ToolSchema: "tools@7"}
	if err := decisions.PutPromotion(context.Background(), curation.Promotion{
		ConversationID: convGarageCheck, Seq: ask, Speech: "One second, checking the garage door.",
		Calls:    []curation.Call{{Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`}},
		Versions: edited, SystemPrompt: ollama.DefaultPrompt + briefPrompt, PromotedAt: household.ReviewedAt(),
	}); err != nil {
		t.Fatal(err)
	}
	p, ok := find(mustPairs(t, s), fmt.Sprintf("%s/%d/replay", convGarageCheck, ask))
	if !ok {
		t.Fatal("the promotion made no pair")
	}
	if p.H.Rejected != "Checking the garage." || p.H.RejectedUnheard != "" {
		t.Errorf("rejected = %q + %q, want only the first ask's speech", p.H.Rejected, p.H.RejectedUnheard)
	}
	if len(p.H.Calls) != 1 || p.H.Calls[0].Tool != "ha_get_state" || !p.H.Attributed || p.H.Versions != household.Versions() {
		t.Errorf("rejected side = calls %v, versions %+v, attributed %v", p.H.Calls, p.H.Versions, p.H.Attributed)
	}
}

// A promotion needs a model to say what it ran under, a turn to belong to,
// and something said.
//
// verifies SPEC §9.2
func TestAPromotionThatCannotBeAttributedIsRefused(t *testing.T) {
	form := url.Values{"model": {"qwen3-32b@1"}, "prompt": {ollama.DefaultPrompt + briefPrompt}, "speech": {shouldHaveZeppel}}
	bare, _ := newHouseholdServer(t)
	if code, _ := post(t, bare, "/replays/"+convZeppel+"/turns/2/promote", form); code != http.StatusServiceUnavailable {
		t.Errorf("with no model, promote = %d, want 503", code)
	}
	s, _ := householdReplayServer(t)
	if code, _ := post(t, s, "/replays/"+convZeppel+"/turns/4/promote", form); code != http.StatusNotFound {
		t.Errorf("promoting the barge-in, not a turn = %d, want 404", code)
	}
	silent := url.Values{"model": form["model"], "prompt": form["prompt"], "speech": {"  "}}
	if code, _ := post(t, s, "/replays/"+convZeppel+"/turns/2/promote", silent); code != http.StatusBadRequest {
		t.Errorf("promoting a take that says nothing = %d, want 400", code)
	}
	if code, _ := post(t, s, "/replays/"+convZeppel+"/turns/2/promote", url.Values{
		"model": form["model"], "prompt": form["prompt"], "speech": form["speech"], "calls": {"media_search"},
	}); code != http.StatusBadRequest {
		t.Errorf("promoting unreadable calls = %d, want 400", code)
	}
	for _, p := range mustPairs(t, s) {
		if p.H.Source == "replay" {
			t.Errorf("a refused promotion left %s", p.ID)
		}
	}
}

// Replay tells each turn what it was told it remembers, as a re-run is.
//
// verifies SPEC §5, §9.2
func TestReplayShowsWhatEachTurnWasToldItRemembers(t *testing.T) {
	s, _ := householdReplayServer(t)
	h := get(t, s, "/replays/"+convDoor)
	if strings.Count(h, "Book club meets here on Thursdays at seven.") != 2 {
		t.Error("both of the door's turns were told about book club; Replay should say so on each")
	}
}

// Review shows what the cut turn was told it remembers beside the clips: a
// cut is judged against what the model knew.
//
// verifies SPEC §5, §9.2
func TestReviewShowsWhatTheCutTurnWasTold(t *testing.T) {
	store := journal.NewMemStore()
	j := journal.New(store, journal.FixedClock(time.Unix(1_760_000_000, 0)),
		journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"})
	recs := zeppelinRecords()
	recall := journal.Record{Kind: journal.KindMemoryRecalled, Fields: map[string]string{
		"person": "alice", "memories_json": journal.EncodeMemories([]journal.Memory{
			{ID: "m_51ab02c4", Person: "alice", Fact: "Prefers the first album of anything."},
		}),
	}}
	recs = append(recs[:2], append([]journal.Record{recall}, recs[2:]...)...)
	for _, r := range recs {
		if _, err := j.Append(context.Background(), "conv-1", r); err != nil {
			t.Fatal(err)
		}
	}
	s := newServer(store, curation.NewMemStore(), fixtureBlobs(t), time.Now)
	h := get(t, s, "/review")
	if !strings.Contains(h, "Told it remembers") || !strings.Contains(h, "Prefers the first album of anything.") {
		t.Error("Review does not show what Alice's cut turn was told")
	}
}

// Review walks the cuts only: a reviewer's own pairs have no barge-in.
//
// verifies SPEC §9.2
func TestReviewWalksOnlyTheCuts(t *testing.T) {
	s, _ := newHouseholdServer(t)
	mustPost(t, s, turnURL(convDoor, doorYes, "labels/too_slow"), nil)
	mustPost(t, s, turnURL(convDoor, doorYes, "note"), url.Values{"should_have": {"Front door unlocked."}})
	h := get(t, s, "/review")
	if !strings.Contains(h, "pair 1 of 3") {
		t.Error("Review should count the day's three cuts, not the labelled door")
	}
}

func mustPairs(t *testing.T, s *server) []pair {
	t.Helper()
	ps, err := s.pairs(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// exportRow is the downloaded row for pair id.
func exportRow(t *testing.T, s *server, id string) string {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(get(t, s, "/export/dpo.jsonl")), "\n") {
		if strings.Contains(line, `"id":"`+id+`"`) {
			return line
		}
	}
	t.Fatalf("the export has no row for %s", id)
	return ""
}
