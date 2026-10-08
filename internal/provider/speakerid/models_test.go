//go:build models

// Model tier: needs a reachable speaker-ID sidecar. Run with `task test:models`,
// pointing it at one:
//
//	go test -tags=models ./internal/provider/speakerid/ -speakerid-url http://127.0.0.1:8890
//
// The endpoint is a flag with no default so no household address lives in the
// repo. Without it these skip.
//
// What is tested here is the half the hermetic tier cannot reach: that the
// real sidecar still honours the contract the client is built for -- width,
// model, unit length -- and what an embed costs. The audio is synthetic on
// purpose: a real household recording never enters the repo (CONTRIBUTING §7),
// and the assertions are about the contract, not about who is speaking.
package speakerid_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"math"
	"math/rand/v2"
	"net/http"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/provider/speakerid"
)

var endpoint = flag.String("speakerid-url", "", "speaker-ID sidecar base URL; skips when empty")

// embedBudget is generous on purpose: the first call after boot may still be
// warming the model, and a CPU box runs it many times slower than a GPU.
const embedBudget = time.Minute

func client(t *testing.T) *speakerid.Client {
	t.Helper()
	if *endpoint == "" {
		t.Skip("no -speakerid-url")
	}
	c, err := speakerid.New(speakerid.Config{BaseURL: *endpoint})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

// voiced is seconds of a harmonic tone at f0 with a slow tremolo: not speech,
// but periodic the way speech is, so the model sees something other than
// noise. Two different f0s are two different "voices".
func voiced(f0 float64, seconds float64) []byte {
	n := int(seconds * bridge.SampleRate)
	out := make([]byte, 2*n)
	for i := range n {
		t := float64(i) / bridge.SampleRate
		var v float64
		for h := 1; h <= 6; h++ {
			v += math.Sin(2*math.Pi*f0*float64(h)*t) / float64(h)
		}
		v *= 0.3 * (1 + 0.2*math.Sin(2*math.Pi*5*t))
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(v*math.MaxInt16)))
	}
	return out
}

// noise is seconds of white noise at a fixed seed.
func noise(seconds float64, seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, seed))
	n := int(seconds * bridge.SampleRate)
	out := make([]byte, 2*n)
	for i := range n {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16((2*r.Float64()-1)*8000)))
	}
	return out
}

// embed runs one call and logs what it cost.
func embed(t *testing.T, c *speakerid.Client, name string, pcm []byte) []float32 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), embedBudget)
	defer cancel()

	start := time.Now()
	e, err := c.Embed(ctx, pcm)
	if err != nil {
		t.Fatalf("embed %s: %v", name, err)
	}
	// Logged every run: the barge-in gate has ~300 ms end to end (SPEC §4.3),
	// and a regression here is invisible in a pass/fail.
	t.Logf("%s (%v of audio) -> %d dims in %v", name, duration(pcm), len(e), time.Since(start).Round(time.Millisecond))
	return e
}

func duration(pcm []byte) time.Duration {
	frames := len(pcm) / (bridge.BitsPerSample / 8)
	return time.Duration(frames) * time.Second / bridge.SampleRate
}

func norm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}

func cosine(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot / (norm(a) * norm(b))
}

// The contract the matcher is built on: the declared width, unit length, and
// finite everywhere. A sidecar that stopped normalising would still pass the
// client's checks and skew every cosine.
//
// verifies SPEC §10
func TestARealSidecarEmbedsToTheContract(t *testing.T) {
	e := embed(t, client(t), "tone", voiced(140, 2))
	if len(e) != speakerid.DefaultDim {
		t.Fatalf("%d dims, want %d", len(e), speakerid.DefaultDim)
	}
	for i, x := range e {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			t.Fatalf("dim %d is %v", i, x)
		}
	}
	if n := norm(e); math.Abs(n-1) > 0.01 {
		t.Errorf("embedding has length %.4f; the sidecar is meant to normalise", n)
	}
}

// Two different signals must not embed identically, and the same signal
// twice must: an embedder that collapses everything to one vector would still
// pass every shape check, and one that is not deterministic cannot be enrolled
// against.
//
// verifies SPEC §5
func TestDifferentSignalsEmbedDifferentlyAndTheSameSignalTheSame(t *testing.T) {
	c := client(t)
	low := embed(t, c, "low tone", voiced(110, 2))
	high := embed(t, c, "high tone", voiced(260, 2))
	hiss := embed(t, c, "noise", noise(2, 1))
	again := embed(t, c, "low tone again", voiced(110, 2))

	if s := cosine(low, high); s > 0.99 {
		t.Errorf("two tones an octave apart embed at cosine %.4f", s)
	}
	if s := cosine(low, hiss); s > 0.99 {
		t.Errorf("a tone and noise embed at cosine %.4f", s)
	}
	if s := cosine(low, again); s < 0.999 {
		t.Errorf("the same audio twice embeds at cosine %.4f", s)
	}
}

// The one failure nothing else would catch: a sidecar upgrade that changed
// the width or the checkpoint. Checked on the wire rather than through the
// client, so the message names the sidecar and not the client's refusal.
//
// verifies SPEC §10
func TestTheSidecarStillDeclaresTheContract(t *testing.T) {
	if *endpoint == "" {
		t.Skip("no -speakerid-url")
	}
	ctx, cancel := context.WithTimeout(context.Background(), embedBudget)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, *endpoint+"/v1/embed", bytes.NewReader(voiced(140, 1)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", speakerid.ContentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("embed: %s", resp.Status)
	}
	var got struct {
		Embedding []float32 `json:"embedding"`
		Dim       int       `json:"dim"`
		Model     string    `json:"model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	t.Logf("sidecar serves %s at %d dims", got.Model, got.Dim)

	if got.Dim != speakerid.DefaultDim || len(got.Embedding) != speakerid.DefaultDim {
		t.Errorf("the sidecar embeds at %d dims (%d sent); every enrolled centroid assumes %d", got.Dim, len(got.Embedding), speakerid.DefaultDim)
	}
	if got.Model != speakerid.DefaultModel {
		t.Errorf("the sidecar serves %q; every enrolled centroid assumes %q", got.Model, speakerid.DefaultModel)
	}
}

// Audio too short to embed must fail the request rather than return a vector
// of nothing: a guest would otherwise be whoever the model's bias favours.
func TestTooLittleAudioFailsTheEmbed(t *testing.T) {
	c := client(t)
	ctx, cancel := context.WithTimeout(context.Background(), embedBudget)
	defer cancel()
	if _, err := c.Embed(ctx, voiced(140, 0.01)); err == nil {
		t.Fatal("10 ms embedded")
	} else {
		t.Logf("too short: %v", err)
	}
}
