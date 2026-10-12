package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/erasure"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// Alan asks for his sister's visit to go. Its page offers the deletion
// behind a second press; the press goes by retention's path (every clip,
// then the reviewer's label, then the summary, then the log); the reviewer
// lands on the day in Browse, told it went; and nothing of it is left on
// any screen, in the export, or in the stores. Deletions says it went and
// how much, never what was said.
//
// verifies SPEC §8
func TestDeletingTheGuestsVisitRemovesItEverywhere(t *testing.T) {
	v := newVisit(t)
	page := get(t, v.s, conversationHref(convGuest))
	for _, want := range []string{
		`<details class="confirm tone-home" id="delete-conversation">`,
		`action="` + deleteHref(convGuest) + `"`,
		"Its 8 events, its 5 clips, every verdict, label and re-run on it",
		`<input type="hidden" name="confirm" value="delete">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the visit's page is missing %q", want)
		}
	}

	w := send(v.s.routes(), deleteHref(convGuest), confirm("delete"))
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", w.Code, w.Body.String())
	}
	back := "/conversations?day=2025-10-09&deleted=" + url.QueryEscape(convGuest)
	if got := w.Header().Get("HX-Redirect"); got != back {
		t.Errorf("redirects to %q, want %q", got, back)
	}

	// Retention's path, in retention's order: the log goes last, so a
	// deletion that fails partway is found again by its log.
	want := []string{
		"remove " + guestAlanAsk, "remove " + guestGoAhead, "remove " + guestAsk, "remove " + guestAskSecond, "remove " + guestAnswer,
		"forget curation " + convGuest, "forget summaries " + convGuest, "delete log " + convGuest,
	}
	if got := v.tr.seen(); !slices.Equal(got, want) {
		t.Errorf("deleted by\n%v\nwant\n%v", got, want)
	}
	for _, ref := range []string{guestAlanAsk, guestGoAhead, guestAsk, guestAskSecond, guestAnswer} {
		if v.kept(ref) {
			t.Errorf("%s outlived the deletion", ref)
		}
	}
	if got := v.log(t, convGuest); len(got) != 0 {
		t.Errorf("the visit keeps %d events", len(got))
	}
	if as, _ := v.decisions.Annotations(context.Background(), convGuest); len(as) != 0 {
		t.Errorf("the label on her turn outlived the deletion: %v", as)
	}
	if ss, _ := v.memories.AllSummaries(context.Background()); len(ss) != 0 {
		t.Errorf("Alan's summary of the visit outlived it: %v", ss)
	}

	browse := get(t, v.s, back)
	if !strings.Contains(browse, "Deleted "+convGuest+" for good.") || !strings.Contains(browse, "8 events, 5 clips, and a reviewer&#39;s curated work") {
		t.Error("Browse does not say the visit went")
	}
	for _, target := range []string{"/conversations?day=2025-10-09", "/queue", "/review", "/replays", "/curate/pairs", "/export", "/export/dpo.jsonl", "/export/wake-negatives.jsonl", "/memory"} {
		body := get(t, v.s, target)
		if strings.Contains(body, convGuest+`"`) || strings.Contains(body, conversationHref(convGuest)) || strings.Contains(body, "pharmacy") {
			t.Errorf("%s still shows the visit", target)
		}
	}
	r := httptest.NewRequest(http.MethodGet, conversationHref(convGuest), nil)
	gone := httptest.NewRecorder()
	v.s.routes().ServeHTTP(gone, r)
	if gone.Code != http.StatusNotFound {
		t.Errorf("the visit's page = %d, want 404", gone.Code)
	}

	trail := get(t, v.s, deletionsRoute)
	for _, want := range []string{convGuest, "8 events, 5 clips", "cannot be restored"} {
		if !strings.Contains(trail, want) {
			t.Errorf("Deletions is missing %q", want)
		}
	}
	if strings.Contains(trail, "pharmacy") || strings.Contains(trail, "sister") {
		t.Error("Deletions keeps what was said")
	}
	records, _ := v.s.erasures.List(context.Background())
	if len(records) != 1 || records[0].Kind != erasure.KindConversation || !records[0].At.Equal(household.ReviewedAt()) {
		t.Errorf("records = %+v, want the one deletion at the reviewer's evening", records)
	}
}

