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
//
// One test takes real speech, from outside the repo: -speakerid-wavs names a
// directory of <speaker>/*.wav (16 kHz s16le mono), enrolls every speaker
// with enough takes through internal/identity, identifies the rest, and
// prints the score distributions the thresholds there were set from.
package speakerid_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/provider/speakerid"
)

var (
	endpoint = flag.String("speakerid-url", "", "speaker-ID sidecar base URL; skips when empty")
	wavs     = flag.String("speakerid-wavs", "", "directory of <speaker>/*.wav to enroll and identify; skips when empty")
)

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
// the width or the model. Checked on the wire rather than through the
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

// speaker is one directory of the corpus: the takes in name order, so the
// corpus decides which ones enroll.
type speaker struct {
	id    string
	takes [][]byte
}

// readCorpus loads <dir>/<speaker>/*.wav. Anything that is not the device's
// format fails rather than being resampled here: the point is to measure the
// model on the audio the household's satellites produce.
func readCorpus(t *testing.T, dir string) []speaker {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus %s: %v", dir, err)
	}
	var out []speaker
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		paths, err := filepath.Glob(filepath.Join(dir, e.Name(), "*.wav"))
		if err != nil {
			t.Fatalf("glob %s: %v", e.Name(), err)
		}
		sort.Strings(paths)
		s := speaker{id: e.Name()}
		for _, p := range paths {
			pcm, err := readWAV(p)
			if err != nil {
				t.Fatalf("read %s: %v", p, err)
			}
			s.takes = append(s.takes, pcm)
		}
		if len(s.takes) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// readWAV returns the data chunk of a canonical PCM WAV in the device's format.
func readWAV(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF WAVE file")
	}
	var fmtSeen bool
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:]))
		body := b[off+8 : min(off+8+size, len(b))]
		switch id {
		case "fmt ":
			if len(body) < 16 {
				return nil, errors.New("fmt chunk is short")
			}
			format, channels := binary.LittleEndian.Uint16(body), binary.LittleEndian.Uint16(body[2:])
			rate, bits := binary.LittleEndian.Uint32(body[4:]), binary.LittleEndian.Uint16(body[14:])
			if format != 1 || channels != 1 || rate != bridge.SampleRate || int(bits) != bridge.BitsPerSample {
				return nil, fmt.Errorf("format %d, %d ch, %d Hz, %d bit; want PCM mono %d Hz %d bit",
					format, channels, rate, bits, bridge.SampleRate, bridge.BitsPerSample)
			}
			fmtSeen = true
		case "data":
			if !fmtSeen {
				return nil, errors.New("data chunk before fmt chunk")
			}
			return body, nil
		}
		// Chunks are word-aligned; an odd size carries a pad byte.
		off += 8 + size + size%2
	}
	return nil, io.ErrUnexpectedEOF
}

// trial is one cosine with the pair that produced it, so the extremes can be
// named: a threshold is set against the worst pair, not the distribution.
type trial struct {
	score           float64
	probe, centroid string
}

func scoresOf(ts []trial) []float64 {
	out := make([]float64, len(ts))
	for i, t := range ts {
		out[i] = t.score
	}
	return out
}

// extremes names the n highest or lowest trials.
func extremes(ts []trial, n int, highest bool) string {
	s := slices.Clone(ts)
	sort.Slice(s, func(i, j int) bool {
		if highest {
			return s[i].score > s[j].score
		}
		return s[i].score < s[j].score
	})
	var parts []string
	for _, t := range s[:min(n, len(s))] {
		parts = append(parts, fmt.Sprintf("%.3f %s vs %s", t.score, t.probe, t.centroid))
	}
	return strings.Join(parts, "; ")
}

// quantiles reports a distribution the way a threshold is chosen from it.
func quantiles(xs []float64) string {
	if len(xs) == 0 {
		return "n=0"
	}
	s := slices.Clone(xs)
	sort.Float64s(s)
	at := func(q float64) float64 { return s[min(len(s)-1, int(q*float64(len(s))))] }
	return fmt.Sprintf("n=%d min=%.3f p05=%.3f p50=%.3f p95=%.3f max=%.3f",
		len(s), s[0], at(0.05), at(0.5), at(0.95), s[len(s)-1])
}

