package session_test

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

// summarizer writes a fixed summary, or fails, and keeps what it was given.
// A blocking one waits until it is cancelled, as a model that never answers.
type summarizer struct {
	text  string
	err   error
	block bool

	mu    sync.Mutex
	calls []summarized
}

type summarized struct {
	Heard  []string
	People []string
}

func (z *summarizer) Summarize(ctx context.Context, dialogue []journal.Entry, people []string) (string, error) {
	var said []string
	for _, e := range dialogue {
		if e.Kind == journal.EntryHeard {
			said = append(said, e.Text)
		}
	}
	z.mu.Lock()
	z.calls = append(z.calls, summarized{Heard: said, People: slices.Clone(people)})
	z.mu.Unlock()
	if z.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return z.text, z.err
}

func (z *summarizer) asked() []summarized {
	z.mu.Lock()
	defer z.mu.Unlock()
	return slices.Clone(z.calls)
}

// summaryRig is a memory rig whose conversations are summarized when they
// end, with the background work counted so a test can wait for it.
type summaryRig struct {
	*rig
	mem         *remembered
	sum         *summarizer
	summarizing *sync.WaitGroup
}

func newSummaryRig(t *testing.T, sum *summarizer) summaryRig {
	t.Helper()
	steps := []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	mem := &remembered{byPerson: map[string][]journal.Memory{}, summaries: map[string][]journal.Summary{}}
	wg := &sync.WaitGroup{}
	r := newRigWith(t, steps, nil, nil, func(c *session.Config) {
		c.Memories, c.Summarizer, c.Summarizing = mem, sum, wg
	})
	return summaryRig{rig: r, mem: mem, sum: sum, summarizing: wg}
}

// heardFrom runs a turn on an utterance the listener attributed to someone.
func heardFrom(s *session.Session, speaker, text string) <-chan error {
	out := make(chan error, 1)
	go func() {
		out <- s.Heard(context.Background(), session.Transcript{Text: text, SpeakerID: speaker, AudioRef: "blob://mic/1"})
	}()
	return out
}

// drained waits for every summary the supervisor started.
func (r summaryRig) drained(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		r.summarizing.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(patience):
		t.Fatal("a summary never finished")
	}
}

const garageSummary = "Teagan asked whether the garage door was closed; Alan said he had closed it."