// withLiveKitchen is Teagan talking to the kitchen right now: a session
// opened a minute before the reviewer sat down and still open.
func withLiveKitchen(t *testing.T, store journal.Store) string {
	t.Helper()
	const live = "conv-2229-kitchen"
	j := journal.New(store, journal.FixedClock(household.ReviewedAt().Add(-time.Minute)), household.Versions())
	for _, r := range []journal.Record{
		rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "teagan", "resumed", "false"),
		rec(journal.KindUtteranceTranscribed, "blob://mic/live-ask", "text", "is the back door locked", "speaker_id", "teagan"),
	} {
		if _, err := j.Append(context.Background(), live, r); err != nil {
			t.Fatal(err)
		}
	}
	return live
}

// Every way a deletion is refused leaves the log, its audio and its rows
// as they were, and says why in a sentence the alert region can show.
//
// verifies SPEC §4.5, §8
func TestADeletionIsRefusedAndSaysWhy(t *testing.T) {
	v := newVisit(t)
	live := withLiveKitchen(t, v.store)
	for _, c := range []struct {
		name, target string
		form         url.Values
		code         int
		says         string
	}{
		{"a live session", deleteHref(live), confirm("delete"), http.StatusConflict, "live session"},
		{"an unknown id", deleteHref("conv-0000-attic"), confirm("delete"), http.StatusNotFound, `No conversation "conv-0000-attic"`},
		{"a satellite's own log", deleteHref("device:kitchen"), confirm("delete"), http.StatusConflict, "chorusd writes all day"},
		{"no second press", deleteHref(convGuest), nil, http.StatusBadRequest, "second press"},
		{"the wrong second press", deleteHref(convGuest), confirm("drop"), http.StatusBadRequest, "second press"},
	} {
		w := send(v.s.routes(), c.target, c.form)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.says) {
			t.Errorf("%s = %d %q, want %d saying %q", c.name, w.Code, w.Body.String(), c.code, c.says)
		}
	}
	if calls := v.tr.seen(); len(calls) != 0 {
		t.Errorf("a refused deletion still deleted: %v", calls)
	}
	if len(v.log(t, live)) != 2 || len(v.log(t, convGuest)) != 8 {
		t.Error("a refused deletion changed a log")
	}
	if page := get(t, v.s, conversationHref(live)); strings.Contains(page, deleteHref(live)) || !strings.Contains(page, "live session") {
		t.Error("the live kitchen offers a deletion it would refuse, or does not say why it cannot")
	}
	if page := get(t, v.s, "/conversations/device:kitchen"); strings.Contains(page, `id="delete-conversation"`) || strings.Contains(page, deleteHref("device:kitchen")) {
		t.Error("the kitchen's own log offers a deletion")
	}
	if records, _ := v.s.erasures.List(context.Background()); len(records) != 0 {
		t.Errorf("a refusal left a record: %+v", records)
	}
}

// A reviewer who should not delete gets a review UI with no deletion on
// any screen, and every destructive route refuses them outright.
//
// verifies SPEC §8
func TestReadOnlyHidesAndRefusesEveryDeletion(t *testing.T) {
	v := newVisit(t)
	v.s.readOnly = true
	for _, target := range []string{conversationHref(convGuest), conversationHref("device:kitchen"), memoryRoute} {
		page := get(t, v.s, target)
		if strings.Contains(page, `class="confirm`) {
			t.Errorf("%s offers a deletion in read-only mode", target)
		}
		if !strings.Contains(page, "Read-only") {
			t.Errorf("%s does not say it is read-only", target)
		}
	}
	for _, c := range []struct {
		target, action string
	}{
		{deleteHref(convGuest), "delete"},
		{dropAudioHref(convGuest, guestAskSeq), "drop"},
		{dropAudioHref("device:kitchen", 7), "drop"},
		{memoryRoute + "/alice/facts/" + alicesAllergy.ID + "/forget", "forget"},
		{memoryRoute + "/alan/summaries/" + convGuest + "/forget", "forget"},
	} {
		w := send(v.s.routes(), c.target, confirm(c.action))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "-read-only") {
			t.Errorf("POST %s = %d %q, want 403 naming -read-only", c.target, w.Code, w.Body.String())
		}
	}
	if calls := v.tr.seen(); len(calls) != 0 {
		t.Errorf("read-only still deleted: %v", calls)
	}
	if ms, _ := v.memories.AllMemories(context.Background()); len(ms) != 2 {
		t.Errorf("read-only forgot a memory: %v", ms)
	}
}

