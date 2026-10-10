package listen_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/stt"
)

// What a household says to a satellite, and whether the words alone show the
// command is not over. The first two blocks are sidecars/smartturn/corpus.py's
// takes, as Parakeet writes them; Smart Turn called the cut-off "set a timer
// for", "what's the weather on" and "remind me to call my mom and" finished
// (ADR-0036).
//
// verifies SPEC §4.5
func TestDanglesOnAWordNoCommandEndsOn(t *testing.T) {
	cases := []struct {
		said string
		want bool
	}{
		// corpus.py COMPLETE: the words never overrule a finished verdict.
		{"Turn off the kitchen lights.", false},
		{"Set the thermostat to sixty eight degrees.", false},
		{"What's the weather on Saturday?", false},
		{"Is the garage door closed?", false},
		{"Set a timer for twelve minutes.", false},
		{"Play something by Led Zeppelin.", false},
		{"Turn on the porch light.", false},
		{"Remind me to call my mom at six.", false},
		{"Lock the front door.", false},
		{"Add oat milk and coffee filters to the shopping list.", false},
		{"Good night.", false},
		{"How long is left on the dishwasher?", false},

		// corpus.py INCOMPLETE, comma and all.
		{"Turn off the,", true},
		{"Set the thermostat to,", true},
		{"What's the weather on,", true},
		{"Set a timer for,", true},
		{"Remind me to call my mom and,", true},
		{"Turn off the, uh,", true},
		{"Play something by, um,", true},
		{"Add oat milk and,", true},
		{"Turn on the porch light and the,", true},
		{"Is the garage door, uh,", true},

		// Parakeet often drops the trailing comma, and the case.
		{"set a timer for", true},
		{"what's the weather in", true},
		{"Remind me to", true},
		{"Turn the hallway lights to fifty percent and then", true},
		{"Add coffee filters to my", true},
		{"Play the next episode of", true},
		{"Turn off the lights in the office or", true},
		{"Set an alarm on", true},
		{"Turn on the porch light and the weather in", true},

		// A particle the verb already took leaves the last one with nothing
		// to finish, and a request is not a question about state.
		{"Turn on the lights in", true},
		{"Switch off the TV in", true},
		{"Turn the lights on in", true},
		{"Are the lights on in", true},
		{"Can you play music in", true},
		{"Could you put the jazz playlist on in", true},
		{"Text Alice that I'm running late because", true},

		// A particle ends a phrasal command, and a question about state.
		{"Turn the kitchen lights on.", false},
		{"Turn it off.", false},
		{"Turn the volume down", false},
		{"Leave the porch light on", false},
		{"Wake me up", false},
		{"Is the oven on?", false},
		{"Are the bedroom lights still on?", false},
		{"Did I leave the stove on?", false},
		{"Could you turn the hallway lights off?", false},
		{"Will you put the kettle on", false},
		{"Turn the lights in the kitchen on", false},
		{"Turn the back porch lights off", false},
		{"What's on?", false},
		{"It's on", false},

		// Words that look like function words but end commands every day.
		{"Turn that off and do this", false},
		{"What's that?", false},
		{"Call her", false},
		{"What's the movie about?", false},
		{"Then what?", false},
		{"Yes, do it then.", false},

		// Nothing decoded is nothing to overrule.
		{"", false},
		{"   ", false},
		{"...", false},
	}
	for _, c := range cases {
		if got := listen.Dangles(c.said); got != c.want {
			t.Errorf("Dangles(%q) = %v, want %v", c.said, got, c.want)
		}
	}
}

// Curly apostrophes are how some decoders write a contraction.
func TestDanglesReadsACurlyApostrophe(t *testing.T) {
	if !listen.Dangles("What’s the weather on") {
		t.Error("a curly apostrophe hid the dangling preposition")
	}
	if listen.Dangles("What’s on") {
		t.Error("“what’s on” was read as cut off")
	}
}

// words is a transcriber that decodes every turn to the same text, or fails,
// and can hold its answer until the test lets it go.
type words struct {
	text string
	err  error

	// heard closes on the first decode; release, when set, is what it waits for.
	heard   chan struct{}
	release chan struct{}

	// gaveUp, when set, closes once a decode returns on its cancellation.
	gaveUp chan struct{}

	mu       sync.Mutex
	asked    [][]byte
	returned bool
	once     sync.Once
}

func newWords(text string) *words { return &words{text: text, heard: make(chan struct{})} }

