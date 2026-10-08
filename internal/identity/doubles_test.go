package identity_test

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"

	"github.com/teaganglenn/chorus/internal/identity"
)

// dim is small so a test can read a vector. The package never assumes 192.
const dim = 4

// axis is a unit vector along one dimension: two of them are orthogonal, which
// is the cleanest "different voice" a test can draw.
func axis(i int) []float32 {
	v := make([]float32, dim)
	v[i] = 1
	return v
}

// noisy perturbs v by eps per dimension, deterministically: the same voice on
// a different day.
func noisy(v []float32, eps float64, seed uint64) []float32 {
	r := rand.New(rand.NewPCG(seed, seed))
	out := make([]float32, len(v))
	for i := range v {
		out[i] = v[i] + float32((2*r.Float64()-1)*eps)
	}
	return out
}

// between is the unit vector a fraction t of the way from a to b.
func between(a, b []float32, t float64) []float32 {
	out := make([]float32, len(a))
	var sum float64
	for i := range a {
		out[i] = float32((1-t)*float64(a[i]) + t*float64(b[i]))
		sum += float64(out[i]) * float64(out[i])
	}
	n := float32(math.Sqrt(sum))
	for i := range out {
		out[i] /= n
	}
	return out
}

func scale(v []float32, k float32) []float32 {
	out := make([]float32, len(v))
	for i := range v {
		out[i] = v[i] * k
	}
	return out
}

func norm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}

// fakeEmbedder maps audio to the vector a test chose for it, so the matcher
// is exercised without a model. An unknown utterance is an error, which is
// what a sidecar that is down looks like.
type fakeEmbedder struct {
	model string
	dim   int
	by    map[string][]float32
	calls int
}

func newFake() *fakeEmbedder {
	return &fakeEmbedder{model: "fake", dim: dim, by: map[string][]float32{}}
}

func (f *fakeEmbedder) Embed(_ context.Context, pcm []byte) ([]float32, error) {
	f.calls++
	v, ok := f.by[string(pcm)]
	if !ok {
		return nil, errors.New("sidecar unreachable")
	}
	return v, nil
}

func (f *fakeEmbedder) Model() string { return f.model }
func (f *fakeEmbedder) Dim() int      { return f.dim }

// household enrolls alan on axis 0 and beth on axis 1, each from three
// slightly different takes.
func household() *identity.Identities {
	ids := &identity.Identities{Model: "fake", Dim: dim}
	must(ids.Enroll("alan", "Alan", [][]float32{axis(0), noisy(axis(0), 0.05, 1), noisy(axis(0), 0.05, 2)}))
	must(ids.Enroll("beth", "Beth", [][]float32{axis(1), noisy(axis(1), 0.05, 3), noisy(axis(1), 0.05, 4)}))
	return ids
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
