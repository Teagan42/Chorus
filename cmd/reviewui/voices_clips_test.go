package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/household"
	"github.com/teagan42/chorus/internal/stt"
)

// spokenClip is seconds of audio with a tone in the middle, as a satellite's
// recording is: a room, something said, a room again.
func spokenClip(seconds, voiced float64, seed int) []byte {
	samples := int(seconds * bridge.SampleRate)
	pcm := make([]byte, 2*samples)
	from := (samples - int(voiced*bridge.SampleRate)) / 2
	for i := from; i < from+int(voiced*bridge.SampleRate) && i < samples; i++ {
		s := 8000 * math.Sin(2*math.Pi*float64(120+seed*10)*float64(i)/bridge.SampleRate)
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(s)))
	}
	return pcm
}

// keep writes one clip into the store and logs it against the satellite, as
// a refused wake does.
func (v *voicesVisit) keep(t *testing.T, satellite, key string, pcm []byte) string {
	t.Helper()
	return v.keepReason(t, satellite, key, pcm, "unknown_speaker")
}

// keepReason writes one clip with the rejection reason the log would carry.
func (v *voicesVisit) keepReason(t *testing.T, satellite, key string, pcm []byte, reason string) string {
	t.Helper()
	ctx := context.Background()
	w, err := v.s.blobs.Create(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(pcm); err != nil {
		t.Fatal(err)
	}
	ref, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}
	conv := devicePrefix + satellite
	last, err := v.s.journal.LastSeq(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	err = v.s.journal.Append(ctx, journal.Event{
		ConversationID: conv, Seq: last + 1, Kind: journal.KindWakeRejected, Actor: "device",
		At: household.ReviewedAt(), AudioRef: ref, Fields: map[string]string{"reason": reason},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// enrollClips is the form as the screen sends it when the takes are ticked
// from a satellite's own recordings.
func enrollClips(t *testing.T, h http.Handler, id, satellite string, refs []string) *httptest.ResponseRecorder {
	t.Helper()
	// The enrollment form is multipart because it carries file takes; a
	// ticked clip rides it as an ordinary field.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("id", id)
	_ = mw.WriteField("satellite", satellite)
	for _, ref := range refs {
		_ = mw.WriteField("clip", ref)
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, voicesRoute, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Teagan is enrolled from what the kitchen satellite itself recorded, which
// is the audio its own front end produced: a voiceprint from it matches that
// satellite more surely than one from a laptop's microphone (ADR-0082).
//
// verifies SPEC §5
func TestEnrollingFromASatellitesOwnClips(t *testing.T) {
	v := newVoicesVisit(t)
	v.emb.anyTake = []float32{0.4, 0.3, 0.2, 0.1}
	var refs []string
	for i := range identity.MinUtterances {
		refs = append(refs, v.keep(t, "kitchen", fmt.Sprintf("wake/own-%d", i), spokenClip(30, 4, i)))
	}

	w := enrollClips(t, v.s.routes(), "teagan", "kitchen", refs)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("enrolling from clips answered %d, want %d: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	ids := v.household(t)
	if len(ids.People) != 1 || ids.People[0].ID != "teagan" {
		t.Fatalf("household is %+v, want teagan enrolled", ids.People)
	}
	if got := ids.People[0].Utterances; got != identity.MinUtterances {
		t.Errorf("voiceprint from %d utterances, want %d", got, identity.MinUtterances)
	}
	// Each take is trimmed to what was said: a 30 s clip holding 4 s of
	// speech must not reach the sidecar as 30 s of mostly room.
	for i, heard := range v.emb.heard {
		if secs := float64(len(heard)/2) / bridge.SampleRate; secs > 5 {
			t.Errorf("take %d reached the sidecar as %.1f s, want the voiced span", i, secs)
		}
	}
}

// A clip the screen never offered is refused, so no other blob the store
// holds -- the assistant's own speech among them -- can become a voiceprint.
//
// verifies SPEC §5
func TestEnrollingRefusesAClipTheSatelliteDidNotKeep(t *testing.T) {
	v := newVoicesVisit(t)
	v.emb.anyTake = []float32{0.4, 0.3, 0.2, 0.1}
	good := v.keep(t, "kitchen", "wake/kitchen-own", spokenClip(30, 4, 1))
	// Committed to the store, but logged against no satellite.
	w, err := v.s.blobs.Create(context.Background(), "tts/the-assistant")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(spokenClip(30, 4, 9))
	stranger, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}

	rec := enrollClips(t, v.s.routes(), "teagan", "kitchen", []string{good, good, stranger})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("answered %d, want %d: a blob off the satellite's log is not a take", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "not one of that satellite&#39;s clips") {
		t.Errorf("the refusal does not say which clip was not offered: %s", rec.Body.String())
	}
	if _, err := identity.Load(v.path); err == nil {
		t.Error("a refused enrollment wrote a household file")
	}
	if v.emb.calls != 0 {
		t.Errorf("the sidecar was asked %d times; nothing is embedded until every take is read", v.emb.calls)
	}
}

// The picker lists the satellite asked for, and falls back to one that
// exists rather than naming a log nobody has.
//
// verifies SPEC §9.2
func TestTheClipPickerListsOneSatellitesRecordings(t *testing.T) {
	v := newVoicesVisit(t)
	v.keep(t, "kitchen", "wake/kitchen-pick", spokenClip(30, 4, 1))

	r := httptest.NewRequest(http.MethodGet, clipsRoute+"?satellite=kitchen", nil)
	w := httptest.NewRecorder()
	v.s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "wake/kitchen-pick") {
		t.Errorf("the kitchen's clip is not offered: %s", body)
	}
	if !strings.Contains(body, "unknown_speaker") {
		t.Errorf("the picker does not say what the log knows was heard: %s", body)
	}

	// An unknown satellite is not a log to read: the picker shows a real one.
	r = httptest.NewRequest(http.MethodGet, clipsRoute+"?satellite=../etc", nil)
	w = httptest.NewRecorder()
	v.s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "../etc") {
		t.Errorf("answered %d and offered an unknown satellite: %s", w.Code, w.Body.String())
	}
}

// A clip with too little speech in it is left out of the list, not offered
// and then refused: the list is short and unpaged, so a row nobody can tick
// costs a row that someone could (ADR-0082).
//
// verifies SPEC §9.2
func TestTheClipPickerLeavesOutClipsTooShortToEmbed(t *testing.T) {
	v := newVoicesVisit(t)
	brief := v.keep(t, "office", "wake/too-brief", spokenClip(6, 1, 1))
	ample := v.keep(t, "office", "wake/long-enough", spokenClip(10, 6, 2))

	r := httptest.NewRequest(http.MethodGet, clipsRoute+"?satellite=office", nil)
	w := httptest.NewRecorder()
	v.s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, brief) {
		t.Errorf("a clip too short to embed is still offered: %s", body)
	}
	if !strings.Contains(body, ample) {
		t.Errorf("the usable clip is not offered: %s", body)
	}
	// Every row says how much was spoken, so a reviewer can prefer a longer.
	if !strings.Contains(body, "s spoken") {
		t.Errorf("rows do not say how much was spoken: %s", body)
	}
}

// With nothing long enough kept, the screen says what a take needs rather
// than listing an empty table.
//
// verifies SPEC §9.2
func TestTheClipPickerSaysWhenNothingIsLongEnough(t *testing.T) {
	v := newVoicesVisit(t)
	v.keep(t, "office", "wake/brief-only", spokenClip(6, 1, 1))

	r := httptest.NewRequest(http.MethodGet, clipsRoute+"?satellite=office", nil)
	w := httptest.NewRecorder()
	v.s.routes().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "long enough to embed") {
		t.Errorf("the picker does not say what a take needs: %s", w.Body.String())
	}
}

// readClip keeps its own length check, so the list and the check cannot
// drift apart silently: a ref that reached it anyway is still refused.
//
// verifies SPEC §5
func TestReadClipRefusesAShortClipEvenWhenOffered(t *testing.T) {
	v := newVoicesVisit(t)
	brief := v.keep(t, "office", "wake/forced", spokenClip(6, 1, 1))
	// Hand it the row the list would never build.
	offered := []clipRow{{Ref: brief}}
	if _, err := v.s.readClip(context.Background(), offered, brief); err == nil {
		t.Error("readClip accepted a clip with too little speech")
	} else if !strings.Contains(err.Error(), "of speech, and a take needs") {
		t.Errorf("the refusal does not say it is too short: %v", err)
	}
}

// A clip the ear found no words in is not a take, however long it is: under
// a mis-set speech gate a room arrives as half a minute of amplified silence,
// and such clips cluster with each other rather than with the household's
// own speech, so one ticked as a take poisons the voiceprint (ADR-0082).
//
// verifies SPEC §9.2
func TestTheClipPickerLeavesOutClipsWithNoSpeechInThem(t *testing.T) {
	v := newVoicesVisit(t)
	ctx := context.Background()
	// Both are long. One is a room the ear found nothing in.
	room := v.keepReason(t, "office", "wake/just-a-room", spokenClip(20, 18, 3), "no_speech")
	spoken := v.keep(t, "office", "wake/a-sentence", spokenClip(10, 6, 4))

	rows, err := v.s.deviceClips(ctx, "office")
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, r := range rows {
		refs = append(refs, r.Ref)
	}
	if slices.Contains(refs, room) {
		t.Errorf("a clip with no speech in it is offered as a take: %v", refs)
	}
	if !slices.Contains(refs, spoken) {
		t.Errorf("the spoken clip is not offered: %v", refs)
	}
}

// fakeEar reads the same words from every clip, so a test can see them reach
// the row without an endpoint.
type fakeEar struct{ text string }

func (e fakeEar) Transcribe(context.Context, []byte) (stt.Result, error) {
	return stt.Result{Text: e.text}, nil
}

// Each row says whose enrolled voice the clip sounds nearest and what the ear
// read in it. Both are what a reviewer needs to not tick somebody else's
// clip, which is the mistake the screen made easy before (ADR-0082).
//
// verifies SPEC §9.2
func TestTheClipPickerSaysWhoItSoundsLikeAndWhatWasSaid(t *testing.T) {
	v := newVoicesVisit(t)
	v.emb.anyTake = []float32{1, 0, 0, 0}
	v.s.voices.words = func() (stt.Transcriber, error) { return fakeEar{text: "open the garage door"}, nil }

	// Enroll someone the clip will sound exactly like.
	takes := v.threeTakes(4, []float32{1, 0, 0, 0})
	if w := enrollPost(t, v.s.routes(), "alan", "Alan", takes); w.Code != http.StatusSeeOther {
		t.Fatalf("enrolling alan answered %d: %s", w.Code, w.Body.String())
	}
	v.keep(t, "office", "wake/whose-voice", spokenClip(10, 6, 1))

	rows, err := v.s.deviceClips(context.Background(), "office")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("offered %d rows, want 1", len(rows))
	}
	if !strings.Contains(rows[0].Closest, "sounds like alan") {
		t.Errorf("the row does not name whose voice it sounds like: %q", rows[0].Closest)
	}
	if rows[0].Said != "open the garage door" {
		t.Errorf("the row does not carry what was said: %q", rows[0].Said)
	}
}

// With no ear and nobody enrolled the row still lists: neither detail is
// required to offer a clip, so a household without them is not stuck.
//
// verifies SPEC §9.2
func TestTheClipPickerListsWithoutAnEarOrAHousehold(t *testing.T) {
	v := newVoicesVisit(t)
	v.keep(t, "office", "wake/bare", spokenClip(10, 6, 1))

	rows, err := v.s.deviceClips(context.Background(), "office")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("offered %d rows, want 1", len(rows))
	}
	if rows[0].Closest != "" || rows[0].Said != "" {
		t.Errorf("details were invented with no ear and nobody enrolled: %+v", rows[0])
	}
	if rows[0].Voiced == "" {
		t.Error("the row lost its spoken length")
	}
}
