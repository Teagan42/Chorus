// Package identity is the speaker half of SPEC §5: explicit enrollment into one
// centroid per person, cosine matching of every utterance against those
// centroids, and the id the session puts on a transcript -- empty for a guest.
//
// The embedding model sits behind Embedder and is not this package's concern;
// internal/provider/speakerid fills the seam (SPEC §10). What this package
// owns is the household's centroids, which are biometric and live in a
// gitignored file next to devices.yaml rather than in the journal database:
// SPEC §13 keeps secrets out of anything that would need a separate backup,
// and a voiceprint cannot be rotated the way a PSK can (ADR-0025).
package identity

import (
	"context"
	"fmt"
	"math"
)

// Embedder turns one utterance into a speaker embedding. It is the seam
// SPEC §10 names TitaNet-L for; a test fills it with vectors it chose.
type Embedder interface {
	// Embed takes one utterance as PCM at bridge.SampleRate and
	// bridge.BitsPerSample, mono, little-endian, and returns Dim() values.
	Embed(ctx context.Context, pcm []byte) ([]float32, error)

	// Model names the model. Centroids from one model are noise against
	// another, so the identities file records it and a swap is refused.
	Model() string

	// Dim is the embedder's to declare. 192 is TitaNet-L's width, not this
	// package's assumption, and the file records whatever was enrolled.
	Dim() int
}

// Thresholds decide an identification. Measured 2026-10-08 with TitaNet-L on
// 37 speakers of sr-data and AudioMNIST (ADR-0029); a household's own
// recordings should replace them through the models tier's -speakerid-wavs.
const (
	// DefaultAccept is the cosine below which the best match is a guest. At
	// 0.70 no cross-speaker take of 2748 passed (max 0.676) and 2 of 80 own
	// takes were refused (min 0.659): a wrong value fails to guest.
	DefaultAccept = 0.70

	// DefaultMargin is how far the best match must lead the runner-up before
	// it is believed, so two similar voices cannot flip-flop a conversation.
	// Correct identifications led by at least 0.138 (p05 0.182, n=80).
	DefaultMargin = 0.10
)

// Thresholds overrides the defaults. A zero field keeps the default.
type Thresholds struct {
	Accept float64
	Margin float64
}

func (t Thresholds) withDefaults() Thresholds {
	if t.Accept == 0 {
		t.Accept = DefaultAccept
	}
	if t.Margin == 0 {
		t.Margin = DefaultMargin
	}
	return t
}

// Reason says why an utterance resolved as it did. It is kept apart from the
// id because the rejections are the tuning corpus (SPEC §4.3): how often a
// household lands on Ambiguous is what says whether to re-enroll.
type Reason string

const (
	Identified     Reason = "identified"
	NobodyEnrolled Reason = "nobody_enrolled"
	BelowThreshold Reason = "below_threshold"

	// Ambiguous is unknown as far as the session is concerned: SPEC §5 gives
	// a low-confidence speaker guest context, and guessing between two
	// enrolled people would hand one person's context to the other.
	Ambiguous Reason = "ambiguous"
)

// Outcome is one utterance's attribution. PersonID is empty unless Reason is
// Identified, which is exactly the contract session.Transcript.SpeakerID has.
type Outcome struct {
	PersonID string
	Reason   Reason

	// Score is the best cosine, kept even on a rejection: a threshold is only
	// tunable against what the rejected candidates actually scored.
	Score       float64
	RunnerUp    string
	RunnerScore float64

	// Embedding is the utterance's own vector, set by Resolver.Resolve. SPEC §5
	// stores it on every trace record; the journal field is a follow-up.
	Embedding []float32
}

