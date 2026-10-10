package memory

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"
)

// Embedder turns text into vectors whose cosine says how alike in meaning
// two texts are. Recall uses one to choose, from more memories than a turn is
// told, the ones about what was just said (SPEC §5, ADR-0044).
type Embedder interface {
	// Embed returns one vector per text, in order.
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// EmbedModel names the model, which the log records against what it
	// chose: a different model chooses differently.
	EmbedModel() string
}

// DefaultRankTimeout bounds ranking on a turn's way to the model. Usually it
// is one short query to embed. The first recall after a restart also embeds
// what is kept, which carries on past the bound so the next turn has it
// (ADR-0044).
const DefaultRankTimeout = 500 * time.Millisecond

// warmTimeout bounds embedding what is kept, which no turn waits for past
// its own bound.
const warmTimeout = time.Minute

// RecallConfig is how the Recaller chooses.
type RecallConfig struct {
	// Embedder ranks by relevance when there are more memories or
	// conversations than a turn is told. Nil gives the newest.
	Embedder Embedder

	// Timeout defaults to DefaultRankTimeout. Ranking that runs out of time
	// or fails gives the newest, as no Embedder does.
	Timeout time.Duration

	// Failed hears why ranking gave up, for the daemon to log. Nil ignores
	// it: the recollection's empty RankedBy already says it happened.
	Failed func(person string, err error)
}

// What ranking chooses from: everything a person has kept, up to a bound
// no household reaches by speaking.
const (
	rankPool  = 500
	embedding = 32
	// fusion is reciprocal rank fusion's constant, the published default:
	// large enough that neither recency nor relevance alone decides.
	fusion = 60
	// maxCached bounds the vectors kept between turns; a household's
	// memories and a month of conversations are a few hundred.
	maxCached = 4096
)

// vectors remembers what each text embedded to, so a turn embeds only what it
// has not seen: the words just said, and anything remembered since.
type vectors struct {
	mu    sync.Mutex
	model string
	byKey map[string][]float32
	// warming is the embedding of kept texts in flight, if one is: a turn
	// that finds one waits for it rather than embedding the same again.
	warming *warming
}

type warming struct {
	done chan struct{}
	err  error
}

// warm embeds texts in the background, unless a warm is already in flight,
// and returns whichever is.
func (v *vectors) warm(texts []string, embed func([]string) error) *warming {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.warming != nil {
		return v.warming
	}
	w := &warming{done: make(chan struct{})}
	v.warming = w
	go func() {
		w.err = embed(texts)
		v.mu.Lock()
		v.warming = nil
		v.mu.Unlock()
		close(w.done)
	}()
	return w
}

func (v *vectors) get(model string, texts []string) ([][]float32, []int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.model != model {
		v.model, v.byKey = model, nil
	}
	out := make([][]float32, len(texts))
	var missing []int
	for i, t := range texts {
		if vec, ok := v.byKey[t]; ok {
			out[i] = vec
		} else {
			missing = append(missing, i)
		}
	}
	return out, missing
}

func (v *vectors) put(model string, texts []string, vecs [][]float32) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.model != model {
		return
	}
	if v.byKey == nil || len(v.byKey)+len(texts) > maxCached {
		v.byKey = map[string][]float32{}
	}
	for i, t := range texts {
		v.byKey[t] = vecs[i]
	}
}

// ranker embeds what recall chooses between, and the words it is chosen for.
type ranker struct {
	e     Embedder
	cache *vectors
}

// vectors returns the words' vector and each text's, embedding the texts
// not yet seen and the words, which change every turn and are never kept.
func (r ranker) vectors(ctx context.Context, words string, texts []string) ([]float32, [][]float32, error) {
	vecs, err := r.kept(ctx, texts)
	if err != nil {
		return nil, nil, err
	}
	q, err := r.embed(ctx, []string{words})
	if err != nil {
		return nil, nil, err
	}
	return q[0], vecs, nil
}

// kept returns the vectors of what is kept, embedding any not yet seen. The
// embedding runs on past ctx, so a turn that cannot wait for it still leaves
// it done for the next.
func (r ranker) kept(ctx context.Context, texts []string) ([][]float32, error) {
	model := r.e.EmbedModel()
	// Twice: the first wait may be on another turn's warm, for other texts.
	for range 2 {
		vecs, missing := r.cache.get(model, texts)
		if len(missing) == 0 {
			return vecs, nil
		}
		w := r.cache.warm(pick(texts, missing), func(in []string) error {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), warmTimeout)
			defer cancel()
			for start := 0; start < len(in); start += embedding {
				batch := in[start:min(start+embedding, len(in))]
				got, err := r.embed(ctx, batch)
				if err != nil {
					return err
				}
				r.cache.put(model, batch, got)
			}
			return nil
		})
		select {
		case <-w.done:
			if w.err != nil {
				return nil, w.err
			}
		case <-ctx.Done():
			return nil, fmt.Errorf("embedding what is kept: %w", ctx.Err())
		}
	}
	vecs, missing := r.cache.get(model, texts)
	if len(missing) > 0 {
		return nil, fmt.Errorf("%d kept texts still not embedded", len(missing))
	}
	return vecs, nil
}

func (r ranker) embed(ctx context.Context, texts []string) ([][]float32, error) {
	got, err := r.e.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	if len(got) != len(texts) {
		return nil, fmt.Errorf("embedded %d of %d", len(got), len(texts))
	}
	for i, v := range got {
		if n := norm(v); n == 0 || math.IsNaN(n) {
			return nil, fmt.Errorf("embedding %d is not a direction", i)
		}
	}
	return got, nil
}

// fuse ranks each candidate by recency (its index, newest first) and by
// cosine to q, adds the reciprocals of the two ranks, and keeps the limit
// best, returned in recency order. Neither rank needs a threshold that would
// have to be tuned per embedding model: yesterday's conversation stays near
// the top on recency alone, and a month-old one about the garage climbs
// when the garage is what was asked about.
func fuse(q []float32, vecs [][]float32, limit int) []int {
	sim := make([]float64, len(vecs))
	for i, v := range vecs {
		sim[i] = cosine(q, v)
	}
	byRelevance := make([]int, len(vecs))
	for i := range byRelevance {
		byRelevance[i] = i
	}
	slices.SortStableFunc(byRelevance, func(a, b int) int { return cmp.Compare(sim[b], sim[a]) })
	score := make([]float64, len(vecs))
	for rank, i := range byRelevance {
		score[i] = 1/float64(fusion+i+1) + 1/float64(fusion+rank+1)
	}
	order := make([]int, len(vecs))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(score[b], score[a]) })
	chosen := order[:limit]
	slices.Sort(chosen)
	return chosen
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return math.Inf(-1)
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot / (norm(a) * norm(b))
}

func norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}