// Teagan asks the kitchen about the garage door and Alan answers from across
// the room. When the conversation ends, the model is asked what it was
// about, and the answer is kept for both of them, stamped with when the
// conversation last heard anyone.
//
// verifies SPEC §5, §8
func TestAConversationThatEndsIsSummarizedForEveryoneInIt(t *testing.T) {
	r := newSummaryRig(t, &summarizer{text: "  " + garageSummary + "\n"})
	s := r.open(t, "teagan")
	wait(t, heardFrom(s, "teagan", "is the garage door closed"))
	// Inside the silence backstop, which would otherwise end it first.
	r.clock.advance(12 * time.Second)
	wait(t, heardFrom(s, "alan", "I closed it on my way in"))
	lastHeard := r.clock.Now()
	if err := s.Close(context.Background(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	r.drained(t)

	want := summarized{Heard: []string{"is the garage door closed", "I closed it on my way in"}, People: []string{"teagan", "alan"}}
	if got := r.sum.asked(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("the model was asked to summarize %+v, want %+v", got, want)
	}
	wantKept := kept{People: []string{"teagan", "alan"}, Summary: journal.Summary{
		ConversationID: s.ConversationID(), At: lastHeard, Text: garageSummary,
	}}
	if got := r.mem.keeps(); len(got) != 1 || !reflect.DeepEqual(got[0], wantKept) {
		t.Errorf("kept %+v, want %+v", got, wantKept)
	}
	e := r.eventOf(t, s.ConversationID(), journal.KindConversationSummarized)
	if e.Fields["people_json"] != `["teagan","alan"]` || e.Fields["summary"] != garageSummary || e.Fields["error"] != "" {
		t.Errorf("recorded %v", e.Fields)
	}
	if st := r.state(t, s.ConversationID()); st.Summary != garageSummary {
		t.Errorf("the log's summary is %q", st.Summary)
	}
}

// Walking from the kitchen to the office carries the conversation along, so
// nothing is summarized until it really ends, and then all of it is.
//
// verifies SPEC §4.5, §5
func TestAConversationIsSummarizedWhenItEndsNotWhenItMoves(t *testing.T) {
	r := newSummaryRig(t, &summarizer{text: "Teagan asked for the porch light on, then off again from the office."})
	kitchen := r.open(t, "teagan")
	wait(t, heardFrom(kitchen, "teagan", "turn the porch light on"))
	office := migrate(t, r.rig, "office", "teagan")
	wait(t, heardFrom(office, "teagan", "actually turn it off"))
	if err := office.Close(context.Background(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	r.drained(t)

	want := []summarized{{Heard: []string{"turn the porch light on", "actually turn it off"}, People: []string{"teagan"}}}
	if got := r.sum.asked(); !reflect.DeepEqual(got, want) {
		t.Errorf("summarized %+v, want once, over both rooms: %+v", got, want)
	}
	if n := countKind(r.kinds(t, office.ConversationID()), journal.KindConversationSummarized); n != 1 {
		t.Errorf("conversation_summarized recorded %d times, want once", n)
	}
}

// A visitor at the front door is nobody to keep a summary for, and a wake
// with nothing said after it leaves nothing to summarize. Neither asks the
// model or records anything.
//
// verifies SPEC §5
func TestNothingIsSummarizedForAGuestOrForSilence(t *testing.T) {
	r := newSummaryRig(t, &summarizer{text: "Somebody asked whether anyone was home."})
	guest := r.open(t, "")
	wait(t, heard(guest, "is anyone home"))
	if err := guest.Close(context.Background(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	quiet := r.open(t, "alice")
	if err := quiet.Close(context.Background(), "silence_timeout"); err != nil {
		t.Fatalf("close: %v", err)
	}
	r.drained(t)

	if got := r.sum.asked(); len(got) != 0 {
		t.Errorf("the model was asked to summarize %+v", got)
	}
	for _, s := range []*session.Session{guest, quiet} {
		if n := countKind(r.kinds(t, s.ConversationID()), journal.KindConversationSummarized); n != 0 {
			t.Errorf("conversation_summarized recorded %d times", n)
		}
	}
}

// A summary that could not be written or kept says why in the log, instead
// of the conversation silently never coming up again.
//
// verifies SPEC §5, §8
func TestAFailedSummaryIsRecordedNotLost(t *testing.T) {
	cases := map[string]struct {
		sum       *summarizer
		keepErr   error
		wantError string
		wantText  string
	}{
		"the model failed": {
			sum:       &summarizer{err: errors.New("ollama chat: 503 Service Unavailable")},
			wantError: "ollama chat: 503 Service Unavailable",
		},
		"the model wrote nothing": {
			sum:       &summarizer{text: " \n"},
			wantError: "the model wrote nothing",
		},
		"the store refused it": {
			sum:       &summarizer{text: "Alice asked whether the package had come; it had."},
			keepErr:   errors.New("connection refused"),
			wantError: "keep: connection refused",
			wantText:  "Alice asked whether the package had come; it had.",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newSummaryRig(t, tc.sum)
			r.mem.keepErr = tc.keepErr
			s := r.open(t, "alice")
			wait(t, heardFrom(s, "alice", "did the package come"))
			if err := s.Close(context.Background(), "model_ended"); err != nil {
				t.Fatalf("close: %v", err)
			}
			r.drained(t)

			e := r.eventOf(t, s.ConversationID(), journal.KindConversationSummarized)
			if e.Fields["error"] != tc.wantError || e.Fields["summary"] != tc.wantText {
				t.Errorf("recorded %v, want error %q and summary %q", e.Fields, tc.wantError, tc.wantText)
			}
			if got := r.mem.keeps(); len(got) != 0 {
				t.Errorf("kept %+v", got)
			}
		})
	}
}

// A model that never answers is given up on after SummaryTimeout, by the
// injected clock, and the log says so.
//
// verifies SPEC §5
func TestASummaryThatNeverComesTimesOut(t *testing.T) {
	r := newSummaryRig(t, &summarizer{block: true})
	s := r.open(t, "teagan")
	wait(t, heardFrom(s, "teagan", "remind me what's on the calendar"))
	if err := s.Close(context.Background(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	deadline := time.Now().Add(patience)
	for len(r.sum.asked()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the model was never asked")
		}
		runtime.Gosched()
	}
	r.clock.advance(session.DefaultSummaryTimeout)
	r.drained(t)

	e := r.eventOf(t, s.ConversationID(), journal.KindConversationSummarized)
	if e.Fields["error"] != context.Canceled.Error() || e.Fields["summary"] != "" {
		t.Errorf("recorded %v, want the cancellation", e.Fields)
	}
}

// Every turn is told when it was heard, by the log's clock, and the
// speaker's recent conversations; the log records them so a replay is told
// the same. A new summary landing mid-conversation is recorded once.
//
// verifies SPEC §5, §8
func TestATurnIsToldTheTimeAndWhatThePersonAskedLately(t *testing.T) {
	r := newSummaryRig(t, &summarizer{text: "unused"})
	eggs := journal.Summary{ConversationID: "conv-kitchen-0731", At: epoch.Add(-28 * time.Hour), Text: "Teagan set a ten minute timer for the eggs."}
	garage := journal.Summary{ConversationID: "conv-garage-0812", At: epoch.Add(-4 * time.Hour), Text: garageSummary}
	r.mem.setSummaries("teagan", eggs)

	r.clock.advance(3 * time.Minute)
	s := r.open(t, "teagan")
	wait(t, heardFrom(s, "teagan", "what did I ask you yesterday"))
	asks := r.engine.asks()
	if len(asks) != 1 || !asks[0].Now.Equal(epoch.Add(3*time.Minute)) {
		t.Fatalf("asked %+v, want told it is three minutes past noon", asks)
	}
	if !reflect.DeepEqual(asks[0].Summaries, []journal.Summary{eggs}) {
		t.Errorf("told %+v, want the eggs", asks[0].Summaries)
	}
	if got := r.mem.askedAt; len(got) != 1 || !got[0].Equal(epoch.Add(3*time.Minute)) {
		t.Errorf("recalled as of %v, want when it was heard", got)
	}

	r.mem.setSummaries("teagan", garage, eggs)
	r.clock.advance(10 * time.Second)
	wait(t, heardFrom(s, "teagan", "and before that"))
	wait(t, heardFrom(s, "teagan", "thanks"))
	if n := countKind(r.kinds(t, s.ConversationID()), journal.KindMemoryRecalled); n != 2 {
		t.Errorf("memory_recalled recorded %d times, want again only when the summaries changed", n)
	}
	st := r.state(t, s.ConversationID())
	if len(st.RecalledSummaries) != 2 || st.RecalledSummaries[0].Text != garageSummary || !st.RecalledSummaries[0].At.Equal(garage.At) {
		t.Errorf("the log recalled %+v, want the garage first", st.RecalledSummaries)
	}
	asks = r.engine.asks()
	if last := asks[len(asks)-1]; !last.Now.Equal(epoch.Add(3*time.Minute+10*time.Second)) || len(last.Summaries) != 2 {
		t.Errorf("the last ask was told %v and %+v", last.Now, last.Summaries)
	}
}

// A summarizer with nowhere to keep summaries is a wiring mistake.
func TestASummarizerNeedsMemories(t *testing.T) {
	cfg := session.Config{
		Journal: journal.New(journal.NewMemStore(), newClock(), versions()), Store: journal.NewMemStore(),
		Clock: newClock(), Timers: newClock(), Engine: &scriptEngine{}, Speaker: newSpeaker(),
		Summarizer: &summarizer{},
	}
	if _, err := session.New(cfg); err == nil {
		t.Error("a supervisor summarizes with nowhere to keep it")
	}
}

// The kitchen satellite drops Teagan mid-conversation and Teagan wakes it
// again straight away, resuming the same conversation. The first summary
// describes the conversation as it was when the link dropped, not the one
// that carried on, so it cannot race the summary the second end writes.
//
// verifies SPEC §4.5, §5
func TestASummaryDescribesTheConversationAsItEnded(t *testing.T) {
	r := newSummaryRig(t, &summarizer{text: "teagan asked about the porch light."})
	first := r.open(t, "teagan")
	wait(t, heardFrom(first, "teagan", "is the porch light on"))
	if err := first.Close(context.Background(), "device_lost"); err != nil {
		t.Fatalf("close: %v", err)
	}
	again := r.open(t, "teagan")
	if !again.Resumed() {
		t.Fatalf("the wake started a new conversation")
	}
	wait(t, heardFrom(again, "teagan", "turn it off then"))
	if err := again.Close(context.Background(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	r.drained(t)

	got := r.sum.asked()
	slices.SortFunc(got, func(a, b summarized) int { return len(a.Heard) - len(b.Heard) })
	want := []summarized{
		{Heard: []string{"is the porch light on"}, People: []string{"teagan"}},
		{Heard: []string{"is the porch light on", "turn it off then"}, People: []string{"teagan"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("summarized %+v, want each end as it was: %+v", got, want)
	}
}
