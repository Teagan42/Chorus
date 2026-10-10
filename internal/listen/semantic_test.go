package listen_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/listen"
)

// ms is that much device audio, in bytes.
func ms(n int) int { return n * 32 }

// judgement is one scripted answer from the judge.
type judgement struct {
	done bool
	err  error
}

// judge answers from a script and keeps every turn it was sent, with the
// context it was asked under.
type judge struct {
	mu      sync.Mutex
	answers []judgement
	asked   [][]byte
	ctxs    []context.Context
}

func (j *judge) Complete(ctx context.Context, pcm []byte) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.asked = append(j.asked, pcm)
	j.ctxs = append(j.ctxs, ctx)
	if len(j.answers) == 0 {
		return false, errors.New("smartturn judge: no answer scripted")
	}
	a := j.answers[0]
	j.answers = j.answers[1:]
	return a.done, a.err
}

func (j *judge) asks() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.asked)
}

// later holds asks until the test runs them, so when a verdict lands is the
// test's choice and not the scheduler's. Under a listener, Go is called on
// the read loop.
type later struct {
	mu     sync.Mutex
	queued []func()
}

func (l *later) Go(f func()) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.queued = append(l.queued, f)
	return true
}

func (l *later) take() []func() {
	l.mu.Lock()
	defer l.mu.Unlock()
	q := l.queued
	l.queued = nil
	return q
}

func (l *later) run() {
	for _, f := range l.take() {
		f()
	}
}

// semantic is the defaults around a judge, with asks held by the test.
func semantic(j *judge, logs *bytes.Buffer) (*listen.Semantic, *later) {
	q := &later{}
	ep := listen.NewSemantic(j)
	ep.Go = q.Go
	if logs != nil {
		ep.Log = slog.New(slog.NewTextHandler(logs, nil))
	}
	return ep, q
}

// feed streams n bytes of one kind in uplink chunks and returns how many
// bytes went in before End, or -1 when it never came.
func feed(t *testing.T, ep listen.Endpointer, amplitude int16, n int) int {
	t.Helper()
	for sent := 0; sent < n; sent += chunkBytes {
		c := min(chunkBytes, n-sent)
		if ep.Feed(voice(amplitude, c)) == listen.End {
			return sent + c
		}
	}
	return -1
}

// speakTo starts a turn: its first chunk is a Start, the rest continue it.
func speakTo(t *testing.T, ep listen.Endpointer, amplitude int16, n int) {
	t.Helper()
	if got := ep.Feed(voice(amplitude, chunkBytes)); got != listen.Start {
		t.Fatalf("first voiced chunk = %v, want Start", got)
	}
	if feed(t, ep, amplitude, n-chunkBytes) != -1 {
		t.Fatal("the turn ended while Alan was still talking")
	}
}

// pauseChunks is how many chunks of quiet reach the default pause.
var pauseChunks = (listen.DefaultPause + chunkBytes - 1) / chunkBytes

// Alan says "turn off the kitchen lights" and stops. The judge hears it as a
// whole turn, so it ends a chunk after the 200 ms pause, not 800 ms later.
//
// verifies SPEC §4.5
func TestSemanticEndsAFinishedTurnAfterAShortPause(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	ep, q := semantic(j, nil)

	speakTo(t, ep, 8000, ms(1900))
	if got := feed(t, ep, 0, pauseChunks*chunkBytes); got != -1 {
		t.Fatalf("ended %d bytes into the pause, before anyone judged it", got)
	}
	if j.asks() != 0 || len(q.queued) != 1 {
		t.Fatalf("%d asks queued after the pause, want one, unrun", len(q.queued))
	}
	q.run()
	if got := feed(t, ep, 0, chunkBytes); got != chunkBytes {
		t.Fatalf("a finished turn did not end on the next chunk (got %d)", got)
	}
	want := (pauseChunks + 1) * chunkBytes
	if got := ep.Trailing(); got != want {
		t.Errorf("Trailing = %d bytes, want the %d of quiet before the End", got, want)
	}
	// The judge heard the words and the pause after them, nothing before.
	if got, want := len(j.asked[0]), ms(1900)+pauseChunks*chunkBytes; got != want {
		t.Errorf("judge was sent %d bytes, want the %d of the turn so far", got, want)
	}
}

