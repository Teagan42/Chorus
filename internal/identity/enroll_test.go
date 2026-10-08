package identity_test

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/identity"
)

// The centroid is the normalised mean: three takes that lean the same way
// land between them, at unit length.
//
// verifies SPEC §5
func TestTheCentroidIsTheUnitMeanOfTheTakes(t *testing.T) {
	// Two takes along axis 0 and one leaning toward axis 1.
	c, err := identity.Centroid([][]float32{axis(0), axis(0), between(axis(0), axis(1), 0.5)})
	if err != nil {
		t.Fatalf("centroid: %v", err)
	}
	if n := norm(c); math.Abs(n-1) > 1e-6 {
		t.Errorf("centroid has length %.6f, want 1", n)
	}
	if c[0] <= c[1] || c[1] <= 0 || c[2] != 0 {
		t.Errorf("centroid %v does not sit between the takes", c)
	}
}

// Each take is normalised before averaging, so a loud recording does not
// outvote a quiet one: the model's norm follows level, not identity.
//
// verifies SPEC §5
func TestALoudTakeDoesNotOutvoteAQuietOne(t *testing.T) {
	balanced, err := identity.Centroid([][]float32{axis(0), axis(1), axis(0), axis(1)})
	if err != nil {
		t.Fatalf("centroid: %v", err)
	}
	loud, err := identity.Centroid([][]float32{scale(axis(0), 100), axis(1), scale(axis(0), 100), axis(1)})
	if err != nil {
		t.Fatalf("centroid: %v", err)
	}
	for i := range balanced {
		if math.Abs(float64(balanced[i]-loud[i])) > 1e-6 {
			t.Fatalf("loud takes moved the centroid: %v vs %v", balanced, loud)
		}
	}
}

// One phrase's embedding is that phrase as much as the voice.
//
// verifies SPEC §5
func TestEnrollmentNeedsSeveralPhrases(t *testing.T) {
	_, err := identity.Centroid([][]float32{axis(0), axis(0)})
	if err == nil {
		t.Fatal("two takes were enough")
	}
	if !strings.Contains(err.Error(), "at least 3") {
		t.Errorf("error does not say how many: %v", err)
	}
}

func TestCentroidRefusesWhatItCannotAverage(t *testing.T) {
	cases := map[string][][]float32{
		"mixed widths": {axis(0), axis(0), {1, 0}},
		"empty take":   {{}, {}, {}},
		"cancelling":   {axis(0), scale(axis(0), -1), axis(1), scale(axis(1), -1)},
		"zero take":    {axis(0), axis(0), make([]float32, dim)},
	}
	for name, takes := range cases {
		if _, err := identity.Centroid(takes); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The id is what every later transcript carries, so enrolling it twice would
// make attribution ambiguous by construction.
func TestEnrollRefusesAReusedID(t *testing.T) {
	ids := household()
	err := ids.Enroll("alan", "", [][]float32{axis(2), axis(2), axis(2)})
	if err == nil {
		t.Fatal("alan was enrolled twice")
	}
	if !strings.Contains(err.Error(), "alan") {
		t.Errorf("error does not name the person: %v", err)
	}
	if len(ids.People) != 2 {
		t.Errorf("household has %d people after a refused enrollment", len(ids.People))
	}
}

func TestEnrollRefusesTheWrongWidth(t *testing.T) {
	ids := &identity.Identities{Model: "fake", Dim: dim + 1}
	if err := ids.Enroll("alan", "", [][]float32{axis(0), axis(0), axis(0)}); err == nil {
		t.Error("a 4-dim centroid joined a 5-dim household")
	}
	if err := ids.Enroll("", "", [][]float32{axis(0), axis(0), axis(0)}); err == nil {
		t.Error("a person with no id was enrolled")
	}
}

// Enrollment from audio embeds every phrase exactly once and records how many
// there were, which is what tells a thin enrollment from a bad one later.
//
// verifies SPEC §5
func TestEnrollAudioEmbedsEveryPhrase(t *testing.T) {
	emb := newFake()
	phrases := [][]byte{[]byte("one"), []byte("two"), []byte("three"), []byte("four")}
	for i, p := range phrases {
		emb.by[string(p)] = noisy(axis(2), 0.05, uint64(i+1))
	}
	ids := identity.New(emb)
	if err := ids.EnrollAudio(context.Background(), emb, "cass", "Cass", phrases); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if emb.calls != len(phrases) {
		t.Errorf("embedded %d times for %d phrases", emb.calls, len(phrases))
	}
	p := ids.People[0]
	if p.ID != "cass" || p.Name != "Cass" || p.Utterances != len(phrases) {
		t.Errorf("enrolled %+v", p)
	}
	if out, _ := ids.Match(axis(2), identity.Thresholds{}); out.PersonID != "cass" {
		t.Errorf("cass does not match her own voice: %+v", out)
	}
}

// A phrase that fails to embed fails the enrollment: a centroid from two of
// three takes is a weaker voiceprint than the one the person was promised.
func TestEnrollAudioFailsOnAnyFailedPhrase(t *testing.T) {
	emb := newFake()
	emb.by["one"], emb.by["two"] = axis(2), axis(2)
	ids := identity.New(emb)
	err := ids.EnrollAudio(context.Background(), emb, "cass", "", [][]byte{[]byte("one"), []byte("two"), []byte("lost")})
	if err == nil {
		t.Fatal("a lost phrase was enrolled around")
	}
	if !strings.Contains(err.Error(), "utterance 2") {
		t.Errorf("error does not say which phrase: %v", err)
	}
	if len(ids.People) != 0 {
		t.Error("a partial enrollment was kept")
	}
}

func TestEnrollAudioRefusesTheWrongEmbedder(t *testing.T) {
	other := newFake()
	other.model = "other"
	ids := &identity.Identities{Model: "fake", Dim: dim}
	if err := ids.EnrollAudio(context.Background(), other, "cass", "", nil); err == nil {
		t.Error("a household took an enrollment from a different model")
	}
}
