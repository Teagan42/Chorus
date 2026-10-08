package audio_test

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/teaganglenn/chorus/internal/blob"
	"github.com/teaganglenn/chorus/internal/reviewui/audio"
)

// store writes one second of ramp PCM under mic/1 and returns the store.
func store(t *testing.T) (blob.Store, string, []byte) {
	t.Helper()
	m := blob.NewMemory()
	w, err := m.Create(context.Background(), "mic/1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	pcm := make([]byte, 32000)
	for i := range pcm {
		pcm[i] = byte(i)
	}
	if _, err := w.Write(pcm); err != nil {
		t.Fatalf("write: %v", err)
	}
	ref, err := w.Commit()
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return m, ref, pcm
}

func get(t *testing.T, h http.Handler, ref string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/audio?ref="+url.QueryEscape(ref), nil))
	return w
}

// verifies SPEC §9.2
func TestABlobComesBackAsAPlayableWAV(t *testing.T) {
	s, ref, pcm := store(t)
	w := get(t, audio.Handler(s), ref)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("content type = %q", ct)
	}
	b := w.Body.Bytes()
	if len(b) != 44+len(pcm) {
		t.Fatalf("body = %d bytes, want header + %d", len(b), len(pcm))
	}
	if string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Error("not a RIFF/WAVE container")
	}
	if rate := binary.LittleEndian.Uint32(b[24:28]); rate != 16000 {
		t.Errorf("sample rate = %d, want the device's 16000", rate)
	}
	if bits := binary.LittleEndian.Uint16(b[34:36]); bits != 16 {
		t.Errorf("bits per sample = %d, want 16", bits)
	}
	if n := binary.LittleEndian.Uint32(b[40:44]); int(n) != len(pcm) {
		t.Errorf("data length = %d, want %d", n, len(pcm))
	}
	if string(b[44:48]) != string(pcm[:4]) {
		t.Error("payload does not start with the blob's bytes")
	}
}

// verifies SPEC §9.2
func TestAMissingBlobIs404AndABadRefIs400(t *testing.T) {
	s, _, _ := store(t)
	h := audio.Handler(s)
	if w := get(t, h, "blob://mic/absent"); w.Code != http.StatusNotFound {
		t.Errorf("missing blob = %d, want 404", w.Code)
	}
	if w := get(t, h, "../../etc/passwd"); w.Code != http.StatusBadRequest {
		t.Errorf("bad ref = %d, want 400", w.Code)
	}
}

// verifies SPEC §9.2
func TestDurationMatchesTheDeviceFormat(t *testing.T) {
	// 32000 bytes of 16 kHz s16le mono is exactly one second.
	if ms := audio.DurationMS(32000); ms != 1000 {
		t.Errorf("duration = %dms, want 1000", ms)
	}
}

// verifies SPEC §9.2
func TestFrameBoundsTrimTheServedAudio(t *testing.T) {
	s, ref, pcm := store(t)
	h := audio.Handler(s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/audio?ref="+url.QueryEscape(ref)+"&to=2080", nil))
	if n := len(w.Body.Bytes()); n != 44+2080*2 {
		t.Errorf("to=2080 served %d bytes, want header + 4160", n)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/audio?ref="+url.QueryEscape(ref)+"&from=2080", nil))
	b := w.Body.Bytes()
	if n := len(b); n != 44+len(pcm)-2080*2 {
		t.Errorf("from=2080 served %d bytes, want header + %d", n, len(pcm)-2080*2)
	}
	if string(b[44:48]) != string(pcm[4160:4164]) {
		t.Error("from=2080 does not start at the 2080th frame")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/audio?ref="+url.QueryEscape(ref)+"&to=junk", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad bound = %d, want 400", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/audio?ref="+url.QueryEscape(ref)+"&from=10&to=5", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("inverted range = %d, want 400", w.Code)
	}
}