func (w *words) Transcribe(ctx context.Context, pcm []byte) (stt.Result, error) {
	w.mu.Lock()
	w.asked = append(w.asked, pcm)
	w.mu.Unlock()
	w.once.Do(func() { close(w.heard) })
	defer func() {
		w.mu.Lock()
		w.returned = true
		w.mu.Unlock()
	}()
	if w.release != nil {
		select {
		case <-w.release:
		case <-ctx.Done():
			if w.gaveUp != nil {
				defer close(w.gaveUp)
			}
			return stt.Result{}, ctx.Err()
		}
	}
	return stt.Result{Text: w.text}, w.err
}

func (w *words) done() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.returned
}

// Teagan says "set a timer for" and trails off on a falling pitch. Smart Turn
// says finished; the words say the duration is still coming.
//
// verifies SPEC §4.5
func TestDanglingOverrulesAFinishedVerdictOnACutOffCommand(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	w := newWords("Set a timer for")
	turn := voice(8000, ms(1200))

	done, err := listen.Dangling{Judge: j, Words: w}.Complete(context.Background(), turn)
	if err != nil || done {
		t.Fatalf("Complete = %v, %v; want unfinished", done, err)
	}
	if len(w.asked) != 1 || len(w.asked[0]) != len(turn) || len(j.asked) != 1 {
		t.Errorf("decoded %d and judged %d turns; want the same audio once each", len(w.asked), len(j.asked))
	}
}

// "Set a timer for twelve minutes" is finished, and the words agree.
//
// verifies SPEC §4.5
func TestDanglingKeepsAFinishedVerdictOnAWholeCommand(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	done, err := listen.Dangling{Judge: j, Words: newWords("Set a timer for twelve minutes.")}.
		Complete(context.Background(), voice(8000, ms(1900)))
	if err != nil || !done {
		t.Fatalf("Complete = %v, %v; want finished", done, err)
	}
}

// The words can only hold a turn open, never end one: Smart Turn hearing
// "turn off the, uh," as unfinished stands, and the decode is not waited for.
//
// verifies SPEC §4.5
func TestDanglingNeverEndsATurnTheJudgeHeldOpen(t *testing.T) {
	j := &judge{answers: []judgement{{done: false}}}
	w := newWords("Turn off the kitchen lights.")
	w.release = make(chan struct{})

	done, err := listen.Dangling{Judge: j, Words: w}.Complete(context.Background(), voice(8000, ms(900)))
	if err != nil || done {
		t.Fatalf("Complete = %v, %v; want the judge's unfinished", done, err)
	}
	if !w.done() {
		t.Error("the decode outlived the ask it was for")
	}
}

// The sidecar is down: the error is the endpointer's to fall back on, as it
// was without the words (ADR-0036).
//
// verifies SPEC §4.5
func TestDanglingPassesOnAJudgeThatFailed(t *testing.T) {
	down := errors.New("smartturn judge: dial tcp 127.0.0.1:8891: connection refused")
	j := &judge{answers: []judgement{{err: down}}}
	_, err := listen.Dangling{Judge: j, Words: newWords("Set a timer for")}.
		Complete(context.Background(), voice(8000, ms(900)))
	if !errors.Is(err, down) {
		t.Errorf("err = %v, want the judge's", err)
	}
}

// Speech recognition is down: Smart Turn's verdict stands, as it did before
// anyone read the words.
//
// verifies SPEC §4.5
func TestDanglingTrustsTheJudgeWhenTheWordsFail(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	w := newWords("")
	w.err = errors.New("transcribe istupakov/parakeet-tdt-0.6b-v2-onnx: 503 Service Unavailable")
	done, err := listen.Dangling{Judge: j, Words: w}.Complete(context.Background(), voice(8000, ms(900)))
	if err != nil || !done {
		t.Errorf("Complete = %v, %v; want the judge's finished", done, err)
	}
}

// Smart Turn and the decode run side by side, so a finished turn waits on
// the slower of the two, not both. Neither answers until the other has been
// asked: one after the other would never return.
//
// verifies SPEC §4.5, §11
func TestDanglingDecodesWhileTheJudgeListens(t *testing.T) {
	w := newWords("Lock the front door.")
	w.release = make(chan struct{})
	j := &rendezvous{words: w}

	done, err := listen.Dangling{Judge: j, Words: w}.Complete(context.Background(), voice(8000, ms(1100)))
	if err != nil || !done {
		t.Fatalf("Complete = %v, %v; want finished", done, err)
	}
}

// rendezvous is a judge that answers "finished" only once the decode is
// running, then lets the decode answer.
type rendezvous struct{ words *words }

