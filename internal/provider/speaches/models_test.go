//go:build models

// Model tier: needs a reachable speaches with the Parakeet model downloaded.
// Run with `task test:models`, pointing it at one:
//
//	go test -tags=models ./internal/provider/speaches/ -stt-url http://127.0.0.1:8000
//
// The endpoint is a flag with no default so no household address lives in the
// repo. Without it these skip.
//
// What is tested here is the half the hermetic tier cannot reach: that the
// sidecar still accepts the upload this package builds and answers in the
// shape it decodes, and what a decode costs against SPEC §11. The audio is
// synthetic -- a household recording never enters the repo (CONTRIBUTING §7)
// -- so nothing asserts on the words, only on shape and latency.
package speaches_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/provider/speaches"
)

var (
	endpoint = flag.String("stt-url", "", "speaches base URL; skips when empty")
	model    = flag.String("stt-model", speaches.DefaultModel, "model to exercise")
)

// decodeBudget is generous on purpose: the first request after an idle period
// reloads the model, and the CPU image's decode rate is not yet measured.
const decodeBudget = 2 * time.Minute

func transcriberAt(t *testing.T) *speaches.Transcriber {
	t.Helper()
	if *endpoint == "" {
		t.Skip("no -stt-url")
	}
	tr, err := speaches.New(speaches.Config{BaseURL: *endpoint, Model: *model})
	if err != nil {
		t.Fatalf("new transcriber: %v", err)
	}
	return tr
}

// noise is d of low-level white noise at the device's rate, seeded so every
// run uploads the same bytes. Not speech, so the model hears near-silence;
// the point is a decode of realistic length, not a transcript.
func noise(d time.Duration) []byte {
	frames := int(d * bridge.SampleRate / time.Second)
	out := make([]byte, 2*frames)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range frames {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(r.IntN(2048)-1024)))
	}
	return out
}

// decode runs one utterance and logs what it cost.
func decode(t *testing.T, tr *speaches.Transcriber, pcm []byte) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), decodeBudget)
	defer cancel()

	start := time.Now()
	r, err := tr.Transcribe(ctx, pcm)
	if err != nil {
		t.Fatalf("transcribe %v: %v", duration(pcm), err)
	}
	// Logged every run: the utterance stream re-decodes a growing buffer every
	// half second, so this number is what bounds the partial cadence and the
	// final's share of SPEC §11's ~700 ms, and a regression here is invisible
	// in a pass/fail.
	t.Logf("%v of audio -> %q in %v", duration(pcm), r.Text, time.Since(start).Round(time.Millisecond))
	return r.Text
}

func duration(pcm []byte) time.Duration {
	frames := len(pcm) / (bridge.BitsPerSample / 8)
	return time.Duration(frames) * time.Second / bridge.SampleRate
}

// An utterance of noise must decode without error, whatever it decodes to.
// Nothing is asserted on the words: the audio is not speech and the model is
// free to hear nothing or garbage in it, but it must not refuse the upload.
func TestARealEndpointDecodesAnUtterance(t *testing.T) {
	decode(t, transcriberAt(t), noise(2*time.Second))
}

// The costs the utterance stream's cadence is tuned against: a half-second
// partial, a few seconds, and the bound. Each is a whole decode of a growing
// buffer, which is what makes this quadratic and worth watching.
func TestWhatAGrowingBufferCosts(t *testing.T) {
	tr := transcriberAt(t)
	for _, d := range []time.Duration{500 * time.Millisecond, 3 * time.Second, 10 * time.Second} {
		decode(t, tr, noise(d))
	}
}

// The assumption Transcribe is written against, checked rather than trusted:
// `response_format=json` answers an object with a `text` string. A server
// upgrade that answered verbose json by default, or renamed the field, would
// otherwise decode as an empty utterance with no error anywhere.
//
// verifies SPEC §10
func TestTheEndpointStillAnswersJsonWithText(t *testing.T) {
	if *endpoint == "" {
		t.Skip("no -stt-url")
	}
	ctx, cancel := context.WithTimeout(context.Background(), decodeBudget)
	defer cancel()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "utterance.wav")
	if err != nil {
		t.Fatal(err)
	}
	// Built by hand so the check is of the server, not of this package's own
	// header writer; a plain 44-byte header on a second of noise.
	pcm := noise(time.Second)
	hdr := make([]byte, 44)
	copy(hdr, "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+len(pcm)))
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)
	binary.LittleEndian.PutUint16(hdr[22:], 1)
	binary.LittleEndian.PutUint32(hdr[24:], bridge.SampleRate)
	binary.LittleEndian.PutUint32(hdr[28:], bridge.SampleRate*2)
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], bridge.BitsPerSample)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(pcm)))
	_, _ = part.Write(hdr)
	_, _ = part.Write(pcm)
	_ = w.WriteField("model", *model)
	_ = w.WriteField("response_format", "json")
	_ = w.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, *endpoint+"/v1/audio/transcriptions", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("transcriptions: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	t.Logf("%s %s: %s", resp.Status, resp.Header.Get("Content-Type"), raw)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the endpoint answered %s", resp.Status)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not a json object: %v", err)
	}
	var text string
	if err := json.Unmarshal(got["text"], &text); err != nil {
		t.Errorf("`text` is not a string: %v", err)
	}
	// Parakeet answers text alone today. Fields appearing here would mean the
	// server started giving more, and stt.Result could grow to carry it.
	for k := range got {
		if k != "text" {
			t.Logf("the endpoint now also answers %q", k)
		}
	}
}

// A model that is not downloaded must fail loudly rather than decode as
// silence: a sidecar brought up without `task stt:up`'s download step would
// otherwise look like an assistant that cannot hear.
func TestAMissingModelFailsTheTranscription(t *testing.T) {
	if *endpoint == "" {
		t.Skip("no -stt-url")
	}
	tr, err := speaches.New(speaches.Config{BaseURL: *endpoint, Model: "definitely/not-a-model"})
	if err != nil {
		t.Fatalf("new transcriber: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), decodeBudget)
	defer cancel()
	if _, err := tr.Transcribe(ctx, noise(time.Second)); err == nil {
		t.Fatal("an unknown model must fail the transcription")
	} else {
		t.Logf("unknown model: %v", err)
	}
}