// "Turn off the... uh..." is not over. The judge says so, the hesitation runs
// past the 800 ms Energy would have cut at, and "kitchen lights" lands in the
// same turn, which the judge then hears whole.
//
// verifies SPEC §4.5
func TestSemanticHoldsAnUnfinishedTurnThroughAHesitation(t *testing.T) {
	j := &judge{answers: []judgement{{done: false}, {done: true}}}
	ep, q := semantic(j, nil)

	speakTo(t, ep, 8000, ms(800))
	feed(t, ep, 0, pauseChunks*chunkBytes)
	q.run()
	if got := feed(t, ep, 0, ms(1200)); got != -1 {
		t.Fatalf("an unfinished turn ended %d bytes into a 1.2 s hesitation", got)
	}
	if got := feed(t, ep, 8000, ms(900)); got != -1 {
		t.Fatal("speech after the hesitation ended the turn")
	}
	feed(t, ep, 0, pauseChunks*chunkBytes)
	q.run()
	if got := feed(t, ep, 0, chunkBytes); got != chunkBytes {
		t.Fatal("the finished turn did not end after its second pause")
	}

	if len(j.asked) != 2 {
		t.Fatalf("judge asked %d times, want once per pause", len(j.asked))
	}
	first, second := j.asked[0], j.asked[1]
	if !bytes.HasPrefix(second, first) || len(second) <= len(first) {
		t.Errorf("the second ask (%d bytes) is not the whole turn after the first (%d)", len(second), len(first))
	}
}

// The judge can be wrong the other way: Teagan finished "set the thermostat
// to sixty eight" and it said unfinished. The turn still ends, at Hold.
//
// verifies SPEC §4.5
func TestSemanticEndsAnUnfinishedTurnAtTheHold(t *testing.T) {
	j := &judge{answers: []judgement{{done: false}}}
	ep, q := semantic(j, nil)

	speakTo(t, ep, 8000, ms(2900))
	feed(t, ep, 0, pauseChunks*chunkBytes)
	q.run()
	if feed(t, ep, 0, listen.DefaultSilence) != -1 {
		t.Fatal("an unfinished turn ended at Energy's silence")
	}
	if feed(t, ep, 0, listen.DefaultHold) == -1 {
		t.Fatal("an unfinished turn was held open past the hold")
	}
	// The chunk that reaches the hold ends it.
	if got := ep.Trailing(); got < listen.DefaultHold || got >= listen.DefaultHold+chunkBytes {
		t.Errorf("Trailing = %d, want the chunk that reached the hold (%d)", got, listen.DefaultHold)
	}
}

// The sidecar is down: every turn ends where Energy would, and the log says
// so once rather than on every pause, then once more when it is back.
//
// verifies SPEC §4.5
func TestSemanticFallsBackToTheSilenceWhenTheJudgeFails(t *testing.T) {
	down := errors.New("smartturn judge: dial tcp 127.0.0.1:8891: connection refused")
	j := &judge{answers: []judgement{{err: down}, {err: down}, {done: true}}}
	var logs bytes.Buffer
	ep, q := semantic(j, &logs)

	for _, line := range []int{ms(1900), ms(1100)} {
		speakTo(t, ep, 8000, line)
		feed(t, ep, 0, pauseChunks*chunkBytes)
		q.run()
		got := feed(t, ep, 0, listen.DefaultSilence)
		if want := listen.DefaultSilence - pauseChunks*chunkBytes; got != want {
			t.Fatalf("ended %d bytes after the ask failed, want at the silence (%d)", got, want)
		}
	}
	if n := strings.Count(logs.String(), "semantic endpointing unavailable"); n != 1 {
		t.Errorf("logged the outage %d times over two turns, want once:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("the log does not say why:\n%s", logs.String())
	}

	speakTo(t, ep, 8000, ms(1300))
	feed(t, ep, 0, pauseChunks*chunkBytes)
	q.run()
	if feed(t, ep, 0, chunkBytes) != chunkBytes {
		t.Error("a recovered judge was not listened to")
	}
	if !strings.Contains(logs.String(), "semantic endpointing answering again") {
		t.Errorf("the recovery was not logged:\n%s", logs.String())
	}
}

// A judge slower than the silence is not waited for: the turn ends at 800 ms,
// its ask is cancelled, and when it finally answers nothing listens.
//
// verifies SPEC §4.5
func TestSemanticDoesNotWaitForASlowJudge(t *testing.T) {
	j := &judge{answers: []judgement{{done: false}, {done: true}}}
	ep, q := semantic(j, nil)

	speakTo(t, ep, 8000, ms(1900))
	got := feed(t, ep, 0, listen.DefaultSilence)
	if got != listen.DefaultSilence {
		t.Fatalf("ended %d bytes in with no verdict, want at the silence", got)
	}
	q.run()
	if err := j.ctxs[0].Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("the slow ask's context is %v, want cancelled with its turn", err)
	}

	// Its "unfinished" must not hold the next turn open.
	speakTo(t, ep, 8000, ms(900))
	feed(t, ep, 0, pauseChunks*chunkBytes)
	q.run()
	if feed(t, ep, 0, chunkBytes) != chunkBytes {
		t.Error("the next turn was judged by the last one's late verdict")
	}
}

