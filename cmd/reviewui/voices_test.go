package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/identity"
)

// voiceDim is small so a test can choose a voice by hand.
const voiceDim = 4

// fakeVoices maps each take to the vector a test chose for it, so enrolling
// runs without a sidecar; a take it was not told of is a sidecar that is
// down. The clip picker embeds its rows in parallel, as the real sidecar
// client allows, so Embed takes mu.
type fakeVoices struct {
	mu    sync.Mutex
	by    map[string][]float32
	calls int

	// anyTake, when set, is the voice of every take it was not told of:
	// a browser's recording, whose bytes no test can know ahead.
	anyTake []float32
	heard   [][]byte
}

func (f *fakeVoices) Embed(_ context.Context, pcm []byte) ([]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.heard = append(f.heard, pcm)
	v, ok := f.by[string(pcm)]
	if !ok && f.anyTake != nil {
		return f.anyTake, nil
	}
	if !ok {
		return nil, errors.New("sidecar unreachable")
	}
	return v, nil
}

func (f *fakeVoices) Model() string { return "fake-titanet" }
func (f *fakeVoices) Dim() int      { return voiceDim }

// take is n samples of distinct PCM, the voice v as far as the fake hears.
func (f *fakeVoices) take(seed int, v []float32) []byte {
	pcm := make([]byte, 2*bridge.SampleRate/10)
	for i := range len(pcm) / 2 {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(seed*31+i)))
	}
	f.by[string(pcm)] = v
	return pcm
}

