package listen

import (
	"context"
	"strings"
	"sync"
	"unicode"

	"github.com/teagan42/chorus/internal/stt"
)

// Dangling is a Judge that will not call a turn finished while its words end
// on one no command ends on. Smart Turn hears "set a timer for", said on a
// falling pitch, as done; the words say the duration is still coming
// (ADR-0042).
type Dangling struct {
	Judge Judge

	// Words decodes the same audio the judge hears, alongside it.
	Words stt.Transcriber
}

var _ Judge = Dangling{}

// Complete is the judge's verdict, overruled only from finished to
// unfinished. A failed decode leaves the verdict as the judge gave it.
func (d Dangling) Complete(ctx context.Context, pcm []byte) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	// Deferred in this order so the decode is cancelled, then waited for.
	defer wg.Wait()
	defer cancel()

	decoded := make(chan stt.Result, 1)
	wg.Go(func() {
		r, err := d.Words.Transcribe(ctx, pcm)
		if err == nil {
			decoded <- r
		}
		close(decoded)
	})

	done, err := d.Judge.Complete(ctx, pcm)
	if err != nil || !done {
		return done, err
	}
	select {
	case r, ok := <-decoded:
		if !ok {
			// A decode that gave up on a dropped ask did not fail: the ask
			// gets its cancellation, not the judge's verdict.
			return ctx.Err() == nil, ctx.Err()
		}
		return !Dangles(r.Text), nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// Dangles reports whether a transcript stops on a word that leaves the command
// unsaid: an article, a conjunction, a preposition with no object, a filler.
// Only words that cannot end a household command count; a wrong yes costs
// the person the hold.
func Dangles(text string) bool {
	words := strings.FieldsFunc(strings.ToLower(strings.ReplaceAll(text, "’", "'")), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
	if len(words) == 0 {
		return false
	}
	last, before := words[len(words)-1], words[:len(words)-1]
	switch {
	case filler(last), unended(last):
		return true
	case last == "then":
		return len(before) > 0 && conjunction(before[len(before)-1])
	case particle(last):
		return !phrasal(before)
	}
	return false
}

// filler is a hesitation: whoever said it has more to say.
func filler(w string) bool {
	switch w {
	case "uh", "uhh", "um", "umm", "uhm", "er", "erm", "hmm", "hm":
		return true
	}
	return false
}

// unended is a word a command cannot stop on: what it introduces is missing.
// "that", "this", "about" and "her" are left out; they end sentences daily.
func unended(w string) bool {
	switch w {
	case "the", "a", "an", "my", "your", "our", "their", "its", "every", "each", "another",
		"for", "to", "of", "with", "at", "from", "into", "onto", "by", "as", "via", "per",
		"until", "till", "til", "toward", "towards", "between", "during", "without":
		return true
	}
	return conjunction(w)
}

func conjunction(w string) bool {
	switch w {
	case "and", "or", "but", "nor", "because", "cause", "if", "unless", "than", "whether":
		return true
	}
	return false
}

// particle is a preposition that also finishes a phrasal verb: "turn it on"
// is whole, "what's the weather on" is not.
func particle(w string) bool {
	switch w {
	case "on", "off", "up", "down", "in", "out", "over", "back", "around", "away":
		return true
	}
	return false
}

// phrasal reports whether the clause before a particle gives it something to
// finish: a verb that takes it and has not taken one already ("turn on the
// lights in" has), a copula opening a question about state ("is the oven
// on"), or a copula right before it ("what's on").
func phrasal(before []string) bool {
	clause := before
	for i := len(before) - 1; i >= 0; i-- {
		if conjunction(before[i]) {
			clause = before[i+1:]
			break
		}
	}
	if len(clause) == 0 {
		return false
	}
	prev := clause[len(clause)-1]
	switch {
	case particle(prev):
		return false
	case copula(prev), strings.HasSuffix(prev, "'s"), copula(clause[0]):
		return true
	}
	for i, w := range clause[:len(clause)-1] {
		if takesParticle(w) && !particle(clause[i+1]) {
			return true
		}
	}
	return false
}

// copula opens a question about state, or states it: "is the oven", "it's".
// Other auxiliaries open requests ("can you play music in"), not states.
func copula(w string) bool {
	switch w {
	case "is", "are", "was", "were", "am", "isn't", "aren't", "wasn't", "weren't":
		return true
	}
	return false
}

// takesParticle is a verb a household finishes with on, off, up or the like,
// in the forms a command or a question about it uses. Verbs as often followed
// by a place or a time ("set an alarm on") are left out.
func takesParticle(w string) bool {
	switch w {
	case "turn", "turned", "switch", "switched", "put", "leave", "left", "keep", "kept",
		"shut", "flip", "plug", "plugged", "lock", "locked", "wake", "warm", "heat", "cool",
		"power", "powered", "start", "log", "sign", "hang", "come", "go", "get", "take",
		"bring", "pull", "dim", "speed", "slow", "open", "close", "fill", "top", "clean",
		"pick", "write", "back":
		return true
	}
	return false
}