// wall is the clock the judge answers on, moved only by the test. Audio the
// test feeds does not move it: that is a burst, audio arriving faster than
// it was spoken.
type wall struct {
	mu  sync.Mutex
	now time.Time
}

func (w *wall) Now() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.now
}

func (w *wall) advance(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.now = w.now.Add(d)
}

// afterAStall is the endpointer with its asks held by the test and the clock
// stopped, the morning after the kitchen satellite's radio stalled.
func afterAStall(j *judge) (*listen.Semantic, *later, *wall) {
	ep, q := semantic(j, nil)
	clk := &wall{now: time.Date(2025, 10, 9, 7, 42, 0, 0, time.UTC)}
	ep.Clock = clk
	return ep, q, clk
}

// The kitchen satellite's radio stalls while Alan finishes "turn off the
// kitchen lights", then sends the quiet after it in one burst. The audio
// says 800 ms of silence went by; the clock says Smart Turn has had no time
// at all. The turn waits for its verdict, then ends on the next chunk, as it
// would have in real time.
//
// verifies SPEC §4.5
func TestSemanticWaitsOutABurstForTheJudge(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	ep, q, _ := afterAStall(j)

	speakTo(t, ep, 8000, ms(1900))
	if got := feed(t, ep, 0, listen.DefaultSilence+ms(300)); got != -1 {
		t.Fatalf("a burst of quiet ended the turn %d bytes in, before the judge could answer", got)
	}
	q.run()
	if got := feed(t, ep, 0, chunkBytes); got != chunkBytes {
		t.Fatal("the finished turn did not end on the chunk after its verdict")
	}
}

// The same burst, and the judge's time runs out by the clock with no verdict:
// the turn ends on the next chunk, where Energy's silence was long passed.
//
// verifies SPEC §4.5
func TestSemanticStopsWaitingWhenTheJudgesTimeIsUp(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	ep, _, clk := afterAStall(j)

	speakTo(t, ep, 8000, ms(1900))
	if got := feed(t, ep, 0, listen.DefaultSilence); got != -1 {
		t.Fatalf("a burst of quiet ended the turn %d bytes in, before the judge could answer", got)
	}
	// Energy's 800 ms less the 200 ms pause the judge was asked after.
	clk.advance(600 * time.Millisecond)
	if got := feed(t, ep, 0, chunkBytes); got != chunkBytes {
		t.Fatal("a judge out of time still held the turn")
	}
}

// A burst is no licence to wait forever: with the clock stopped and no
// verdict, the turn ends at the hold, as an unfinished one would.
//
// verifies SPEC §4.5
func TestSemanticEndsABurstAtTheHold(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	ep, _, _ := afterAStall(j)

	speakTo(t, ep, 8000, ms(1900))
	got := feed(t, ep, 0, 2*listen.DefaultHold)
	if got == -1 {
		t.Fatal("a turn with no verdict was held past the hold")
	}
	if tr := ep.Trailing(); tr < listen.DefaultHold || tr >= listen.DefaultHold+chunkBytes {
		t.Errorf("Trailing = %d, want the chunk that reached the hold (%d)", tr, listen.DefaultHold)
	}
}

// Alan pauses, then keeps talking before the verdict lands. That verdict was
// about a pause that is over: the ask is dropped and its "finished" ignored.
//
// verifies SPEC §4.5
func TestSemanticDropsAVerdictOnAPauseThatEnded(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	ep, q := semantic(j, nil)

	speakTo(t, ep, 8000, ms(700))
	feed(t, ep, 0, pauseChunks*chunkBytes)
	feed(t, ep, 8000, ms(600))
	q.run()
	if err := j.ctxs[0].Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("the ask's context is %v after speech resumed, want cancelled", err)
	}
	if got := feed(t, ep, 0, chunkBytes); got != -1 {
		t.Error("a verdict on an earlier pause ended the turn")
	}
}