// -read-only's default comes from the environment, and a value that is not
// a yes or a no stops the UI rather than guessing.
//
// verifies SPEC §8
func TestReadOnlyReadsItsEnvironment(t *testing.T) {
	for v, want := range map[string]bool{"": false, "true": true, "1": true, " TRUE ": true, "false": false, "0": false} {
		got, err := parseReadOnly(v)
		if err != nil || got != want {
			t.Errorf("parseReadOnly(%q) = %v, %v, want %v", v, got, err, want)
		}
	}
	if _, err := parseReadOnly("sometimes"); err == nil || !strings.Contains(err.Error(), readOnlyEnv) {
		t.Errorf("parseReadOnly(sometimes) = %v, want an error naming %s", err, readOnlyEnv)
	}
}

// Every destructive route is a POST, behind the same cross-origin check and
// Host allow-list as the verdicts: a recipe site open in Alan's browser
// cannot delete his sister's visit, and neither can a page that re-pointed
// its own name at the box.
//
// verifies SPEC §1, §8
func TestDeletionsArePostOnlyAndSameOriginOnly(t *testing.T) {
	v := newVisit(t)
	h := v.s.handler(defaultHosts(defaultAddr))
	targets := []string{
		deleteHref(convGuest), dropAudioHref(convGuest, guestAskSeq),
		memoryRoute + "/alice/facts/" + alicesAllergy.ID + "/forget", memoryRoute + "/alan/summaries/" + convGuest + "/forget",
	}
	for _, target := range targets {
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader("confirm=delete&confirm=drop&confirm=forget"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Host = "127.0.0.1:8080"
		r.Header.Set("Origin", "http://recipes.example")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("a cross-site POST %s = %d, want 403", target, w.Code)
		}

		foreign := httptest.NewRequest(http.MethodPost, target, strings.NewReader("confirm=delete"))
		foreign.Host = "recipes.example"
		w = httptest.NewRecorder()
		h.ServeHTTP(w, foreign)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("a POST %s addressed to a foreign host = %d, want 421", target, w.Code)
		}

		w = getAs(h, target)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %d, want 405", target, w.Code)
		}
	}
	if calls := v.tr.seen(); len(calls) != 0 {
		t.Errorf("a refused request still deleted: %v", calls)
	}
}

// Alan keeps the visit but drops what his sister said: her ask's page row
// offers it behind a second press, both channels go and are recorded as
// redacted in the log, the page shows the clip as gone where the player
// was, and the answer still plays.
//
// verifies SPEC §8, §9.2
func TestDroppingTheGuestsAskRedactsItsClip(t *testing.T) {
	v := newVisit(t)
	page := get(t, v.s, conversationHref(convGuest))
	if !strings.Contains(page, `action="`+dropAudioHref(convGuest, guestAskSeq)+`"`) || !strings.Contains(page, "Removes this clip and its second channel for good") {
		t.Fatal("her ask offers no drop")
	}

	w := send(v.s.routes(), dropAudioHref(convGuest, guestAskSeq), confirm("drop"))
	if w.Code != http.StatusOK || w.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("drop = %d %v: %s", w.Code, w.Header(), w.Body.String())
	}
	if want := []string{"remove " + guestAsk, "remove " + guestAskSecond}; !slices.Equal(v.tr.seen(), want) {
		t.Errorf("dropped by %v, want %v", v.tr.seen(), want)
	}
	if v.kept(guestAsk) || v.kept(guestAskSecond) || !v.kept(guestAnswer) {
		t.Error("the drop did not take exactly her ask")
	}
	var reasons []string
	for _, e := range v.log(t, convGuest) {
		if e.Kind == journal.KindAudioDropped {
			reasons = append(reasons, e.Fields["audio_ref"]+" "+e.Fields["reason"])
		}
	}
	if want := []string{guestAsk + " redacted", guestAskSecond + " redacted"}; !slices.Equal(reasons, want) {
		t.Errorf("the log records %v, want %v", reasons, want)
	}

	page = get(t, v.s, conversationHref(convGuest))
	if strings.Contains(page, "/audio?ref="+url.QueryEscape(guestAsk)) || strings.Contains(page, "/audio?ref="+url.QueryEscape(guestAskSecond)) {
		t.Error("the page still plays her ask")
	}
	for _, want := range []string{"audio redacted", "/audio?ref=" + url.QueryEscape(guestAnswer), "is there a pharmacy open near here tonight"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	if strings.Contains(page, `action="`+dropAudioHref(convGuest, guestAskSeq)+`"`) {
		t.Error("the page still offers to drop a clip that is gone")
	}
	if row := rowOf(page, guestAskSeq); !strings.Contains(row, "audio redacted") {
		t.Errorf("her ask's own row does not say its audio went:\n%s", row)
	}
	r := httptest.NewRequest(http.MethodGet, "/audio?ref="+url.QueryEscape(guestAsk), nil)
	played := httptest.NewRecorder()
	v.s.routes().ServeHTTP(played, r)
	if played.Code != http.StatusNotFound {
		t.Errorf("her ask still plays: %d", played.Code)
	}

	records, _ := v.s.erasures.List(context.Background())
	if len(records) != 1 || records[0].Kind != erasure.KindAudio || records[0].Subject != guestAsk+" "+guestAskSecond {
		t.Errorf("records = %+v, want her ask's two clips", records)
	}
	if trail := get(t, v.s, deletionsRoute); !strings.Contains(trail, "2 clips of #5") || strings.Contains(trail, "pharmacy") {
		t.Error("Deletions does not count the drop, or keeps what she said")
	}
}

