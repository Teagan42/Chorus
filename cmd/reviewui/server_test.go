package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/blob"
	"github.com/teaganglenn/chorus/internal/curation"
	"github.com/teaganglenn/chorus/internal/journal"
)

// bargeInLog writes one barge-in conversation through a real journal, the
// same shape a session records (SPEC §4.4): a cut turn, the correction, and
// the turn that answered it.
func bargeInLog(t *testing.T) *journal.MemStore {
	t.Helper()
	store := journal.NewMemStore()
	v := journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"}
	j := journal.New(store, journal.FixedClock(time.Unix(1_760_000_000, 0)), v)

	rec := func(kind journal.Kind, audio string, fields ...string) journal.Record {
		r := journal.Record{Kind: kind, AudioRef: audio, Fields: map[string]string{}}
		for i := 0; i+1 < len(fields); i += 2 {
			r.Fields[fields[i]] = fields[i+1]
		}
		return r
	}
	for _, r := range []journal.Record{
		rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "alice", "resumed", "false"),
		rec(journal.KindUtteranceTranscribed, "blob://mic/1", "text", "play something by zeppelin", "speaker_id", "alice"),
		rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", `{"mode":"queue","streamed":true}`),
		rec(journal.KindBargeInDetected, "blob://mic/2", "tts_position_ms", "420"),
		rec(journal.KindSpeechTruncated, "blob://tts/s1", "spoken_text", "I found three", "unspoken_text", " albums by that artist", "frames_played", "2080"),
		rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "cancelled"),
		rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop"),
		rec(journal.KindUtteranceTranscribed, "blob://mic/3", "text", "just the first one", "speaker_id", "alice"),
		rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", `{"mode":"queue","streamed":true}`),
		rec(journal.KindSpeechSpoken, "blob://tts/s2", "text", "Playing Led Zeppelin one.", "frames_played", "16000"),
		rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "ok"),
		rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop"),
		rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "kitchen"),
	} {
		if _, err := j.Append(context.Background(), "conv-1", r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
	return store
}

// fixtureBlobs stores PCM under every ref the fixture journal cites, sized
// so each clip has a distinct, known duration.
func fixtureBlobs(t *testing.T) *blob.Memory {
	t.Helper()
	m := blob.NewMemory()
	for key, seconds := range map[string]float64{
		"mic/1": 1, "mic/2": 0.5, "mic/3": 1, "tts/s1": 2, "tts/s2": 1,
	} {
		w, err := m.Create(context.Background(), key)
		if err != nil {
			t.Fatalf("create %s: %v", key, err)
		}
		if _, err := w.Write(make([]byte, int(seconds*32000))); err != nil {
			t.Fatalf("write %s: %v", key, err)
		}
		if _, err := w.Commit(); err != nil {
			t.Fatalf("commit %s: %v", key, err)
		}
	}
	return m
}

func newTestServer(t *testing.T) (*server, curation.Store) {
	t.Helper()
	decisions := curation.NewMemStore()
	s := newServer(bargeInLog(t), decisions, fixtureBlobs(t), func() time.Time { return time.Unix(1_760_000_100, 0).UTC() })
	return s, decisions
}

func get(t *testing.T, s *server, target string) string {
	t.Helper()
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func post(t *testing.T, s *server, target string, form url.Values) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

// pairID is what the harvester derives for the fixture's one barge-in: the
// conversation and the sequence of the cut.
const pairID = "conv-1/5"

// verifies SPEC §9.2
func TestCuratePageShowsTheHarvestedPair(t *testing.T) {
	s, _ := newTestServer(t)
	h := get(t, s, "/curate/pairs")
	for _, want := range []string{
		"I found three",               // the rejected side, heard half
		" albums by that artist",      // the unheard remainder
		"just the first one",          // the correction
		"Playing Led Zeppelin one.",   // AsSaid, offered as the starting chosen
		"unreviewed · fix chosen",     // the mismatch is visible before any click
		"/pairs/" + pairID + "/edit",  // the flow posts to this pair
		"answers just the first one,", // provenance names the wrong prompt
	} {
		if !strings.Contains(h, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

// verifies SPEC §9.1
func TestAcceptingTheMismatchedPairTripsTheGuardAndStoresNothing(t *testing.T) {
	s, decisions := newTestServer(t)
	code, h := post(t, s, "/pairs/"+pairID+"/accept", url.Values{})
	if code != http.StatusOK {
		t.Fatalf("accept = %d: %s", code, h)
	}
	if !strings.Contains(h, "This chosen answer replies to the wrong prompt.") {
		t.Error("the guard did not interrupt the accept")
	}
	if _, ok, _ := decisions.Get(context.Background(), pairID); ok {
		t.Error("a guarded accept stored a verdict")
	}
}

// verifies SPEC §9.2
func TestSavingAFixedChosenPersistsAndSurvivesAReload(t *testing.T) {
	s, decisions := newTestServer(t)
	fixed := "Playing the first Led Zeppelin album."
	if code, h := post(t, s, "/pairs/"+pairID+"/save", url.Values{"chosen": {fixed}}); code != http.StatusOK {
		t.Fatalf("save = %d: %s", code, h)
	}
	d, ok, err := decisions.Get(context.Background(), pairID)
	if err != nil || !ok {
		t.Fatalf("decision: ok=%v err=%v", ok, err)
	}
	if d.Status != curation.StatusEdited || d.Chosen != fixed {
		t.Errorf("decision = %+v, want edited with the fixed text", d)
	}

	// Accept now passes the guard and upgrades the verdict.
	if code, h := post(t, s, "/pairs/"+pairID+"/accept", url.Values{}); code != http.StatusOK {
		t.Fatalf("accept = %d: %s", code, h)
	}
	d, _, _ = decisions.Get(context.Background(), pairID)
	if d.Status != curation.StatusAccepted {
		t.Errorf("status = %s, want accepted", d.Status)
	}

	// The page reflects the stored verdict after a fresh load.
	if h := get(t, s, "/curate/pairs"); !strings.Contains(h, fixed) {
		t.Error("reload lost the fixed chosen side")
	}
}

// verifies SPEC §9.2
func TestDiscardingWithAReasonPersistsIt(t *testing.T) {
	s, decisions := newTestServer(t)
	if code, h := post(t, s, "/pairs/"+pairID+"/reason", url.Values{"reason": {"Barge-in was noise"}}); code != http.StatusOK {
		t.Fatalf("reason = %d: %s", code, h)
	}
	d, ok, _ := decisions.Get(context.Background(), pairID)
	if !ok || d.Status != curation.StatusDiscarded || d.Reason != "Barge-in was noise" {
		t.Errorf("decision = %+v ok=%v, want a discard with its reason", d, ok)
	}
}

// verifies SPEC §9.2
func TestUndoReturnsThePairToUnreviewed(t *testing.T) {
	s, decisions := newTestServer(t)
	if code, _ := post(t, s, "/pairs/"+pairID+"/accept-anyway", url.Values{}); code != http.StatusOK {
		t.Fatal("accept-anyway failed")
	}
	if d, ok, _ := decisions.Get(context.Background(), pairID); !ok || !d.Unfixed {
		t.Fatalf("decision = %+v ok=%v, want an unfixed accept", d, ok)
	}
	if code, _ := post(t, s, "/pairs/"+pairID+"/undo", url.Values{}); code != http.StatusOK {
		t.Fatal("undo failed")
	}
	if _, ok, _ := decisions.Get(context.Background(), pairID); ok {
		t.Error("undo left a verdict behind; unreviewed is the absence of one")
	}
}

// verifies SPEC §9.2
func TestAnUnknownPairOrActionIsRefused(t *testing.T) {
	s, _ := newTestServer(t)
	if code, _ := post(t, s, "/pairs/conv-9/5/accept", url.Values{}); code != http.StatusNotFound {
		t.Errorf("unknown pair = %d, want 404", code)
	}
	if code, _ := post(t, s, "/pairs/"+pairID+"/explode", url.Values{}); code != http.StatusBadRequest {
		t.Errorf("unknown action = %d, want 400", code)
	}
}

// verifies SPEC §9.2
func TestSwitchingStatusTabsDropsASelectionOutsideTheFilter(t *testing.T) {
	s, _ := newTestServer(t)
	// The one pair is unreviewed, so the Accepted tab must not keep showing
	// it in the detail pane while the list says the pile is empty.
	h := get(t, s, "/curate/pairs?status=accepted&pair="+url.QueryEscape(pairID))
	if !strings.Contains(h, "Nothing in this pile.") {
		t.Fatal("the accepted tab should list nothing")
	}
	if strings.Contains(h, "/pairs/"+pairID+"/edit") {
		t.Error("a pair outside the active filter is still in the detail pane")
	}

	// Once the pair is accepted, that tab selects it again.
	if code, _ := post(t, s, "/pairs/"+pairID+"/accept-anyway", url.Values{}); code != http.StatusOK {
		t.Fatal("accept-anyway failed")
	}
	h = get(t, s, "/curate/pairs?status=accepted")
	if !strings.Contains(h, "/pairs/"+pairID+"/edit") {
		t.Error("the accepted tab did not select its one pair")
	}
}

// verifies SPEC §9.2
func TestReviewPageDrawsTheCutOnTheTimeline(t *testing.T) {
	s, _ := newTestServer(t)
	h := get(t, s, "/review")
	for _, want := range []string{
		"cut · 00:00.130",            // the DAC-confirmed 2080 frames, not the detection snapshot
		"I found three",              // the heard half of the rejected turn
		" albums by that artist",     // the unheard tail, drawn hatched
		"just the first one",         // the correction on the mic track
		"play something by zeppelin", // the prompt names the page
	} {
		if !strings.Contains(h, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

// verifies SPEC §9.2
func TestReviewPageOffersEveryClipAsPlayableAudio(t *testing.T) {
	s, _ := newTestServer(t)
	h := get(t, s, "/review?pair="+url.QueryEscape(pairID))
	if !strings.Contains(h, "<audio") {
		t.Fatal("no audio element on the page")
	}
	for _, ref := range []string{"blob://tts/s1", "blob://mic/2", "blob://mic/3", "blob://tts/s2"} {
		if !strings.Contains(h, "/audio?ref="+url.QueryEscape(ref)) {
			t.Errorf("no player for %s", ref)
		}
	}
}

// verifies SPEC §9.2
func TestReviewAudioRouteServesAJournalBlob(t *testing.T) {
	s, _ := newTestServer(t)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/audio?ref="+url.QueryEscape("blob://mic/2"), nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/wav" {
		t.Errorf("GET /audio = %d %s, want a WAV", w.Code, w.Header().Get("Content-Type"))
	}
}

// verifies SPEC §9.2
func TestReviewPageWithoutBargeInsSaysSo(t *testing.T) {
	s := newServer(journal.NewMemStore(), curation.NewMemStore(), blob.NewMemory(), time.Now)
	h := get(t, s, "/review")
	if !strings.Contains(h, "Nothing to review.") {
		t.Error("an empty journal should show the empty state")
	}
}

// verifies SPEC §9.2
func TestHeardPlaybackIsBoundedAtTheConfirmedCut(t *testing.T) {
	s, _ := newTestServer(t)
	h := get(t, s, "/review")
	if !strings.Contains(h, "/audio?ref="+url.QueryEscape("blob://tts/s1")+"&amp;to=2080") {
		t.Error("the heard player is not bounded at frames_played; it would play the unheard tail")
	}
	if !strings.Contains(h, "/audio?ref="+url.QueryEscape("blob://tts/s1")+"&amp;from=2080") {
		t.Error("the unheard tail is not offered as its own clip")
	}
}

// accept stores the verdict that makes the fixture pair exportable.
func accept(t *testing.T, decisions curation.Store, chosen string) {
	t.Helper()
	err := decisions.Put(context.Background(), curation.Decision{
		PairID: pairID, ConversationID: "conv-1",
		Status: curation.StatusAccepted, Chosen: chosen,
		DecidedAt: time.Unix(1_760_000_100, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
}

// verifies SPEC §9.1
func TestExportCarriesOnlyAcceptedFixedPairs(t *testing.T) {
	s, decisions := newTestServer(t)

	if body := get(t, s, "/export/dpo.jsonl"); strings.TrimSpace(body) != "" {
		t.Fatalf("an unreviewed pair exported: %q", body)
	}

	accept(t, decisions, "Playing the first Led Zeppelin album.")
	body := get(t, s, "/export/dpo.jsonl")
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 1 {
		t.Fatalf("exported %d rows, want 1: %q", len(lines), body)
	}
	var row struct {
		Prompt []struct {
			Role, Content string
		} `json:"prompt"`
		Chosen   string `json:"chosen"`
		Rejected string `json:"rejected"`
		Meta     struct {
			Pair     string `json:"pair"`
			Versions struct {
				Model string `json:"model"`
			} `json:"versions"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if n := len(row.Prompt); n == 0 || row.Prompt[n-1].Content != "play something by zeppelin" {
		t.Errorf("prompt = %+v, want it to end with the shared prompt", row.Prompt)
	}
	if row.Chosen != "Playing the first Led Zeppelin album." {
		t.Errorf("chosen = %q", row.Chosen)
	}
	// Rejected is the turn as generated: the heard half and the unheard tail.
	if row.Rejected != "I found three albums by that artist" {
		t.Errorf("rejected = %q", row.Rejected)
	}
	if row.Meta.Pair != pairID || row.Meta.Versions.Model != "qwen3-32b@1" {
		t.Errorf("meta = %+v", row.Meta)
	}
}

// verifies SPEC §9.1
func TestUnfixedAcceptIsHeldOutOfTheExport(t *testing.T) {
	s, decisions := newTestServer(t)
	err := decisions.Put(context.Background(), curation.Decision{
		PairID: pairID, ConversationID: "conv-1",
		Status: curation.StatusAccepted, Chosen: "Playing Led Zeppelin one.", Unfixed: true,
		DecidedAt: time.Unix(1_760_000_100, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if body := get(t, s, "/export/dpo.jsonl"); strings.TrimSpace(body) != "" {
		t.Errorf("an unfixed accept exported: %q", body)
	}
}

// verifies SPEC §9.2
func TestExportPageCountsThePilesAndPreviewsTheRows(t *testing.T) {
	s, decisions := newTestServer(t)
	h := get(t, s, "/export")
	if !strings.Contains(h, "Nothing to export yet.") {
		t.Error("an empty dataset should say so")
	}

	accept(t, decisions, "Playing the first Led Zeppelin album.")
	h = get(t, s, "/export")
	for _, want := range []string{
		"/export/dpo.jsonl",                     // the download
		"Playing the first Led Zeppelin album.", // the preview shows the row
		"Exportable",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}
