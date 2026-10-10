package main

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/triage"
)

// bargeInLog writes one barge-in conversation through a real journal, the
// same shape a session records (SPEC §4.4): a cut turn, the correction, and
// the turn that answered it.
func bargeInLog(t *testing.T) *journal.MemStore {
	t.Helper()
	store := journal.NewMemStore()
	v := journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"}
	j := journal.New(store, journal.FixedClock(time.Unix(1_760_000_000, 0)), v)
	for _, r := range zeppelinRecords() {
		if _, err := j.Append(context.Background(), "conv-1", r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
	return store
}

// zeppelinRecords is Alice in the kitchen asking for Zeppelin, cutting the
// list off with "just the first one", and the turn that played it.
func zeppelinRecords() []journal.Record {
	rec := func(kind journal.Kind, audio string, fields ...string) journal.Record {
		r := journal.Record{Kind: kind, AudioRef: audio, Fields: map[string]string{}}
		for i := 0; i+1 < len(fields); i += 2 {
			r.Fields[fields[i]] = fields[i+1]
		}
		return r
	}
	return []journal.Record{
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
	}
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

// The bare address lands on Browse, where the brand link and the guide say
// a reviewer starts.
//
// verifies SPEC §9.2
func TestTheRootLandsOnBrowse(t *testing.T) {
	s, _ := newTestServer(t)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || loc != "/conversations" {
		t.Errorf("GET / = %d to %q, want 303 to /conversations", w.Code, loc)
	}
}

// A page on another site, open in Teagan's browser, posts to the review box
// on the household's network. Every write is refused and stores nothing;
// the same posts from the UI's own pages, or from curl, still land.
//
// verifies SPEC §9.2
func TestAWriteFromAnotherSiteIsRefused(t *testing.T) {
	s, decisions := householdReplayServer(t)
	writes := []struct {
		target string
		form   url.Values
	}{
		{"/pairs/" + url.PathEscape(pairZeppel) + "/accept-anyway", nil},
		{turnURL(convZeppel, 2, "labels/wrong_tool"), url.Values{"note": {shouldHaveZeppel}}},
		{turnURL(convZeppel, 2, "note"), url.Values{"note": {shouldHaveZeppel}}},
		{"/replays/" + convZeppel, url.Values{"model": {"qwen3-32b"}, "prompt": {"Lead with the count."}}},
		{"/replays/" + convZeppel + "/runs/1/turns/2/promote", nil},
	}
	send := func(target string, form url.Values, header ...string) int {
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		return w.Code
	}
	for _, wr := range writes {
		if code := send(wr.target, wr.form, "Sec-Fetch-Site", "cross-site"); code != http.StatusForbidden {
			t.Errorf("cross-site POST %s = %d, want 403", wr.target, code)
		}
		// An older browser sends no Sec-Fetch-Site, but says where it came from.
		if code := send(wr.target, wr.form, "Origin", "https://recipes.example"); code != http.StatusForbidden {
			t.Errorf("foreign-origin POST %s = %d, want 403", wr.target, code)
		}
	}
	if ps := mustPairs(t, s); len(ps) == 0 {
		t.Fatal("the household harvested no pairs")
	}
	if d, ok, _ := decisions.Get(context.Background(), pairZeppel); ok {
		t.Errorf("a refused write stored %+v", d)
	}
	if rs, _ := decisions.Reruns(context.Background(), convZeppel); len(rs) > 0 {
		t.Errorf("a refused write kept %d re-runs", len(rs))
	}
	if a, _ := decisions.Annotations(context.Background(), convZeppel); len(a) > 0 {
		t.Errorf("a refused write labelled %+v", a)
	}

	for _, ok := range [][]string{
		{"Sec-Fetch-Site", "same-origin"},
		{"Origin", "http://example.com"}, // httptest's own host
		nil,                              // curl, or the demo's in-tab server
	} {
		if code := send(turnURL(convZeppel, 2, "labels/too_slow"), nil, ok...); code != http.StatusOK {
			t.Errorf("same-origin POST with %v = %d, want 200", ok, code)
		}
	}
}

// A template that fails halfway is a 500 with the error, not the half page
// it had written with the error pasted under it.
//
// verifies SPEC §9.2
func TestAPageThatFailsToRenderSendsOnlyTheError(t *testing.T) {
	s, _ := newTestServer(t)
	template.Must(s.tpl.New("page-half").Parse(`{{template "doc-start" .Doc}}<h1>Curate</h1>{{.Pair.Chosen.Text}}`))
	w := httptest.NewRecorder()
	s.render(w, "page-half", map[string]any{"Doc": s.doc("Curate"), "Pair": map[string]string{"Chosen": "Playing Led Zeppelin one."}})
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if b := w.Body.String(); strings.Contains(b, "<h1>Curate</h1>") || !strings.Contains(b, "page-half") {
		t.Errorf("body = %q, want only the error naming the template", b)
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
	// The download is harvest.Export's conversational preference shape: every
	// side a message array, so TRL-style loaders read it unchanged.
	type msg struct{ Role, Content string }
	var row struct {
		Prompt   []msg `json:"prompt"`
		Chosen   []msg `json:"chosen"`
		Rejected []msg `json:"rejected"`
		Meta     struct {
			ID       string `json:"id"`
			Curated  bool   `json:"curated"`
			Versions struct {
				Model string `json:"model"`
			} `json:"versions"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("not the conversational shape: %v", err)
	}
	if n := len(row.Prompt); n == 0 || row.Prompt[n-1].Content != "play something by zeppelin" {
		t.Errorf("prompt = %+v, want it to end with the shared prompt", row.Prompt)
	}
	if len(row.Chosen) != 1 || row.Chosen[0] != (msg{"assistant", "Playing the first Led Zeppelin album."}) {
		t.Errorf("chosen = %+v", row.Chosen)
	}
	// Rejected is the turn as generated: the heard half and the unheard tail.
	if len(row.Rejected) != 1 || row.Rejected[0] != (msg{"assistant", "I found three albums by that artist"}) {
		t.Errorf("rejected = %+v", row.Rejected)
	}
	if row.Meta.ID != pairID || !row.Meta.Curated || row.Meta.Versions.Model != "qwen3-32b@1" {
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

// verifies SPEC §9.2
func TestASlowDownloadDoesNotHoldTheReviewUI(t *testing.T) {
	s, decisions := newTestServer(t)
	accept(t, decisions, "Playing the first Led Zeppelin album.")

	// A client that never reads: the handler blocks on its first write.
	stalled := &blockingWriter{header: http.Header{}, release: make(chan struct{}), writing: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.routes().ServeHTTP(stalled, httptest.NewRequest(http.MethodGet, "/export/dpo.jsonl", nil))
	}()
	<-stalled.writing

	served := make(chan struct{})
	go func() {
		defer close(served)
		get(t, s, "/curate/pairs")
	}()
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Error("a stalled download held the lock every page needs")
	}
	close(stalled.release)
	<-done
}

// blockingWriter stalls on Write until released, like a client that stopped reading.
type blockingWriter struct {
	header  http.Header
	release chan struct{}
	writing chan struct{}
	once    sync.Once
}

func (b *blockingWriter) Header() http.Header { return b.header }
func (b *blockingWriter) WriteHeader(int)     {}
func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.writing) })
	<-b.release
	return len(p), nil
}

// withFailure adds a second conversation whose one tool timed out, recorded
// later than the barge-in so it heads the queue.
func withFailure(t *testing.T, store *journal.MemStore) *journal.MemStore {
	t.Helper()
	v := journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"}
	j := journal.New(store, journal.FixedClock(time.Unix(1_760_003_600, 0)), v)
	for _, r := range []journal.Record{
		{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "office", "speaker_id": "teagan", "resumed": "false"}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/9", Fields: map[string]string{"text": "is the garage door closed", "speaker_id": "teagan"}},
		{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "cover.state", "call_id": "c1", "args_json": "{}"}},
		{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "c1", "outcome": "timed_out"}},
		{Kind: journal.KindModelCompleted, Fields: map[string]string{"completion_json": "{}", "finish_reason": "stop"}},
	} {
		if _, err := j.Append(context.Background(), "conv-2", r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
	return store
}

func newTriageServer(t *testing.T) *server {
	t.Helper()
	return newServer(withFailure(t, bargeInLog(t)), curation.NewMemStore(), fixtureBlobs(t), time.Now)
}

// verifies SPEC §9.2
func TestTriageQueuesEverySignalNewestFirst(t *testing.T) {
	h := get(t, newTriageServer(t), "/queue")
	failure := strings.Index(h, "is the garage door closed")
	barge := strings.Index(h, "play something by zeppelin")
	if failure < 0 || barge < 0 {
		t.Fatalf("queue is missing a signal (failure at %d, barge-in at %d)", failure, barge)
	}
	if failure > barge {
		t.Error("the later conversation should head the queue")
	}
	for _, want := range []string{
		"cover.state timed_out",                   // why the failure is here
		"cut → “just the first one”",              // why the barge-in is here
		"/review?pair=" + url.QueryEscape(pairID), // a barge-in opens its pair
		"teagan · office",                         // who and where
	} {
		if !strings.Contains(h, want) {
			t.Errorf("queue is missing %q", want)
		}
	}
}

// Triage tells the time on the server's clock, as Browse and Replay do, not
// in whatever zone the process happened to start in.
//
// verifies SPEC §9.2
func TestTriageTellsTheTimeOnTheServersClock(t *testing.T) {
	pacific := time.FixedZone("PDT", -7*60*60)
	now := func() time.Time { return time.Unix(1_760_010_000, 0).In(pacific) }
	s := newServer(withFailure(t, bargeInLog(t)), curation.NewMemStore(), fixtureBlobs(t), now)
	// Alice's barge-in was at 08:53 UTC, before two in the morning in Pacific.
	if h := get(t, s, "/queue"); !strings.Contains(h, "Oct 9 01:53") {
		t.Error("Triage does not show the barge-in at 01:53 on the server's clock")
	}
	if h := get(t, s, "/replays"); !strings.Contains(h, "9 Oct 01:53") {
		t.Error("Replay does not show the conversation at 01:53 on the server's clock")
	}
}

// verifies SPEC §9.2
func TestTriageTabsFilterBySignal(t *testing.T) {
	s := newTriageServer(t)
	h := get(t, s, "/queue?tab=failure")
	if !strings.Contains(h, "is the garage door closed") || strings.Contains(h, "play something by zeppelin") {
		t.Error("the failures tab should hold the failure and only it")
	}
	h = get(t, s, "/queue?tab=speaker-flip")
	if !strings.Contains(h, "Nothing in this pile.") {
		t.Error("an empty tab should say so")
	}
}

// withWakeReject writes a stage-two rejection to the kitchen device's log,
// where the listener records them (listen.DeviceConversation).
func withWakeReject(t *testing.T, store *journal.MemStore) *journal.MemStore {
	t.Helper()
	j := journal.New(store, journal.FixedClock(time.Unix(1_760_001_800, 0)), journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"})
	r := journal.Record{Kind: journal.KindWakeRejected, AudioRef: "blob://wake/1", Fields: map[string]string{"reason": "no_speech"}}
	if _, err := j.Append(context.Background(), "device:kitchen", r); err != nil {
		t.Fatalf("append wake reject: %v", err)
	}
	return store
}

func newBrowseServer(t *testing.T) *server {
	t.Helper()
	store := withWakeReject(t, withFailure(t, bargeInLog(t)))
	now := func() time.Time { return time.Unix(1_760_010_000, 0).UTC() }
	return newServer(store, curation.NewMemStore(), fixtureBlobs(t), now)
}

// verifies SPEC §9.2
func TestBrowseLaysTheDayOutBySatellite(t *testing.T) {
	h := get(t, newBrowseServer(t), "/conversations")
	for _, want := range []string{
		"Thursday 9 October",               // today, in the server's zone
		`class="day-lanes__name">kitchen<`, // one lane per satellite
		`class="day-lanes__name">office<`,
		`href="/conversations/conv-1"`, // sessions and rows open their conversation
		"is-flagged tone-people",       // the barge-in flags its session
		"is-flagged tone-home",         // the failure flags its session
		"day-lanes__reject",            // the kitchen's rejected wake
		"play something by zeppelin",   // the list names each conversation by its first ask
		"is the garage door closed",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("Browse is missing %q", want)
		}
	}
	if strings.Contains(h, `href="/conversations/device:kitchen"`) {
		t.Error("a device's rejection log is not a conversation")
	}
}

// verifies SPEC §9.2
func TestBrowseOnAQuietDaySaysSo(t *testing.T) {
	h := get(t, newBrowseServer(t), "/conversations?day=2025-10-01")
	if !strings.Contains(h, "No conversations this day.") {
		t.Error("a quiet day should say so")
	}
	if !strings.Contains(h, "/conversations?day=2025-10-02") {
		t.Error("the day after should be one click away")
	}
}

// verifies SPEC §9.2
func TestConversationPageReplaysTheLogWithItsAudio(t *testing.T) {
	h := get(t, newBrowseServer(t), "/conversations/conv-1")
	for _, want := range []string{
		"play something by zeppelin",
		"I found three",
		" albums by that artist", // the unheard tail, as the log kept it
		"speak",                  // the tool the turn dispatched
		`id="seq-5"`,             // the cut, addressable from Triage
		"/audio?ref=" + url.QueryEscape("blob://mic/1"),                   // the prompt, playable
		"/audio?ref=" + url.QueryEscape("blob://tts/s1") + "&amp;to=2080", // the heard half only
	} {
		if !strings.Contains(h, want) {
			t.Errorf("conversation page is missing %q", want)
		}
	}
	w := httptest.NewRecorder()
	newBrowseServer(t).routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/conversations/conv-9", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown conversation = %d, want 404", w.Code)
	}
}

// A screen reader names each player by whose clip it is, not "audio" five
// times over, and each session on the lane by what flagged it.
//
// verifies SPEC §9.2
func TestEveryPlayerAndSessionSaysWhatItIs(t *testing.T) {
	s := newBrowseServer(t)
	for _, c := range []struct {
		path  string
		wants []string
	}{
		{"/conversations/conv-1", []string{
			`aria-label="#2 utterance transcribed · alice"`,
			`aria-label="#5 speech truncated · speaking"`,
		}},
		{"/review", []string{
			`aria-label="rejected · heard · assistant"`,
			`aria-label="barge-in · alice"`,
			`aria-label="correction · alice"`,
		}},
		{"/conversations", []string{
			`aria-label="kitchen 08:53 · barge-in pair"`,
			`aria-label="office 09:53 · failure"`,
		}},
	} {
		h := get(t, s, c.path)
		if n, named := strings.Count(h, "<audio "), strings.Count(h, "<audio aria-label="); n != named {
			t.Errorf("%s: %d of %d players have no name", c.path, n-named, n)
		}
		for _, want := range c.wants {
			if !strings.Contains(h, want) {
				t.Errorf("%s is missing %s", c.path, want)
			}
		}
	}
}

// verifies SPEC §9.2
func TestTriageFailureRowsOpenTheirConversationAtTheEvent(t *testing.T) {
	h := get(t, newTriageServer(t), "/queue?tab=failure")
	if !strings.Contains(h, `href="/conversations/conv-2#seq-4"`) {
		t.Error("a failure row should open its conversation at the failing event")
	}
}

// A detached tool's failure can land after the conversation moved rooms; the
// lane it flags is the one whose turn called it.
//
// verifies SPEC §9.2
func TestALateFailureFlagsTheSessionThatCalledTheTool(t *testing.T) {
	at := time.Unix(1_760_000_000, 0)
	ev := func(seq uint64, kind journal.Kind, fields ...string) journal.Event {
		e := journal.Event{Seq: seq, At: at, Kind: kind, Fields: map[string]string{}}
		for i := 0; i+1 < len(fields); i += 2 {
			e.Fields[fields[i]] = fields[i+1]
		}
		return e
	}
	events := []journal.Event{
		ev(1, journal.KindSessionOpened, "satellite", "kitchen"),
		ev(2, journal.KindUtteranceTranscribed, "text", "download the new album"),
		ev(3, journal.KindToolCalled, "tool", "media.fetch", "call_id", "c1"),
		ev(4, journal.KindSessionOpened, "satellite", "office"),
		ev(5, journal.KindToolResult, "call_id", "c1", "outcome", "error"),
	}
	sig := triage.Signal{Kind: triage.KindFailure, Seq: 5, Satellite: "kitchen", Session: 1}
	c := summarize("conv-1", events, []triage.Signal{sig})
	if len(c.sessions[0].signals) != 1 || len(c.sessions[1].signals) != 0 {
		t.Errorf("kitchen has %d signals, office %d; want the failure on the kitchen",
			len(c.sessions[0].signals), len(c.sessions[1].signals))
	}
}

// withRepeat adds Alan in the kitchen asking for an oven timer, being asked
// how long instead of getting one, and asking again 6.2 s later.
func withRepeat(t *testing.T, store *journal.MemStore) *journal.MemStore {
	t.Helper()
	clk := &stepClock{}
	j := journal.New(store, clk, journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"})
	start := time.Unix(1_760_007_200, 0)
	for _, r := range []struct {
		sec float64
		rec journal.Record
	}{
		{0, journal.Record{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen", "speaker_id": "alan", "resumed": "false"}}},
		{0.2, journal.Record{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/20", Fields: map[string]string{"text": "set a timer for the oven", "speaker_id": "alan"}}},
		{2.6, journal.Record{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "speak", "call_id": "s1", "args_json": `{"mode":"queue","streamed":true}`}}},
		{4.4, journal.Record{Kind: journal.KindSpeechSpoken, AudioRef: "blob://tts/s20", Fields: map[string]string{"text": "Sure, how long?", "frames_played": "28800"}}},
		{4.5, journal.Record{Kind: journal.KindModelCompleted, Fields: map[string]string{"completion_json": "{}", "finish_reason": "stop"}}},
		{6.4, journal.Record{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/21", Fields: map[string]string{"text": "set a timer for twelve minutes", "speaker_id": "alan"}}},
		{6.9, journal.Record{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "ha_call_service", "call_id": "c1", "args_json": `{"domain":"timer","service":"start","entity_id":"timer.oven","data":{"duration":"00:12:00"}}`}}},
	} {
		clk.now = start.Add(time.Duration(r.sec * float64(time.Second)))
		if _, err := j.Append(context.Background(), "conv-3", r.rec); err != nil {
			t.Fatalf("append %s: %v", r.rec.Kind, err)
		}
	}
	return store
}

// stepClock is set before each append, so a fixture says when each thing
// was heard.
type stepClock struct{ now time.Time }

func (c *stepClock) Now() time.Time { return c.now }

func newRepeatServer(t *testing.T) *server {
	t.Helper()
	store := withRepeat(t, withFailure(t, bargeInLog(t)))
	return newServer(store, curation.NewMemStore(), fixtureBlobs(t), func() time.Time { return time.Unix(1_760_010_000, 0).UTC() })
}

// verifies SPEC §9.1
func TestTriageQueuesARepeatedRequestAtTheSecondAsk(t *testing.T) {
	h := get(t, newRepeatServer(t), "/queue?tab=repeated")
	for _, want := range []string{
		`href="/queue?tab=repeated">Repeated <span class="tabs__count">1</span>`,
		"set a timer for twelve minutes",
		"asked again 6.2 s after “set a timer for the oven” · no tool call on the first ask",
		`href="/conversations/conv-3#seq-6"`,
		"alan · kitchen",
		"Barge-ins, failures, repeated asks, slow answers and speaker flips",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("repeated tab is missing %q", want)
		}
	}
	if strings.Contains(h, "play something by zeppelin") || strings.Contains(h, "is the garage door closed") {
		t.Error("the repeated tab should hold only the repeat")
	}
}

// verifies SPEC §9.2
func TestBrowseFlagsTheSessionWithARepeat(t *testing.T) {
	h := get(t, newRepeatServer(t), "/conversations")
	if !strings.Contains(h, `class="day-lanes__session is-flagged tone-voice" href="/conversations/conv-3"`) {
		t.Error("the kitchen session with the repeated ask should be flagged")
	}
}

// withSlow adds conv-4: Teagan in the hallway asks whether the front door is
// locked and waits 2.84 s for the answer to start, then asks for the porch
// light and hears it inside the budget. Only the first answer is slow.
func withSlow(t *testing.T, store *journal.MemStore) *journal.MemStore {
	t.Helper()
	clk := &stepClock{}
	j := journal.New(store, clk, journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"})
	start := time.Unix(1_760_008_400, 0)
	for _, r := range []struct {
		sec float64
		rec journal.Record
	}{
		{0, journal.Record{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "hallway", "speaker_id": "teagan", "resumed": "false"}}},
		{0.4, journal.Record{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/30", Fields: map[string]string{"text": "is the front door locked", "speaker_id": "teagan"}}},
		{1.1, journal.Record{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "ha_get_state", "call_id": "c1", "args_json": `{"entity_id":"lock.front_door"}`}}},
		{2.3, journal.Record{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "c1", "outcome": "ok", "result_json": `{"state":"locked"}`}}},
		{2.4, journal.Record{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "speak", "call_id": "s1", "args_json": `{"mode":"queue","streamed":true}`}}},
		{3.0, journal.Record{Kind: journal.KindSpeechStarted, Fields: map[string]string{"call_id": "s1", "wait_ms": "2840"}}},
		{4.6, journal.Record{Kind: journal.KindSpeechSpoken, AudioRef: "blob://tts/s30", Fields: map[string]string{"text": "The front door is locked.", "frames_played": "25600"}}},
		{4.7, journal.Record{Kind: journal.KindModelCompleted, Fields: map[string]string{"completion_json": "{}", "finish_reason": "stop"}}},
		{9.2, journal.Record{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/31", Fields: map[string]string{"text": "turn on the porch light", "speaker_id": "teagan"}}},
		{9.5, journal.Record{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "ha_call_service", "call_id": "c2", "args_json": `{"domain":"light","service":"turn_on","entity_id":"light.porch"}`}}},
		{9.6, journal.Record{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "speak", "call_id": "s2", "args_json": `{"mode":"queue","streamed":true}`}}},
		{9.8, journal.Record{Kind: journal.KindSpeechStarted, Fields: map[string]string{"call_id": "s2", "wait_ms": "560"}}},
		{11.0, journal.Record{Kind: journal.KindSpeechSpoken, AudioRef: "blob://tts/s31", Fields: map[string]string{"text": "Porch light is on.", "frames_played": "19200"}}},
		{11.1, journal.Record{Kind: journal.KindModelCompleted, Fields: map[string]string{"completion_json": "{}", "finish_reason": "stop"}}},
	} {
		clk.now = start.Add(time.Duration(r.sec * float64(time.Second)))
		if _, err := j.Append(context.Background(), "conv-4", r.rec); err != nil {
			t.Fatalf("append %s: %v", r.rec.Kind, err)
		}
	}
	return store
}

func newSlowServer(t *testing.T) *server {
	t.Helper()
	store := withSlow(t, withRepeat(t, withFailure(t, bargeInLog(t))))
	return newServer(store, curation.NewMemStore(), fixtureBlobs(t), func() time.Time { return time.Unix(1_760_010_000, 0).UTC() })
}

// verifies SPEC §11, §9.2
func TestTriageQueuesASlowAnswerAtItsFirstFrame(t *testing.T) {
	h := get(t, newSlowServer(t), "/queue?tab=slow")
	for _, want := range []string{
		`href="/queue?tab=slow">Slow <span class="tabs__count">1</span>`,
		"is the front door locked",
		"first audio 2.8 s after the ask · target 0.7 s",
		`href="/conversations/conv-4#seq-6"`,
		"teagan · hallway",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("slow tab is missing %q", want)
		}
	}
	if strings.Contains(h, "turn on the porch light") {
		t.Error("an answer inside the budget should not be in the slow tab")
	}
}

// verifies SPEC §11, §8
func TestTheLogShowsWhenEachAnswerStarted(t *testing.T) {
	h := get(t, newSlowServer(t), "/conversations/conv-4")
	for _, want := range []string{
		"speech started",
		"first audio 2.84 s after the ask",
		"first audio 0.56 s after the ask",
		"call s1",
		"first audio 2.8 s after the ask · target 0.7 s", // the slow signal on its row
	} {
		if !strings.Contains(h, want) {
			t.Errorf("conversation log is missing %q", want)
		}
	}
}

// verifies SPEC §9.2
func TestBrowseFlagsTheSessionWithASlowAnswer(t *testing.T) {
	h := get(t, newSlowServer(t), "/conversations")
	if !strings.Contains(h, `class="day-lanes__session is-flagged tone-voice" href="/conversations/conv-4"`) {
		t.Error("the hallway session with the slow answer should be flagged")
	}
}