// A drop is refused for a clip already gone, an event with no audio or
// that does not exist, a live session's log, the house log, and a
// post that skipped its second press.
//
// verifies SPEC §4.5, §8
func TestADropIsRefusedAndSaysWhy(t *testing.T) {
	v := newVisit(t)
	live := withLiveKitchen(t, v.store)
	if w := send(v.s.routes(), dropAudioHref(convGuest, guestAskSeq), confirm("drop")); w.Code != http.StatusOK {
		t.Fatalf("first drop = %d: %s", w.Code, w.Body.String())
	}
	before := len(v.tr.seen())
	for _, c := range []struct {
		name, target string
		form         url.Values
		code         int
		says         string
	}{
		{"a clip already gone", dropAudioHref(convGuest, guestAskSeq), confirm("drop"), http.StatusConflict, "no audio still kept"},
		{"a completion", dropAudioHref(convGuest, 3), confirm("drop"), http.StatusConflict, "no audio still kept"},
		{"no such event", dropAudioHref(convGuest, 99), confirm("drop"), http.StatusNotFound, "no such event"},
		{"not an event", conversationHref(convGuest) + "/events/first/audio/drop", confirm("drop"), http.StatusNotFound, "is not an event"},
		{"a live session", dropAudioHref(live, 2), confirm("drop"), http.StatusConflict, "live session"},
		{"the house log", dropAudioHref(houseLog, 1), confirm("drop"), http.StatusConflict, "chorusd writes all day"},
		{"presence in a satellite's log", dropAudioHref("device:kitchen", 1), confirm("drop"), http.StatusConflict, "no audio still kept"},
		{"no second press", dropAudioHref(convGuest, 7), nil, http.StatusBadRequest, "second press"},
	} {
		w := send(v.s.routes(), c.target, c.form)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.says) {
			t.Errorf("%s = %d %q, want %d saying %q", c.name, w.Code, w.Body.String(), c.code, c.says)
		}
	}
	if got := v.tr.seen(); len(got) != before {
		t.Errorf("a refused drop still removed: %v", got[before:])
	}
	if !v.kept(guestAnswer) || !v.kept(guestAlanAsk) {
		t.Error("a refused drop removed audio")
	}
}

// The living room's Satellite1 heard Alan's sister say "Hey Eddie" that
// evening, kept both channels, and stage two held her as an unknown
// speaker.
const (
	guestWakeAudio  = "blob://wake/living_room-guest"
	guestWakeSecond = "blob://wake/living_room-guest-second"
)

// withGuestsWake writes her rejected wake, and its audio, into the living
// room's own log, and returns its seq.
func withGuestsWake(t *testing.T, v *visit) uint64 {
	t.Helper()
	writeBlob(t, v.blobs, guestWakeAudio)
	writeBlob(t, v.blobs, guestWakeSecond)
	j := journal.New(v.store, journal.FixedClock(household.Day().Add(21*time.Hour+20*time.Minute)), household.Versions())
	e, err := j.Append(context.Background(), "device:living_room",
		rec(journal.KindWakeRejected, guestWakeAudio, "reason", "unknown_speaker", "wake_word", "hey_eddie", "second_audio_ref", guestWakeSecond))
	if err != nil {
		t.Fatal(err)
	}
	return e.Seq
}

