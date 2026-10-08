package identity

import (
	"context"
	"fmt"
)

// MinUtterances is how many guided phrases an enrollment needs. One phrase's
// embedding is that phrase as much as the voice; the mean over several is what
// cancels the phrase out. Three is the floor, not a measured optimum.
const MinUtterances = 3

// Centroid is the L2-normalised mean of a person's utterance embeddings
// (SPEC §5). Normalised so that a cosine against it is a dot product and so
// a person enrolled with ten phrases weighs the same as one enrolled with
// three.
func Centroid(samples [][]float32) ([]float32, error) {
	if len(samples) < MinUtterances {
		return nil, fmt.Errorf("centroid: %d utterances, want at least %d", len(samples), MinUtterances)
	}
	dim := len(samples[0])
	if dim == 0 {
		return nil, fmt.Errorf("centroid: utterance 0 is empty")
	}
	mean := make([]float32, dim)
	for i, s := range samples {
		if len(s) != dim {
			return nil, fmt.Errorf("centroid: utterance %d has %d dims, utterance 0 has %d", i, len(s), dim)
		}
		// Each sample is normalised first so a loud recording does not
		// outvote a quiet one: the model's norm tracks level, not identity.
		u, err := normalise(s)
		if err != nil {
			return nil, fmt.Errorf("centroid: utterance %d: %w", i, err)
		}
		for j := range mean {
			mean[j] += u[j] / float32(len(samples))
		}
	}
	c, err := normalise(mean)
	if err != nil {
		// Only utterances that cancel out reach here, which means they were
		// not one voice.
		return nil, fmt.Errorf("centroid: utterances do not agree on a voice: %w", err)
	}
	return c, nil
}

// Enroll adds a person from embeddings already in hand. The id is what every
// later transcript carries, so it is required and cannot be reused.
func (ids *Identities) Enroll(id, name string, samples [][]float32) error {
	if id == "" {
		return fmt.Errorf("enroll: id is required")
	}
	for _, p := range ids.People {
		if p.ID == id {
			return fmt.Errorf("enroll %s: already enrolled", id)
		}
	}
	c, err := Centroid(samples)
	if err != nil {
		return fmt.Errorf("enroll %s: %w", id, err)
	}
	if len(c) != ids.Dim {
		return fmt.Errorf("enroll %s: embeddings have %d dims, household has %d", id, len(c), ids.Dim)
	}
	ids.People = append(ids.People, Person{ID: id, Name: name, Utterances: len(samples), Centroid: c})
	return nil
}

// EnrollAudio is Enroll from the guided phrases themselves: each utterance is
// embedded, then the centroid is built. The household must have been created
// for this embedder, or the centroid would be scored against a different
// model's geometry forever after.
func (ids *Identities) EnrollAudio(ctx context.Context, emb Embedder, id, name string, utterances [][]byte) error {
	if ids.Model != emb.Model() || ids.Dim != emb.Dim() {
		return fmt.Errorf("enroll %s: household is %s (%d dims) but the embedder is %s (%d dims)",
			id, ids.Model, ids.Dim, emb.Model(), emb.Dim())
	}
	samples := make([][]float32, 0, len(utterances))
	for i, pcm := range utterances {
		e, err := emb.Embed(ctx, pcm)
		if err != nil {
			return fmt.Errorf("enroll %s: embed utterance %d: %w", id, i, err)
		}
		samples = append(samples, e)
	}
	return ids.Enroll(id, name, samples)
}
