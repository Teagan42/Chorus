package speakerid_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/provider/speakerid"
)

// roundTrip serves a canned reply in-process and keeps the request that asked
// for it. No listener: hermetic means hermetic (CONTRIBUTING).
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

// A four-wide client keeps the fixtures readable; the width is the sidecar's
// to declare and the client only checks the two agree.
const dim = 4

const reply = `{"embedding":[0.5,0.5,0.5,0.5],"dim":4,"model":"fake"}`

func clientOn(t *testing.T, rt *roundTrip) *speakerid.Client {
	t.Helper()
	c, err := speakerid.New(speakerid.Config{
		BaseURL: "http://speakerid.invalid/", Model: "fake", Dim: dim,
		HTTP: &http.Client{Transport: rt},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

// pcm is n whole samples of something that is not silence.
func pcm(n int) []byte {
	out := make([]byte, 2*n)
	for i := range out {
		out[i] = byte(i * 7)
	}
	return out
}

// The request is the whole contract from the sidecar's side: raw PCM, labelled
// as such, at the one path. Anything else is a sidecar someone else wrote.
//
// verifies SPEC §10
func TestItPostsRawPCMToTheEmbedPath(t *testing.T) {
	rt := &roundTrip{body: reply}
	c := clientOn(t, rt)
	audio := pcm(1600)

	got, err := c.Embed(context.Background(), audio)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	switch {
	case rt.req.Method != http.MethodPost:
		t.Errorf("method is %s", rt.req.Method)
	case rt.req.URL.String() != "http://speakerid.invalid/v1/embed":
		t.Errorf("posted to %s", rt.req.URL)
	case rt.req.Header.Get("Content-Type") != speakerid.ContentType:
		t.Errorf("content type is %q", rt.req.Header.Get("Content-Type"))
	case string(rt.reqBody) != string(audio):
		t.Error("the body is not the audio, byte for byte")
	}
	if len(got) != dim || got[0] != 0.5 {
		t.Errorf("embedding = %v", got)
	}
}

// Centroids are only comparable within one model at one width, so a reply
// that disagrees with what the client was built for is refused outright
// rather than handed to the matcher.
//
// verifies SPEC §5
func TestAChangedSidecarIsRefused(t *testing.T) {
	cases := map[string]string{
		"narrower":            `{"embedding":[1,0,0],"dim":3,"model":"fake"}`,
		"wider":               `{"embedding":[1,0,0,0,0],"dim":5,"model":"fake"}`,
		"dim field disagrees": `{"embedding":[1,0,0,0],"dim":3,"model":"fake"}`,
		"other model":         `{"embedding":[1,0,0,0],"dim":4,"model":"someone-elses"}`,
		"no model":            `{"embedding":[1,0,0,0],"dim":4}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c := clientOn(t, &roundTrip{body: body})
			if _, err := c.Embed(context.Background(), pcm(1600)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// The status alone does not say whether the audio was too short or the model
// failed to load; the body does, and only so much of it belongs in a log.
func TestAFailedRequestQuotesABoundedBody(t *testing.T) {
	long := strings.Repeat("x", 4096)
	c := clientOn(t, &roundTrip{status: http.StatusBadRequest, body: `{"detail":"audio too short"}` + long})
	_, err := c.Embed(context.Background(), pcm(16))
	if err == nil {
		t.Fatal("a 400 must fail the embed")
	}
	if !strings.Contains(err.Error(), "audio too short") {
		t.Errorf("error does not quote the cause: %v", err)
	}
	if len(err.Error()) > 2048 {
		t.Errorf("error is %d bytes; the body was not bounded", len(err.Error()))
	}
}

func TestAMalformedReplyFails(t *testing.T) {
	c := clientOn(t, &roundTrip{body: `{"embedding":[0.5,`})
	if _, err := c.Embed(context.Background(), pcm(1600)); err == nil {
		t.Fatal("a truncated reply was accepted")
	}
}

func TestADeadSidecarFails(t *testing.T) {
	c := clientOn(t, &roundTrip{err: errors.New("dial tcp: connection refused")})
	_, err := c.Embed(context.Background(), pcm(1600))
	if err == nil {
		t.Fatal("a dial failure was accepted")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error lost the cause: %v", err)
	}
}

// The barge-in gate runs on the stream's context, and a stop while an embed
// is in flight must not leave the request running to a reply nobody reads.
//
// verifies SPEC §4.3
func TestACancelledContextAbandonsTheEmbed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &blockingTrip{cancel: cancel}
	c, err := speakerid.New(speakerid.Config{
		BaseURL: "http://speakerid.invalid", Model: "fake", Dim: dim,
		HTTP: &http.Client{Transport: rt},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.Embed(ctx, pcm(1600)); !errors.Is(err, context.Canceled) {
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

// Nothing to embed is not a request: the sidecar would answer 400, and an odd
// byte count means every later sample is a byte out of phase.
func TestUnembeddableAudioMakesNoRequest(t *testing.T) {
	for name, audio := range map[string][]byte{
		"empty": nil,
		"odd":   pcm(800)[:1599],
	} {
		rt := &roundTrip{body: reply}
		c := clientOn(t, rt)
		if _, err := c.Embed(context.Background(), audio); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if rt.req != nil {
			t.Errorf("%s: a request was sent", name)
		}
	}
}

// The defaults are the contract the sidecar README states, so a client with
// nothing but a URL is built for the real sidecar.
//
// verifies SPEC §10
func TestDefaultsNameTheSpecifiedModel(t *testing.T) {
	c, err := speakerid.New(speakerid.Config{BaseURL: "http://speakerid.invalid"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if c.Model() != speakerid.DefaultModel || c.Dim() != speakerid.DefaultDim {
		t.Errorf("defaults are %s at %d", c.Model(), c.Dim())
	}
	if !strings.Contains(c.Model(), "ecapa") {
		t.Errorf("default model %q is not the ECAPA-TDNN SPEC §10 names", c.Model())
	}
}

func TestNewRejectsIncompleteWiring(t *testing.T) {
	for name, cfg := range map[string]speakerid.Config{
		"no url":       {},
		"negative dim": {BaseURL: "http://speakerid.invalid", Dim: -1},
	} {
		if _, err := speakerid.New(cfg); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}