func (r *rendezvous) Complete(ctx context.Context, _ []byte) (bool, error) {
	select {
	case <-r.words.heard:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	close(r.words.release)
	return true, nil
}

// An ask the endpointer dropped, because Alan kept talking, drops the decode
// with it and returns no verdict.
//
// verifies SPEC §4.5
func TestDanglingStopsDecodingWhenTheAskIsDropped(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	w := newWords("Turn on the porch light and the")
	w.release = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())

	result := make(chan error, 1)
	go func() {
		_, err := listen.Dangling{Judge: j, Words: w}.Complete(ctx, voice(8000, ms(1500)))
		result <- err
	}()
	<-w.heard
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancellation", err)
	}
	if !w.done() {
		t.Error("the decode outlived the ask it was for")
	}
}

// stubborn is Smart Turn answering "finished" for an ask that was dropped
// while it ran: its reply was already on the wire. It answers only once the
// decode beside it has given up, so the verdict and the cancellation are both
// waiting when Dangling looks.
type stubborn struct{ words *words }

func (j stubborn) Complete(ctx context.Context, _ []byte) (bool, error) {
	<-ctx.Done()
	<-j.words.gaveUp
	return true, nil
}

// Alan keeps talking after "turn on the porch light and the", so the ask is
// dropped mid-decode. A decode that gave up on the cancellation is not a
// failed decode: the ask returns the cancellation, never the judge's
// "finished" for words that were still coming. Either can be the one Dangling
// sees first, so the pause repeats.
//
// verifies SPEC §4.5
func TestDanglingGivesNoVerdictOnAnAskDroppedMidDecode(t *testing.T) {
	for range 50 {
		w := newWords("Turn on the porch light and the")
		w.release, w.gaveUp = make(chan struct{}), make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())

		result := make(chan error, 1)
		go func() {
			_, err := listen.Dangling{Judge: stubborn{w}, Words: w}.Complete(ctx, voice(8000, ms(1500)))
			result <- err
		}()
		<-w.heard
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want the cancellation", err)
		}
	}
}

// Through the endpointer: "set a timer for", a second's thought, "twelve
// minutes". Smart Turn calls the first pause finished; the words hold the turn
// past Energy's 800 ms, and the duration lands in the same turn.
//
// verifies SPEC §4.5
func TestSemanticHoldsACutOffCommandForItsLastWords(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}, {done: true}}}
	w := &script{texts: []string{"Set a timer for", "Set a timer for twelve minutes."}}
	ep, q := semantic(nil, nil)
	ep.Judge = listen.Dangling{Judge: j, Words: w}

	speakTo(t, ep, 8000, ms(900))
	feed(t, ep, 0, pauseChunks*chunkBytes)
	q.run()
	if got := feed(t, ep, 0, ms(1000)); got != -1 {
		t.Fatalf("\"set a timer for\" ended %d bytes into the pause", got)
	}
	if got := feed(t, ep, 8000, ms(800)); got != -1 {
		t.Fatal("the duration ended the turn while it was being said")
	}
	feed(t, ep, 0, pauseChunks*chunkBytes)
	q.run()
	if got := feed(t, ep, 0, chunkBytes); got != chunkBytes {
		t.Fatal("the whole command did not end after its pause")
	}
	if len(j.asked) != 2 || len(w.asked) != 2 {
		t.Errorf("judged %d and decoded %d pauses; want both, twice", len(j.asked), len(w.asked))
	}
}

// Words that mean it is over cannot end a turn sooner than a held one would:
// a cut-off command whose decode is still running at the silence ends there,
// exactly as an unanswered judge does.
//
// verifies SPEC §4.5
func TestSemanticEndsAtTheSilenceWhileTheWordsAreStillDecoding(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	w := newWords("Turn off the")
	w.release = make(chan struct{})
	ep, q := semantic(nil, nil)
	ep.Judge = listen.Dangling{Judge: j, Words: w}

	speakTo(t, ep, 8000, ms(700))
	feed(t, ep, 0, pauseChunks*chunkBytes)
	asks := q.take()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for _, f := range asks {
			f()
		}
	}()
	<-w.heard
	if got := feed(t, ep, 0, listen.DefaultSilence); got == -1 {
		t.Fatal("a turn with no verdict was held past the silence")
	}
	<-finished
	if !w.done() {
		t.Error("the decode outlived the turn it was for")
	}
}

// script decodes each turn to the next scripted text.
type script struct {
	mu    sync.Mutex
	texts []string
	asked [][]byte
}

func (s *script) Transcribe(_ context.Context, pcm []byte) (stt.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, pcm)
	if len(s.texts) == 0 {
		return stt.Result{}, errors.New("transcribe: no text scripted")
	}
	text := s.texts[0]
	s.texts = s.texts[1:]
	return stt.Result{Text: text}, nil
}
