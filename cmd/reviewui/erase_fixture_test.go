package main

import (
	"context"
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
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// The guest's visit: Alan's sister in the living room that Thursday
// evening, after the jazz. Alan wakes it, she asks for a pharmacy, and the
// session closes. Alan later asks for the recording of her to go.
const (
	convGuest      = "conv-2015-living_room"
	guestAlanAsk   = "blob://mic/guest-alan-ask"
	guestGoAhead   = "blob://tts/guest-go-ahead"
	guestAsk       = "blob://mic/guest-ask"
	guestAskSecond = "blob://mic/guest-ask-second"
	guestAnswer    = "blob://tts/guest-answer"

	// guestAskSeq is her ask, the turn whose audio is dropped.
	guestAskSeq = 5
)

// Alice's allergy, which she asked the office to keep and later to forget,
// and the book club fact Teagan shared, which stays.
var (
	alicesAllergy = memory.Memory{
		ID: "m_a11e2961", Person: "alice", Fact: "Allergic to peanuts.", Shareable: true,
		ConversationID: household.ConvGarage, CallID: "call_remember", At: household.Day().Add(9*time.Hour + 31*time.Minute),
	}
	teagansBookClub = memory.Memory{
		ID: "m_3b8f0a21", Person: "teagan", Fact: "Book club meets here on Thursdays at seven.", Shareable: true,
		ConversationID: "conv-week-before-teagan", CallID: "call_remember", At: household.Day().Add(-7 * 24 * time.Hour),
	}
	visitSummary = memory.Summary{
		ConversationID: convGuest, Person: "alan", Text: "Alan's sister asked where a pharmacy was open; Mill Road, until ten.",
		At: household.Day().Add(20*time.Hour + 16*time.Minute),
	}
)

// withGuestVisit writes the visit into the household's journal and its
// audio into the blobs: half a second of silence a clip.
func withGuestVisit(t *testing.T, store journal.Store, blobs blob.Store) {
	t.Helper()
	ctx := context.Background()
	start := household.Day().Add(20*time.Hour + 15*time.Minute)
	lines := []struct {
		after time.Duration
		r     journal.Record
	}{
		{0, rec(journal.KindSessionOpened, "", "satellite", "living_room", "speaker_id", "alan", "resumed", "false")},
		{400 * time.Millisecond, rec(journal.KindUtteranceTranscribed, guestAlanAsk, "text", "my sister has a question", "speaker_id", "alan")},
		{900 * time.Millisecond, rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
		{1500 * time.Millisecond, rec(journal.KindSpeechSpoken, guestGoAhead, "text", "Go ahead.", "frames_played", "8000")},
		{4 * time.Second, rec(journal.KindUtteranceTranscribed, guestAsk, "text", "is there a pharmacy open near here tonight", "speaker_id", "", "second_audio_ref", guestAskSecond)},
		{4500 * time.Millisecond, rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
		{6 * time.Second, rec(journal.KindSpeechSpoken, guestAnswer, "text", "The one on Mill Road is open until ten.", "frames_played", "8000")},
		{20 * time.Second, rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "living_room")},
	}
	for _, l := range lines {
		for _, ref := range []string{l.r.AudioRef, l.r.Fields["second_audio_ref"]} {
			if ref != "" {
				writeBlob(t, blobs, ref)
			}
		}
		j := journal.New(store, journal.FixedClock(start.Add(l.after)), household.Versions())
		if _, err := j.Append(ctx, convGuest, l.r); err != nil {
			t.Fatalf("append %s: %v", l.r.Kind, err)
		}
	}
}

func rec(kind journal.Kind, audio string, fields ...string) journal.Record {
	r := journal.Record{Kind: kind, AudioRef: audio, Fields: map[string]string{}}
	for i := 0; i+1 < len(fields); i += 2 {
		r.Fields[fields[i]] = fields[i+1]
	}
	return r
}

func writeBlob(t *testing.T, blobs blob.Store, ref string) {
	t.Helper()
	key, err := blob.KeyFor(ref)
	if err != nil {
		t.Fatal(err)
	}
	w, err := blobs.Create(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(make([]byte, 16000)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(); err != nil {
		t.Fatal(err)
	}
}

// trail is every deleting call the doubles saw, in order.
type trail struct {
	mu    sync.Mutex
	calls []string
}

func (t *trail) add(call string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, call)
}

func (t *trail) seen() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.calls...)
}

// The doubles: the real in-memory stores, each telling the trail what it
// was asked to delete, so a test can see which path a deletion took.
type (
	trailJournal struct {
		*journal.MemStore
		tr *trail
	}
	trailBlobs struct {
		*blob.Memory
		tr *trail
	}
	trailCuration struct {
		*curation.MemStore
		tr *trail
	}
	trailMemories struct {
		*memory.MemStore
		tr *trail
	}
)

func (j trailJournal) DeleteLog(ctx context.Context, id string) error {
	j.tr.add("delete log " + id)
	return j.MemStore.DeleteLog(ctx, id)
}

func (b trailBlobs) Remove(ctx context.Context, ref string) error {
	b.tr.add("remove " + ref)
	return b.Memory.Remove(ctx, ref)
}

func (c trailCuration) Forget(ctx context.Context, id string) error {
	c.tr.add("forget curation " + id)
	return c.MemStore.Forget(ctx, id)
}

func (m trailMemories) ForgetSummaries(ctx context.Context, id string) error {
	m.tr.add("forget summaries " + id)
	return m.MemStore.ForgetSummaries(ctx, id)
}

// visit is the household's Thursday with the guest's visit in it, over
// doubles that tell the trail what they deleted.
type visit struct {
	s         *server
	store     *journal.MemStore
	blobs     *blob.Memory
	decisions *curation.MemStore
	memories  *memory.MemStore
	tr        *trail
}

// newVisit serves the day with the visit, a reviewer's label on the
// guest's turn (she was taken for Alan), Alan's summary of it, and the
// memories Alice and Teagan asked to keep.
func newVisit(t *testing.T) *visit {
	t.Helper()
	ctx := context.Background()
	v := &visit{store: householdJournal(t), blobs: householdBlobs(t), decisions: curation.NewMemStore(), memories: memory.NewMemStore(), tr: &trail{}}
	withGuestVisit(t, v.store, v.blobs)
	if err := v.decisions.PutAnnotation(ctx, curation.Annotation{
		ConversationID: convGuest, Seq: guestAskSeq, Labels: []curation.Label{curation.LabelWrongPerson}, AnnotatedAt: household.ReviewedAt(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []memory.Memory{alicesAllergy, teagansBookClub} {
		if err := v.memories.Remember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.memories.Summarized(ctx, visitSummary); err != nil {
		t.Fatal(err)
	}
	v.s = newServer(trailJournal{v.store, v.tr}, trailCuration{v.decisions, v.tr}, trailBlobs{v.blobs, v.tr}, household.ReviewedAt)
	v.s.memories = trailMemories{v.memories, v.tr}
	return v
}

// send is one POST as the page's htmx sends it, through h.
func send(h http.Handler, target string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	r.Host = "127.0.0.1:8080"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func confirm(action string) url.Values { return url.Values{"confirm": {action}} }

// kept reports whether a clip is still in the blob store.
func (v *visit) kept(ref string) bool {
	_, ok := v.blobs.Bytes(ref)
	return ok
}

func (v *visit) log(t *testing.T, id string) []journal.Event {
	t.Helper()
	events, err := v.store.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return events
}
