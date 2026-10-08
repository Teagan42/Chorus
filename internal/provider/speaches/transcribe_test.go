package speaches_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/provider/speaches"
)

// roundTrip serves a canned response in-process and keeps the request that
// asked for it. No listener: hermetic means hermetic (CONTRIBUTING §1).
type roundTrip struct {
	status int
	body   string
	err    error

	calls   int
	req     *http.Request
	reqBody []byte
}

func (rt *roundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.calls++
	rt.req = r
	if r.Body != nil {
		rt.reqBody, _ = io.ReadAll(r.Body)
	}
	if rt.err != nil {
		return nil, rt.err
	}
	status := rt.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(rt.body)),
		Header:     http.Header{"Content-Type": {"application/json"}},
		Request:    r,
	}, nil
}

// transcriber wires a Transcriber to a canned response.
func transcriber(t *testing.T, rt *roundTrip, cfg speaches.Config) *speaches.Transcriber {
	t.Helper()
	cfg.BaseURL = "http://speaches.invalid"
	cfg.HTTP = &http.Client{Transport: rt}
	tr, err := speaches.New(cfg)
	if err != nil {
		t.Fatalf("new transcriber: %v", err)
	}
	return tr
}

// speech is a quarter second of device audio.
func speech() []byte {
	out := make([]byte, 2*4000)
	for i := range 4000 {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(i%1000)))
	}
	return out
}

// form decodes the multipart request the fake saw: every field by name, and
// the file part's bytes and declared media type.
type form struct {
	fields   map[string]string
	file     []byte
	fileType string
}

func parse(t *testing.T, rt *roundTrip) form {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(rt.req.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("content type is %q: %v", rt.req.Header.Get("Content-Type"), err)
	}
	f := form{fields: map[string]string{}}
	mr := multipart.NewReader(strings.NewReader(string(rt.reqBody)), params["boundary"])
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return f
		}
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		b, _ := io.ReadAll(p)
		if p.FormName() == "file" {
			f.file, f.fileType = b, p.Header.Get("Content-Type")
			continue
		}
		f.fields[p.FormName()] = string(b)
	}
}

// The request is the de facto self-hosted STT contract: a multipart upload
// naming the model, with the audio as a WAV the endpoint can read the rate
// off. Every field here was chosen on purpose (see transcribe.go).
//
// verifies SPEC §10
func TestItUploadsTheUtteranceAsAWav(t *testing.T) {
	rt := &roundTrip{body: `{"text":"turn off the kitchen lights"}`}
	tr := transcriber(t, rt, speaches.Config{})
	pcm := speech()
	if _, err := tr.Transcribe(context.Background(), pcm); err != nil {
		t.Fatalf("transcribe: %v", err)
	}

	if rt.req.Method != http.MethodPost || rt.req.URL.Path != "/v1/audio/transcriptions" {
		t.Errorf("sent %s %s", rt.req.Method, rt.req.URL.Path)
	}
	f := parse(t, rt)
	switch {
	case f.fields["model"] != speaches.DefaultModel:
		t.Errorf("model is %q, want the pinned default", f.fields["model"])
	case f.fields["response_format"] != "json":
		t.Errorf("response_format is %q, want json", f.fields["response_format"])
	case f.fileType != "audio/wav":
		t.Errorf("file part is %q, want audio/wav", f.fileType)
	}
	if _, sent := f.fields["language"]; sent {
		t.Error("language was sent without being configured")
	}

	if len(f.file) != 44+len(pcm) {
		t.Fatalf("file is %d bytes, want a 44-byte header on %d", len(f.file), len(pcm))
	}
	rate := binary.LittleEndian.Uint32(f.file[24:28])
	channels := binary.LittleEndian.Uint16(f.file[22:24])
	bits := binary.LittleEndian.Uint16(f.file[34:36])
	if rate != bridge.SampleRate || channels != 1 || bits != bridge.BitsPerSample {
		t.Errorf("header declares %d Hz %d channel %d-bit, want the device's format", rate, channels, bits)
	}
	if string(f.file[44:]) != string(pcm) {
		t.Error("the samples were altered on the way into the container")
	}
}

func TestLanguageIsSentOnlyWhenConfigured(t *testing.T) {
	rt := &roundTrip{body: `{"text":"hello"}`}
	tr := transcriber(t, rt, speaches.Config{Language: "en"})
	if _, err := tr.Transcribe(context.Background(), speech()); err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if got := parse(t, rt).fields["language"]; got != "en" {
		t.Errorf("language is %q, want en", got)
	}
}

// The whole point of the seam: the endpoint's json becomes a Result.
//
// verifies SPEC §10
func TestItReturnsWhatWasHeard(t *testing.T) {
	rt := &roundTrip{body: `{"text":"turn off the kitchen lights"}`}
	tr := transcriber(t, rt, speaches.Config{})
	r, err := tr.Transcribe(context.Background(), speech())
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if r.Text != "turn off the kitchen lights" {
		t.Errorf("text = %q", r.Text)
	}
}

