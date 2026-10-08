package identity_test

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/identity"
)

// A take of an enrolled voice comes back as that person, with the cosine the
// decision was made on.
//
// verifies SPEC §5
func TestANearDuplicateOfAnEnrolledVoiceIsIdentified(t *testing.T) {
	ids := household()
	out, err := ids.Match(noisy(axis(0), 0.1, 9), identity.Thresholds{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if out.PersonID != "alan" || out.Reason != identity.Identified {
		t.Fatalf("got %+v, want alan identified", out)
	}
	if out.Score < 0.9 {
		t.Errorf("score %.3f for a near duplicate", out.Score)
	}
	if out.RunnerUp != "beth" {
		t.Errorf("runner-up is %q, want beth", out.RunnerUp)
	}
}

// Nobody enrolled is the fresh-install case: every speaker is a guest, and
// there must be no error about it, or the house would not answer anyone.
//
// verifies SPEC §5
func TestAnEmptyHouseholdMakesEveryoneAGuest(t *testing.T) {
	ids := &identity.Identities{Model: "fake", Dim: dim}
	out, err := ids.Match(axis(0), identity.Thresholds{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if out.PersonID != "" || out.Reason != identity.NobodyEnrolled {
		t.Errorf("got %+v, want a guest", out)
	}
}

// The television and a visitor both land here: orthogonal to everyone, so the
// best cosine is far below any acceptance threshold and the id is empty.
//
// verifies SPEC §5
func TestAnUnenrolledVoiceIsAGuest(t *testing.T) {
	ids := household()
	out, err := ids.Match(axis(3), identity.Thresholds{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if out.PersonID != "" || out.Reason != identity.BelowThreshold {
		t.Errorf("got %+v, want below threshold", out)
	}
	// The score stays on a rejection: it is what the threshold gets tuned on.
	if out.Score > 0.2 {
		t.Errorf("an orthogonal probe scored %.3f", out.Score)
	}
}

// A probe halfway between two household voices must not be given to either:
// guessing hands one person's context to the other, and the guess would flip
// on the next utterance.
//
// verifies SPEC §5
func TestAProbeBetweenTwoVoicesIsAmbiguousNotAGuess(t *testing.T) {
	ids := household()
	out, err := ids.Match(between(axis(0), axis(1), 0.5), identity.Thresholds{})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if out.PersonID != "" || out.Reason != identity.Ambiguous {
		t.Errorf("got %+v, want ambiguous with no id", out)
	}
	if out.Score < identity.DefaultAccept {
		t.Errorf("score %.3f is below accept, so the margin was never the reason", out.Score)
	}
}

// The margin is what separates "closer to alan" from "alan": the lead has to
// clear it. A probe that leans toward alan by more than the margin is his;
// one that leans by less is nobody's.
//
// verifies SPEC §5
func TestTheMarginDecidesBetweenTwoCloseVoices(t *testing.T) {
	ids := household()
	wide := identity.Thresholds{Margin: 0.4}
	narrow := identity.Thresholds{Margin: 0.01}
	probe := between(axis(0), axis(1), 0.4) // leans to alan by ~0.28

	if out, _ := ids.Match(probe, wide); out.Reason != identity.Ambiguous {
		t.Errorf("under a wide margin: %+v, want ambiguous", out)
	}
	if out, _ := ids.Match(probe, narrow); out.PersonID != "alan" {
		t.Errorf("under a narrow margin: %+v, want alan", out)
	}
}

// With one person enrolled there is no runner-up, so there is nothing for the
// margin to measure against and only the accept threshold applies.
func TestASolePersonNeedsNoMargin(t *testing.T) {
	ids := &identity.Identities{Model: "fake", Dim: dim}
	must(ids.Enroll("alan", "", [][]float32{axis(0), axis(0), axis(0)}))
	out, err := ids.Match(noisy(axis(0), 0.1, 5), identity.Thresholds{Margin: 0.99})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if out.PersonID != "alan" {
		t.Errorf("got %+v, want alan", out)
	}
	if out.RunnerUp != "" {
		t.Errorf("runner-up %q with one person enrolled", out.RunnerUp)
	}
}

// The accept threshold is applied to the raw cosine, so a probe that is
// nobody's by a hair fails and one that is somebody's by a hair passes.
func TestTheAcceptThresholdIsACosine(t *testing.T) {
	ids := &identity.Identities{Model: "fake", Dim: dim}
	must(ids.Enroll("alan", "", [][]float32{axis(0), axis(0), axis(0)}))
	// cos(60°) = 0.5 exactly.
	probe := []float32{0.5, float32(math.Sqrt(0.75)), 0, 0}

	if out, _ := ids.Match(probe, identity.Thresholds{Accept: 0.51}); out.PersonID != "" {
		t.Errorf("accepted at 0.51: %+v", out)
	}
	if out, _ := ids.Match(probe, identity.Thresholds{Accept: 0.49}); out.PersonID != "alan" {
		t.Errorf("rejected at 0.49: %+v", out)
	}
}

// The cosine is a direction, so a quiet take of the same voice must score the
// same as a loud one.
func TestScaleDoesNotChangeTheScore(t *testing.T) {
	ids := household()
	loud, _ := ids.Match(scale(axis(0), 40), identity.Thresholds{})
	quiet, _ := ids.Match(scale(axis(0), 0.01), identity.Thresholds{})
	if math.Abs(loud.Score-quiet.Score) > 1e-6 {
		t.Errorf("loud scored %.6f, quiet %.6f", loud.Score, quiet.Score)
	}
}

// A vector of the wrong width or with no direction is a bug upstream, not a
// guest: silently scoring it would hide a model swap.
func TestMatchRefusesAVectorItCannotScore(t *testing.T) {
	ids := household()
	for name, probe := range map[string][]float32{
		"wrong dims": {1, 0},
		"zero":       make([]float32, dim),
		"nan":        {float32(math.NaN()), 0, 0, 0},
	} {
		if _, err := ids.Match(probe, identity.Thresholds{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The resolver is the per-utterance call the Listening child makes: audio in,
// the id for Transcript.SpeakerID out, empty for a guest.
//
// verifies SPEC §5
func TestTheResolverTurnsAudioIntoASpeakerID(t *testing.T) {
	emb := newFake()
	emb.by["alan says hi"] = noisy(axis(0), 0.1, 7)
	emb.by["the tv"] = axis(2)
	r, err := identity.NewResolver(emb, household(), identity.Thresholds{})
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}

	if id, err := r.SpeakerID(context.Background(), []byte("alan says hi")); err != nil || id != "alan" {
		t.Errorf("alan resolved to %q, %v", id, err)
	}
	if id, err := r.SpeakerID(context.Background(), []byte("the tv")); err != nil || id != "" {
		t.Errorf("the tv resolved to %q, %v", id, err)
	}
}

// A sidecar that is down yields a guest, not a dead house. The error still
// surfaces so it can be logged.
//
// verifies SPEC §5
func TestAFailedEmbeddingIsAGuestWithAnError(t *testing.T) {
	r, err := identity.NewResolver(newFake(), household(), identity.Thresholds{})
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	id, err := r.SpeakerID(context.Background(), []byte("unknown audio"))
	if err == nil {
		t.Fatal("a failed embed must report")
	}
	if id != "" {
		t.Errorf("id is %q on a failed embed", id)
	}
	if !strings.Contains(err.Error(), "embed") {
		t.Errorf("error does not say what failed: %v", err)
	}
}

// SPEC §5 stores the embedding on every trace record, matched or not, so the
// resolver hands it back rather than making the journal embed twice.
//
// verifies SPEC §5
func TestResolveCarriesTheEmbeddingForTheJournal(t *testing.T) {
	emb := newFake()
	emb.by["guest"] = axis(3)
	r, err := identity.NewResolver(emb, household(), identity.Thresholds{})
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	out, err := r.Resolve(context.Background(), []byte("guest"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if out.PersonID != "" {
		t.Errorf("a guest resolved to %q", out.PersonID)
	}
	if len(out.Embedding) != dim || out.Embedding[3] != 1 {
		t.Errorf("embedding %v was not carried", out.Embedding)
	}
}

// Centroids are only comparable within the model that produced them. A
// resolver built on the wrong embedder would score every utterance as noise.
func TestTheResolverRefusesAnEmbedderTheHouseholdWasNotEnrolledWith(t *testing.T) {
	other := newFake()
	other.model = "other-model"
	if _, err := identity.NewResolver(other, household(), identity.Thresholds{}); err == nil {
		t.Error("a different model was accepted")
	}

	wide := newFake()
	wide.dim = dim + 1
	if _, err := identity.NewResolver(wide, household(), identity.Thresholds{}); err == nil {
		t.Error("a different dimension was accepted")
	}
}

// The gate's household list is the enrolled set and nothing else, so an
// enrollment is what lets a person interrupt (session.Gate).
//
// verifies SPEC §4.3
func TestTheHouseholdIsTheEnrolledSet(t *testing.T) {
	got := household().Household()
	if len(got) != 2 || got[0] != "alan" || got[1] != "beth" {
		t.Errorf("household = %v", got)
	}
	if len((&identity.Identities{Model: "fake", Dim: dim}).Household()) != 0 {
		t.Error("an empty household has members")
	}
}