// A real enrollment and identification pass over a corpus from outside the
// repo. Speakers with more than MinUtterances takes enroll from their first
// MinUtterances and are identified on the rest; the others are guests, who
// must not be identified as anyone. The distributions are logged every run:
// they are what identity.DefaultAccept and DefaultMargin were set from, and
// what a household re-measures them with.
//
// verifies SPEC §5
func TestARealCorpusEnrollsAndIdentifies(t *testing.T) {
	c := client(t)
	if *wavs == "" {
		t.Skip("no -speakerid-wavs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	corpus := readCorpus(t, *wavs)
	ids := identity.New(c)
	var held []speaker
	var guests []speaker
	for _, s := range corpus {
		if len(s.takes) <= identity.MinUtterances {
			guests = append(guests, s)
			continue
		}
		if err := ids.EnrollAudio(ctx, c, s.id, s.id, s.takes[:identity.MinUtterances]); err != nil {
			t.Fatalf("enroll %s: %v", s.id, err)
		}
		held = append(held, speaker{id: s.id, takes: s.takes[identity.MinUtterances:]})
	}
	if len(ids.People) < 2 {
		t.Skipf("%s: %d speakers with more than %d takes; two are the least that can be told apart", *wavs, len(ids.People), identity.MinUtterances)
	}
	r, err := identity.NewResolver(c, ids, identity.Thresholds{})
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	t.Logf("enrolled %d speakers from %d takes each; %d held-out speakers, %d guests", len(ids.People), identity.MinUtterances, len(held), len(guests))

	var same, other, guest []trial
	var margin []float64
	var trials, top1, identified, ambiguous, below, wrong, guestTrials, guestAccepted int
	for _, s := range held {
		for i, pcm := range s.takes {
			out, err := r.Resolve(ctx, pcm)
			if err != nil {
				t.Fatalf("resolve %s take %d: %v", s.id, i, err)
			}
			trials++
			scores := scoreAll(ids, out.Embedding)
			probe := fmt.Sprintf("%s/%d", s.id, i)
			same = append(same, trial{scores[s.id], probe, s.id})
			for id, sc := range scores {
				if id != s.id {
					other = append(other, trial{sc, probe, id})
				}
			}
			best := bestOf(scores)
			if best == s.id {
				top1++
				margin = append(margin, out.Score-out.RunnerScore)
			}
			switch {
			case out.Reason == identity.Identified && out.PersonID == s.id:
				identified++
			case out.Reason == identity.Identified:
				wrong++
				t.Errorf("%s take %d identified as %s (%.3f over %.3f)", s.id, i, out.PersonID, out.Score, out.RunnerScore)
			case out.Reason == identity.Ambiguous:
				ambiguous++
				t.Logf("%s take %d ambiguous: %s %.3f vs %s %.3f", s.id, i, best, out.Score, out.RunnerUp, out.RunnerScore)
			default:
				below++
				t.Logf("%s take %d below threshold: best %s at %.3f", s.id, i, best, out.Score)
			}
		}
	}
	for _, g := range guests {
		for i, pcm := range g.takes {
			out, err := r.Resolve(ctx, pcm)
			if err != nil {
				t.Fatalf("resolve guest %s take %d: %v", g.id, i, err)
			}
			guestTrials++
			for id, sc := range scoreAll(ids, out.Embedding) {
				guest = append(guest, trial{sc, fmt.Sprintf("%s/%d", g.id, i), id})
			}
			if out.Reason == identity.Identified {
				guestAccepted++
				t.Errorf("guest %s take %d identified as %s at %.3f", g.id, i, out.PersonID, out.Score)
			}
		}
	}

	t.Logf("held-out takes vs own centroid:        %s", quantiles(scoresOf(same)))
	t.Logf("held-out takes vs other centroids:     %s", quantiles(scoresOf(other)))
	t.Logf("guest takes vs every centroid:         %s", quantiles(scoresOf(guest)))
	t.Logf("best minus runner-up, correct top-1:   %s", quantiles(margin))
	t.Logf("lowest same-speaker:   %s", extremes(same, 5, false))
	t.Logf("highest other-speaker: %s", extremes(other, 5, true))
	t.Logf("highest guest:         %s", extremes(guest, 5, true))
	// What each candidate accept would cost on this corpus: a false accept
	// hands over a person's context, a false reject is a guest turn.
	for _, accept := range []float64{0.5, 0.55, 0.6, 0.65, 0.7, 0.75} {
		var fa, fr int
		for _, tr := range slices.Concat(other, guest) {
			if tr.score >= accept {
				fa++
			}
		}
		for _, tr := range same {
			if tr.score < accept {
				fr++
			}
		}
		t.Logf("accept %.2f: %d false accepts of %d, %d false rejects of %d", accept, fa, len(other)+len(guest), fr, len(same))
	}
	t.Logf("top-1 accuracy ignoring thresholds: %d/%d", top1, trials)
	t.Logf("with accept=%.2f margin=%.2f: identified %d, ambiguous %d, below threshold %d, wrong %d of %d; guests rejected %d/%d",
		identity.DefaultAccept, identity.DefaultMargin, identified, ambiguous, below, wrong, trials, guestTrials-guestAccepted, guestTrials)
	if top1 < trials {
		t.Errorf("top-1 accuracy %d/%d: the model does not separate this corpus", top1, trials)
	}
}

// scoreAll is the cosine against every centroid, which Match only reports
// the top two of.
func scoreAll(ids *identity.Identities, embedding []float32) map[string]float64 {
	n := norm(embedding)
	out := make(map[string]float64, len(ids.People))
	for _, p := range ids.People {
		var dot float64
		for i := range embedding {
			dot += float64(embedding[i]) * float64(p.Centroid[i])
		}
		out[p.ID] = dot / n
	}
	return out
}

func bestOf(scores map[string]float64) string {
	best, bestScore := "", math.Inf(-1)
	for id, s := range scores {
		if s > bestScore || (s == bestScore && strings.Compare(id, best) < 0) {
			best, bestScore = id, s
		}
	}
	return best
}