// Match scores one embedding against every centroid. The embedding is
// normalised here rather than trusted: a centroid is unit length by
// construction, but an embedder's output need not be.
func (ids *Identities) Match(embedding []float32, th Thresholds) (Outcome, error) {
	if len(embedding) != ids.Dim {
		return Outcome{}, fmt.Errorf("match: embedding has %d dims, enrolled centroids have %d", len(embedding), ids.Dim)
	}
	probe, err := normalise(embedding)
	if err != nil {
		return Outcome{}, fmt.Errorf("match: %w", err)
	}
	if len(ids.People) == 0 {
		return Outcome{Reason: NobodyEnrolled}, nil
	}

	th = th.withDefaults()
	scores := make([]float64, len(ids.People))
	best, second := -1, -1
	for i := range ids.People {
		scores[i] = dot(probe, ids.People[i].Centroid)
		switch {
		case best < 0 || scores[i] > scores[best]:
			best, second = i, best
		case second < 0 || scores[i] > scores[second]:
			second = i
		}
	}

	out := Outcome{Score: scores[best]}
	if second >= 0 {
		out.RunnerUp = ids.People[second].ID
		out.RunnerScore = scores[second]
	}
	switch {
	case out.Score < th.Accept:
		out.Reason = BelowThreshold
	case second >= 0 && out.Score-out.RunnerScore < th.Margin:
		out.Reason = Ambiguous
	default:
		out.Reason = Identified
		out.PersonID = ids.People[best].ID
	}
	return out, nil
}

// Resolver is what the Listening child calls once per utterance: audio in,
// the id for Transcript.SpeakerID out (SPEC §5). It reads the household and
// never writes it; enroll first, then build the resolver.
type Resolver struct {
	emb Embedder
	ids *Identities
	th  Thresholds
}

// NewResolver refuses an embedder the household was not enrolled with. The
// cosines would still compute, and every one of them would be noise.
func NewResolver(emb Embedder, ids *Identities, th Thresholds) (*Resolver, error) {
	if emb == nil {
		return nil, fmt.Errorf("identity: embedder is required")
	}
	if ids == nil {
		return nil, fmt.Errorf("identity: identities are required")
	}
	if ids.Model != emb.Model() || ids.Dim != emb.Dim() {
		return nil, fmt.Errorf("identity: household enrolled with %s (%d dims) but the embedder is %s (%d dims); re-enroll",
			ids.Model, ids.Dim, emb.Model(), emb.Dim())
	}
	return &Resolver{emb: emb, ids: ids, th: th.withDefaults()}, nil
}

// Resolve embeds one utterance and matches it. The embedding rides along on
// the outcome so the caller can journal it without a second model call.
func (r *Resolver) Resolve(ctx context.Context, pcm []byte) (Outcome, error) {
	e, err := r.emb.Embed(ctx, pcm)
	if err != nil {
		return Outcome{}, fmt.Errorf("embed %d-byte utterance: %w", len(pcm), err)
	}
	out, err := r.ids.Match(e, r.th)
	if err != nil {
		return Outcome{}, err
	}
	out.Embedding = e
	return out, nil
}

// SpeakerID is Resolve reduced to the session's contract: the person, or
// empty for a guest. An error also yields empty, because a sidecar that is
// down must not stop the household talking to the house -- the caller logs
// it and the utterance is a guest's (SPEC §5).
func (r *Resolver) SpeakerID(ctx context.Context, pcm []byte) (string, error) {
	out, err := r.Resolve(ctx, pcm)
	if err != nil {
		return "", err
	}
	return out.PersonID, nil
}

// Household is the enrolled ids in enrollment order, which is what the
// barge-in gate takes as its known speakers (session.Gate, SPEC §4.3).
func (ids *Identities) Household() []string {
	out := make([]string, len(ids.People))
	for i := range ids.People {
		out[i] = ids.People[i].ID
	}
	return out
}

func dot(a, b []float32) float64 {
	var acc float64
	for i := range a {
		acc += float64(a[i]) * float64(b[i])
	}
	return acc
}

// normalise returns the unit vector, refusing what has no direction: a zero or
// non-finite vector would make every cosine NaN and NaN compares false against
// every threshold, which reads as a confident rejection.
func normalise(v []float32) ([]float32, error) {
	var sum float64
	for _, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, fmt.Errorf("vector is not finite")
		}
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return nil, fmt.Errorf("vector is zero")
	}
	n := math.Sqrt(sum)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out, nil
}