// Three different mistakes -- a model not downloaded, one the server cannot
// serve, audio it could not decode -- are all non-2xx that only the body
// tells apart. Quoted, but bounded: a stack trace is not an error message.
func TestItQuotesWhatTheEndpointComplainedAbout(t *testing.T) {
	rt := &roundTrip{
		status: http.StatusNotFound,
		body:   `{"detail":"Model 'nope' is not supported. If you think this is a mistake, please open an issue."}` + strings.Repeat(" ", 4096) + "TAIL",
	}
	tr := transcriber(t, rt, speaches.Config{Model: "nope"})
	_, err := tr.Transcribe(context.Background(), speech())
	if err == nil {
		t.Fatal("a 404 must fail the transcription")
	}
	if !strings.Contains(err.Error(), "Model 'nope' is not supported") {
		t.Errorf("error does not name the cause: %v", err)
	}
	if strings.Contains(err.Error(), "TAIL") {
		t.Errorf("error quotes an unbounded body: %d bytes", len(err.Error()))
	}
}

// Silence is not a request: the endpoint answers 400 to an empty file, and an
// utterance that closed before any audio arrived is ordinary.
func TestEmptyAudioMakesNoRequest(t *testing.T) {
	rt := &roundTrip{body: `{"text":"ghost"}`}
	tr := transcriber(t, rt, speaches.Config{})
	r, err := tr.Transcribe(context.Background(), nil)
	if err != nil || r.Text != "" {
		t.Errorf("transcribe nothing = %+v, %v", r, err)
	}
	if rt.calls != 0 {
		t.Errorf("empty audio reached the endpoint %d time(s)", rt.calls)
	}
}

// A torn sample is noise if it is sent: every sample after the odd byte is a
// byte out of phase.
func TestATornSampleIsRefusedRatherThanSent(t *testing.T) {
	rt := &roundTrip{body: `{"text":"noise"}`}
	tr := transcriber(t, rt, speaches.Config{})
	if _, err := tr.Transcribe(context.Background(), speech()[:1199]); err == nil {
		t.Fatal("an odd byte count must fail")
	}
	if rt.calls != 0 {
		t.Error("torn audio reached the endpoint")
	}
}

// The utterance cancels a partial decode when it finishes, and the session
// cancels everything when it ends; either must abandon the request.
//
// verifies SPEC §4
func TestACancelledContextAbandonsTheRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	hung := &hangingTransport{cancel: cancel}
	tr, err := speaches.New(speaches.Config{BaseURL: "http://speaches.invalid", HTTP: &http.Client{Transport: hung}})
	if err != nil {
		t.Fatalf("new transcriber: %v", err)
	}
	if _, err := tr.Transcribe(ctx, speech()); !errors.Is(err, context.Canceled) {
		t.Errorf("error is %v, want context.Canceled", err)
	}
}

// hangingTransport cancels the caller's context on arrival and then waits for
// it, the way a sidecar mid-decode would when the session ends.
type hangingTransport struct{ cancel context.CancelFunc }

func (h *hangingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	h.cancel()
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func TestADeadEndpointFailsTheTranscription(t *testing.T) {
	rt := &roundTrip{err: errors.New("dial tcp: connection refused")}
	tr := transcriber(t, rt, speaches.Config{})
	if _, err := tr.Transcribe(context.Background(), speech()); err == nil {
		t.Fatal("a dial failure must fail the transcription")
	}
}

func TestAMalformedReplyFailsTheTranscription(t *testing.T) {
	rt := &roundTrip{body: `turn off the lights`}
	tr := transcriber(t, rt, speaches.Config{})
	if _, err := tr.Transcribe(context.Background(), speech()); err == nil {
		t.Fatal("a reply that is not json must fail rather than read as silence")
	}
}

func TestNewRequiresAnEndpoint(t *testing.T) {
	if _, err := speaches.New(speaches.Config{}); err == nil {
		t.Fatal("a transcriber with no base url must not build")
	}
}

// The journal stamps this on every event, so an utterance_transcribed can
// say which recogniser produced it (SPEC §8). The model id is already a
// stable name, so unlike the prompt it needs no fingerprint; it is spelled
// out here rather than read back from the sidecar, so an upgrade cannot
// move it.
//
// verifies SPEC §8
func TestVersionNamesTheModel(t *testing.T) {
	cases := map[string]struct {
		cfg  speaches.Config
		want string
	}{
		"default":            {speaches.Config{}, speaches.DefaultModel},
		"a configured model": {speaches.Config{Model: "Systran/faster-whisper-small"}, "Systran/faster-whisper-small"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tr := transcriber(t, &roundTrip{}, tc.cfg)
			if got := tr.Version(); got != tc.want {
				t.Errorf("version = %q, want %q", got, tc.want)
			}
		})
	}
}