// wavOf lays pcm into a canonical WAV at the given rate.
func wavOf(pcm []byte, rate uint32) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	for _, field := range []any{uint32(16), uint16(1), uint16(1), rate, rate * 2, uint16(2), uint16(16)} {
		_ = binary.Write(&b, binary.LittleEndian, field)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

// enrollPost is the form as the browser sends it: id, name and the takes.
func enrollPost(t *testing.T, h http.Handler, id, name string, takes map[string][]byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("id", id)
	_ = mw.WriteField("name", name)
	for fname, b := range takes {
		fw, err := mw.CreateFormFile("take", fname)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(b)
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, voicesRoute, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type voicesVisit struct {
	s    *server
	emb  *fakeVoices
	path string
}

// newVoicesVisit serves the household's day with identities.yaml in a
// temporary directory and the fake sidecar to enroll through.
func newVoicesVisit(t *testing.T) *voicesVisit {
	t.Helper()
	s, _ := newHouseholdServer(t)
	v := &voicesVisit{s: s, emb: &fakeVoices{by: map[string][]float32{}}, path: filepath.Join(t.TempDir(), identity.DefaultPath)}
	s.voices = voices{identities: v.path, embedder: func() (identity.Embedder, error) { return v.emb, nil }}
	return v
}

// threeTakes is one person's phrases, each a little off the voice v.
func (v *voicesVisit) threeTakes(seed int, voice []float32) map[string][]byte {
	takes := map[string][]byte{}
	for i := range 3 {
		vec := append([]float32(nil), voice...)
		vec[(i+1)%voiceDim] += 0.05
		takes[fmt.Sprintf("take-%d.wav", i+1)] = wavOf(v.emb.take(seed*10+i, vec), bridge.SampleRate)
	}
	return takes
}

func (v *voicesVisit) household(t *testing.T) *identity.Identities {
	t.Helper()
	ids, err := identity.Load(v.path)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// Alice reads three phrases into the Voices screen. She is embedded over
// the sidecar, saved to identities.yaml as a voiceprint chorusd can match,
// and the screen says she is enrolled and that chorusd must restart to
// hear it. The vector never reaches the page.
//
// verifies SPEC §5
func TestEnrollingAliceFromTheVoicesScreen(t *testing.T) {
	v := newVoicesVisit(t)
	if !strings.Contains(get(t, v.s, "/conversations"), `href="`+voicesRoute+`"`) {
		t.Error("Browse does not link to the Voices screen")
	}
	page := get(t, v.s, voicesRoute)
	for _, want := range []string{"Nobody is enrolled.", `enctype="multipart/form-data"`, `name="take"`, "Add oat milk", `<script src="/static/js/enroll.js" defer></script>`} {
		if !strings.Contains(page, want) {
			t.Errorf("the empty Voices screen is missing %q", want)
		}
	}

	w := enrollPost(t, v.s.routes(), "alice", "Alice", v.threeTakes(1, []float32{1, 0, 0, 0}))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != voicesRoute+"?enrolled=alice" {
		t.Fatalf("enroll = %d to %q: %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	ids := v.household(t)
	if ids.Model != "fake-titanet" || ids.Dim != voiceDim || len(ids.People) != 1 || ids.People[0].ID != "alice" || ids.People[0].Name != "Alice" || ids.People[0].Utterances != 3 {
		t.Fatalf("identities.yaml holds %+v", ids)
	}
	if out, err := ids.Match([]float32{1, 0, 0, 0}, identity.Thresholds{}); err != nil || out.PersonID != "alice" {
		t.Errorf("Alice's own voice matches %+v, %v", out, err)
	}
	if info, err := os.Stat(v.path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("identities.yaml is %v, %v; a voiceprint is owner-readable only", info, err)
	}

	page = get(t, v.s, voicesRoute+"?enrolled=alice")
	for _, want := range []string{"Enrolled alice.", "Restart chorusd", `id="voice-alice"`, "alice · Alice", "voiceprint from 3 phrases", "Enrolled · 1 · fake-titanet", `action="` + voiceRemoveHref("alice") + `"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the Voices screen after enrolling is missing %q", want)
		}
	}
	if strings.Contains(page, "centroid") || strings.Contains(page, "0.99") {
		t.Error("the Voices screen shows the voiceprint itself")
	}
}

// What the screen refuses, each said on the page with the form kept, and
// none of it costing a sidecar call or writing the file: too few takes, a
// take in another format, an id that is not one word, someone already
// enrolled, and no sidecar to embed through.
//
// verifies SPEC §5
func TestTheVoicesScreenRefusesAnEnrollmentItCannotTrust(t *testing.T) {
	v := newVoicesVisit(t)
	if w := enrollPost(t, v.s.routes(), "teagan", "", v.threeTakes(2, []float32{0, 1, 0, 0})); w.Code != http.StatusSeeOther {
		t.Fatalf("enroll teagan = %d: %s", w.Code, w.Body.String())
	}
	before, err := os.ReadFile(v.path)
	if err != nil {
		t.Fatal(err)
	}
	v.emb.calls = 0

	two := v.threeTakes(3, []float32{0, 0, 1, 0})
	delete(two, "take-3.wav")
	resampled := v.threeTakes(4, []float32{0, 0, 1, 0})
	resampled["take-2.wav"] = wavOf(v.emb.take(99, []float32{0, 0, 1, 0}), 48000)

	for _, c := range []struct {
		name, id string
		takes    map[string][]byte
		status   int
		says     string
	}{
		{"two takes", "alan", two, http.StatusBadRequest, "2 takes, want at least 3"},
		{"a 48 kHz take", "alan", resampled, http.StatusBadRequest, "take-2.wav is PCM, 1 ch, 48000 Hz, 16 bit"},
		{"an id with a space", "alan smith", v.threeTakes(5, []float32{0, 0, 1, 0}), http.StatusBadRequest, "is not an id"},
		{"teagan again", "teagan", v.threeTakes(6, []float32{0, 0, 1, 0}), http.StatusConflict, "teagan is already enrolled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := enrollPost(t, v.s.routes(), c.id, "Alan", c.takes)
			body := w.Body.String()
			if w.Code != c.status || !strings.Contains(body, "Nobody was enrolled.") || !strings.Contains(body, c.says) {
				t.Errorf("= %d, want %d saying %q:\n%s", w.Code, c.status, c.says, body)
			}
			if !strings.Contains(body, `value="Alan"`) {
				t.Error("the refusal lost the name the reviewer typed")
			}
		})
	}
	if v.emb.calls != 0 {
		t.Errorf("refusals cost %d sidecar calls", v.emb.calls)
	}
	if after, _ := os.ReadFile(v.path); !bytes.Equal(before, after) {
		t.Error("a refused enrollment changed identities.yaml")
	}

	v.s.voices.embedder = nil
	page := get(t, v.s, voicesRoute)
	if !strings.Contains(page, "Enrollment is off.") || !strings.Contains(page, speakerIDURLEnv) || strings.Contains(page, `name="take"`) {
		t.Error("with no sidecar the screen still offers the form, or does not say why not")
	}
	if !strings.Contains(page, `id="voice-teagan"`) {
		t.Error("with no sidecar the screen stopped listing who is enrolled")
	}
	if w := enrollPost(t, v.s.routes(), "alan", "Alan", v.threeTakes(7, []float32{0, 0, 1, 0})); w.Code != http.StatusServiceUnavailable {
		t.Errorf("enroll with no sidecar = %d", w.Code)
	}
}

// Teagan is removed behind the second press: the list swaps without them,
// identities.yaml keeps Alice, and the screen says chorusd must restart.
// A read-only UI offers no Remove and refuses one sent anyway.
//
// verifies SPEC §5
func TestRemovingTeagansVoiceprint(t *testing.T) {
	v := newVoicesVisit(t)
	for i, id := range []string{"teagan", "alice"} {
		voice := make([]float32, voiceDim)
		voice[i] = 1
		if w := enrollPost(t, v.s.routes(), id, "", v.threeTakes(10+i, voice)); w.Code != http.StatusSeeOther {
			t.Fatalf("enroll %s = %d: %s", id, w.Code, w.Body.String())
		}
	}
	h := v.s.routes()
	if w := send(h, voiceRemoveHref("teagan"), nil); w.Code != http.StatusBadRequest {
		t.Errorf("a removal without the second press = %d", w.Code)
	}
	w := send(h, voiceRemoveHref("teagan"), confirm("remove"))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.HasPrefix(body, `<section id="voices"`) || strings.Contains(body, "voice-teagan") ||
		!strings.Contains(body, "voice-alice") || !strings.Contains(body, "Removed teagan. Restart chorusd") {
		t.Errorf("remove = %d:\n%s", w.Code, body)
	}
	if got := v.household(t).Household(); len(got) != 1 || got[0] != "alice" {
		t.Errorf("identities.yaml holds %v, want alice only", got)
	}
	if w := send(h, voiceRemoveHref("teagan"), confirm("remove")); w.Code != http.StatusNotFound {
		t.Errorf("removing teagan twice = %d", w.Code)
	}

	v.s.readOnly = true
	if page := get(t, v.s, voicesRoute); strings.Contains(page, voiceRemoveHref("alice")) || !strings.Contains(page, "removes nobody") {
		t.Error("a read-only Voices screen offers Remove")
	}
	if w := send(v.s.routes(), voiceRemoveHref("alice"), confirm("remove")); w.Code != http.StatusForbidden {
		t.Errorf("a read-only remove = %d", w.Code)
	}
}

// An instance started with no household file says so instead of failing.
//
// verifies SPEC §9.2
func TestVoicesWithNoHouseholdFile(t *testing.T) {
	s, _ := newHouseholdServer(t)
	page := get(t, s, voicesRoute)
	if !strings.Contains(page, "This review UI has no household file.") || !strings.Contains(page, "Enrollment is off.") {
		t.Errorf("the Voices screen without -identities says:\n%s", page)
	}
}