// Alan asks for his sister's voice to go from the living room's own log
// too. Her rejected wake's row offers the drop behind the same second
// press as a conversation's clip, and nothing offers to delete the log;
// the press takes both channels, records them as redacted in that log,
// and leaves the wake and its verdict where they were, now saying the
// audio is gone. Deletions counts it like any drop (ADR-0070).
//
// verifies SPEC §8, §9.3
func TestDroppingTheGuestsWakeFromTheLivingRoomsLog(t *testing.T) {
	v := newVisit(t)
	seq := withGuestsWake(t, v)
	const room = "device:living_room"
	page := get(t, v.s, conversationHref(room))
	for _, want := range []string{
		`action="` + dropAudioHref(room, seq) + `"`,
		"Removes this clip and its second channel for good",
		"Its line in the log stays",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the living room's log is missing %q", want)
		}
	}
	if strings.Contains(page, `id="delete-conversation"`) {
		t.Error("the living room's own log offers to delete it whole")
	}

	w := send(v.s.routes(), dropAudioHref(room, seq), confirm("drop"))
	if w.Code != http.StatusOK || w.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("drop = %d %v: %s", w.Code, w.Header(), w.Body.String())
	}
	if want := []string{"remove " + guestWakeAudio, "remove " + guestWakeSecond}; !slices.Equal(v.tr.seen(), want) {
		t.Errorf("dropped by %v, want %v", v.tr.seen(), want)
	}
	if v.kept(guestWakeAudio) || v.kept(guestWakeSecond) {
		t.Error("her wake is still kept")
	}
	events := v.log(t, room)
	var reasons []string
	for _, e := range events {
		if e.Kind == journal.KindAudioDropped {
			reasons = append(reasons, e.Fields["audio_ref"]+" "+e.Fields["reason"])
		}
	}
	if want := []string{guestWakeAudio + " redacted", guestWakeSecond + " redacted"}; !slices.Equal(reasons, want) {
		t.Errorf("the log records %v, want %v", reasons, want)
	}
	if events[seq-1].Kind != journal.KindWakeRejected {
		t.Errorf("#%d is now %s, want her wake still there", seq, events[seq-1].Kind)
	}

	page = get(t, v.s, conversationHref(room))
	row := rowOf(page, int(seq))
	if !strings.Contains(row, "audio redacted") || strings.Contains(row, "<audio") || strings.Contains(row, dropAudioHref(room, seq)) {
		t.Errorf("her wake's row still plays or offers the drop, or does not say the audio went:\n%s", row)
	}
	if !strings.Contains(page, "Held: only the voice check failed") {
		t.Error("her wake lost its verdict with its audio")
	}
	records, _ := v.s.erasures.List(context.Background())
	if len(records) != 1 || records[0].Kind != erasure.KindAudio || records[0].ConversationID != room || records[0].Subject != guestWakeAudio+" "+guestWakeSecond {
		t.Errorf("records = %+v, want her wake's two clips from %s", records, room)
	}
	if trail := get(t, v.s, deletionsRoute); !strings.Contains(trail, room+" · "+guestWakeAudio) || !strings.Contains(trail, fmt.Sprintf("2 clips of #%d", seq)) {
		t.Error("Deletions does not list the drop from the living room's log")
	}
}

// verifies SPEC §9.2
func TestNoDeletionScreenCarriesAnInlineScript(t *testing.T) {
	v := newVisit(t)
	h := v.s.handler(defaultHosts(defaultAddr))
	for _, target := range []string{conversationHref(convGuest), memoryRoute, deletionsRoute} {
		w := getAs(h, target)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, w.Code)
		}
		body := w.Body.String()
		for _, tag := range strings.Split(body, "<script")[1:] {
			if !strings.HasPrefix(tag, ` src="/static/`) {
				t.Errorf("GET %s carries a script of its own: <script%.60s", target, tag)
			}
		}
		for _, inline := range []string{" onclick=", " onsubmit=", " hx-on", "javascript:", "confirm(", "<style"} {
			if strings.Contains(body, inline) {
				t.Errorf("GET %s carries %q, which the policy refuses", target, inline)
			}
		}
	}
}
