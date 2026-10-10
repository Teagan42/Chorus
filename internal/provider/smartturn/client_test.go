package smartturn_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/provider/smartturn"
)

// roundTrip serves a canned reply in-process and keeps the request that asked
// for it. No listener: hermetic means hermetic (CONTRIBUTING §1).
type roundTrip struct {
	status int
	body   string
	err    error

	req     *http.Request
	reqBody []byte
}

func (rt *roundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
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

const finished = `{"complete":true,"probability":0.981,"model":"smart-turn-v3.2-cpu"}`

func clientOn(t *testing.T, rt *roundTrip) *smartturn.Client {
	t.Helper()
	c, err := smartturn.New(smartturn.Config{
		BaseURL: "http://smartturn.invalid/", HTTP: &http.Client{Transport: rt},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

// turn is ms milliseconds of something voiced, as the satellite sends it.
func turn(ms int) []byte {
	out := make([]byte, ms*32)
	for i := range out {
		out[i] = byte(i * 7)
	}
	return out
}

// "Turn off the kitchen lights" and 224 ms of quiet go up as raw PCM,
// labelled as such, at the one path; the verdict comes back whole.
//
// verifies SPEC §4.5
func TestItPostsTheTurnSoFarAsRawPCM(t *testing.T) {
	rt := &roundTrip{body: finished}
	c := clientOn(t, rt)
	audio := turn(1900 + 224)

	v, err := c.Judge(context.Background(), audio)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	switch {
	case rt.req.Method != http.MethodPost:
		t.Errorf("method is %s", rt.req.Method)
	case rt.req.URL.String() != "http://smartturn.invalid/v1/turn":
		t.Errorf("posted to %s", rt.req.URL)
	case rt.req.Header.Get("Content-Type") != smartturn.ContentType:
		t.Errorf("content type is %q", rt.req.Header.Get("Content-Type"))
	case string(rt.reqBody) != string(audio):
		t.Error("the body is not the audio, byte for byte")
	}
	if !v.Complete || v.Probability != 0.981 || v.Model != smartturn.DefaultModel {
		t.Errorf("verdict = %+v", v)
	}
}

// What the endpointer asks: "turn off the... uh..." is not finished.
//
// verifies SPEC §4.5
func TestCompleteIsTheVerdict(t *testing.T) {
	for body, want := range map[string]bool{
		finished: true,
		`{"complete":false,"probability":0.009,"model":"smart-turn-v3.2-cpu"}`: false,
	} {
		done, err := clientOn(t, &roundTrip{body: body}).Complete(context.Background(), turn(1300))
		if err != nil || done != want {
			t.Errorf("%s: complete = %v, %v; want %v", body, done, err, want)
		}
	}
}

// A verdict from another checkpoint, or one that is not a probability, would
// end turns on someone else's judgement: it is an error, and the endpointer
// falls back to the silence.
//
// verifies SPEC §4.5
func TestAChangedSidecarIsRefused(t *testing.T) {
	cases := map[string]string{
		"smart turn v2":   `{"complete":true,"probability":0.9,"model":"smart-turn-v2"}`,
		"no model":        `{"complete":true,"probability":0.9}`,
		"above one":       `{"complete":true,"probability":1.4,"model":"smart-turn-v3.2-cpu"}`,
		"below zero":      `{"complete":false,"probability":-0.2,"model":"smart-turn-v3.2-cpu"}`,
		"truncated reply": `{"complete":true,"prob`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := clientOn(t, &roundTrip{body: body}).Complete(context.Background(), turn(900)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// The status alone does not say whether the audio was too short or the model
// failed; the body does, and only so much of it belongs in a log.
func TestAFailedRequestQuotesABoundedBody(t *testing.T) {
	long := strings.Repeat("x", 4096)
	c := clientOn(t, &roundTrip{status: http.StatusBadRequest, body: `{"detail":"1599 samples is under the 1600-sample minimum"}` + long})
	_, err := c.Complete(context.Background(), turn(90))
	if err == nil {
		t.Fatal("a 400 must fail the ask")
	}
	if !strings.Contains(err.Error(), "1600-sample minimum") {
		t.Errorf("error does not quote the cause: %v", err)
	}
	if len(err.Error()) > 2048 {
		t.Errorf("error is %d bytes; the body was not bounded", len(err.Error()))
	}
}

func TestADeadSidecarFails(t *testing.T) {
	c := clientOn(t, &roundTrip{err: errors.New("dial tcp 127.0.0.1:8891: connection refused")})
	_, err := c.Complete(context.Background(), turn(1900))
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error is %v, want the dial failure", err)
	}
}

// Alan kept talking: the endpointer cancels the ask about a pause that is
// over, and the request must not run on to a reply nobody reads.
//
// verifies SPEC §4.5
func TestACancelledContextAbandonsTheAsk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, err := smartturn.New(smartturn.Config{
		BaseURL: "http://smartturn.invalid", HTTP: &http.Client{Transport: &blockingTrip{cancel: cancel}},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.Complete(ctx, turn(700)); !errors.Is(err, context.Canceled) {
		t.Errorf("error is %v, want context.Canceled", err)
	}
}

// blockingTrip cancels the request's own context and then waits on it, the
// way a transport that is mid-write observes a cancellation.
type blockingTrip struct{ cancel context.CancelFunc }

func (b *blockingTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	b.cancel()
	<-r.Context().Done()
	return nil, r.Context().Err()
}

// Nothing to judge is not a request: the sidecar would answer 400, and an odd
// byte count means every later sample is a byte out of phase.
func TestUnjudgeableAudioMakesNoRequest(t *testing.T) {
	for name, audio := range map[string][]byte{"empty": nil, "odd": turn(900)[:28799]} {
		rt := &roundTrip{body: finished}
		if _, err := clientOn(t, rt).Complete(context.Background(), audio); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if rt.req != nil {
			t.Errorf("%s: a request was sent", name)
		}
	}
}

// A client with nothing but a URL is built for the sidecar the repo pins.
//
// verifies SPEC §10
func TestDefaultsNameThePinnedModel(t *testing.T) {
	c, err := smartturn.New(smartturn.Config{BaseURL: "http://smartturn.invalid"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if c.Model() != "smart-turn-v3.2-cpu" {
		t.Errorf("default model is %q", c.Model())
	}
	if _, err := smartturn.New(smartturn.Config{}); err == nil {
		t.Error("a client with no url was built")
	}
}