// A long turn sends the judge its last 8 s, which ends with the pause.
//
// verifies SPEC §4.5
func TestSemanticSendsTheJudgeTheEndOfALongTurn(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	ep, q := semantic(j, nil)

	speakTo(t, ep, 7000, ms(2000))
	feed(t, ep, 9000, ms(8000))
	feed(t, ep, 0, pauseChunks*chunkBytes)
	q.run()

	sent := j.asked[0]
	if len(sent) != listen.DefaultWindow {
		t.Fatalf("judge was sent %d bytes, want the %d-byte window", len(sent), listen.DefaultWindow)
	}
	if !bytes.HasSuffix(sent, quiet(pauseChunks*chunkBytes)) || bytes.Contains(sent, voice(7000, 2)) {
		t.Error("the window is not the turn's last 8 s")
	}
}

// Teagan starts softly, "um, turn on the porch light": the onset under the
// threshold reaches the judge as it reaches STT, or the judge hears half a
// sentence. Quiet from before the lead-in does not.
//
// verifies SPEC §4.5
func TestSemanticSendsTheJudgeTheOnsetTheThresholdMissed(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	ep, q := semantic(j, nil)

	feed(t, ep, 0, ms(1000))
	feed(t, ep, 200, listen.DefaultLeadIn)
	speakTo(t, ep, 8000, ms(1600))
	feed(t, ep, 0, pauseChunks*chunkBytes)
	q.run()

	sent := j.asked[0]
	if want := listen.DefaultLeadIn + ms(1600) + pauseChunks*chunkBytes; len(sent) != want {
		t.Fatalf("judge was sent %d bytes, want the %d of lead-in, turn and pause", len(sent), want)
	}
	if !bytes.HasPrefix(sent, voice(200, listen.DefaultLeadIn)) {
		t.Error("the soft onset is not where the judge's audio starts")
	}
}

// A mute forgets the turn and the ask about it.
func TestSemanticResetForgetsTheTurnAndItsAsk(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}}}
	ep, q := semantic(j, nil)

	speakTo(t, ep, 8000, ms(900))
	feed(t, ep, 0, pauseChunks*chunkBytes)
	ep.Reset()
	q.run()
	if err := j.ctxs[0].Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("the ask's context is %v after a reset, want cancelled", err)
	}
	if got := ep.Feed(quiet(chunkBytes)); got != listen.Continue {
		t.Errorf("quiet after a reset = %v, want Continue", got)
	}
	if got := ep.Feed(voice(8000, chunkBytes)); got != listen.Start {
		t.Errorf("speech after a reset = %v, want a fresh Start", got)
	}
}

// An ask the listener refuses, because the link is closing, is a failed one.
func TestSemanticTreatsARefusedAskAsNoAnswer(t *testing.T) {
	j := &judge{}
	ep := listen.NewSemantic(j)
	ep.Go = func(func()) bool { return false }

	speakTo(t, ep, 8000, ms(1900))
	if got := feed(t, ep, 0, listen.DefaultSilence); got != listen.DefaultSilence {
		t.Errorf("ended %d bytes in, want at the silence", got)
	}
	if j.asks() != 0 {
		t.Error("the judge was asked anyway")
	}
}

func TestSemanticDefaults(t *testing.T) {
	ep := listen.NewSemantic(&judge{})
	switch {
	case ep.Threshold != listen.DefaultSpeechEnergy:
		t.Errorf("Threshold = %v", ep.Threshold)
	case ep.Pause != 6400 || ep.Silence != listen.DefaultSilence || ep.Hold != 64000 || ep.Window != 256000:
		t.Errorf("Pause %d, Silence %d, Hold %d, Window %d: want 200 ms, Energy's, 2 s and 8 s",
			ep.Pause, ep.Silence, ep.Hold, ep.Window)
	case ep.LeadIn != listen.DefaultLeadIn:
		t.Errorf("LeadIn = %d, want the listener's %d", ep.LeadIn, listen.DefaultLeadIn)
	}
	var _ listen.Trailer = ep
}
