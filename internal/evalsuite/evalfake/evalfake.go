// Package evalfake is a scripted turn engine for eval suites, so a suite runs
// end to end with no model: in the hermetic tests, and in `task eval:demo`
// over the household's Thursday. It answers each turn as the journal
// recorded it, unless a script says otherwise for the prompt it was built
// with, and it takes as long to speak as it is told to, on a clock that
// moves only then.
package evalfake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/evalsuite"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/rerun"
	"github.com/teagan42/chorus/internal/session"
)

// Clock is a clock that moves only when a model takes time to answer.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// Now is the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock on by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Answer is one take, how long it took to first speak, and whether the
// stream broke at its end, as a model that dies mid-answer.
type Answer struct {
	Take        rerun.Take
	FirstSpeech time.Duration
	Fail        string
}

// Model answers turns. Scripts[prompt][transcript] overrides the recorded
// take for an engine built with that prompt; the seed picks among several
// answers, so repetitions can differ as a sampled model's do.
type Model struct {
	Clock *Clock

	// Latency is how long an answer takes to first speak when it names no
	// time of its own.
	Latency time.Duration

	Scripts map[string]map[string][]Answer

	recorded map[string]rerun.Take // conversation + "\x00" + transcript

	mu    sync.Mutex
	asked []evalsuite.Config
}

// New answers from every conversation in store.
func New(ctx context.Context, store journal.Store) (*Model, error) {
	lister, ok := store.(journal.Lister)
	if !ok {
		return nil, fmt.Errorf("evalfake: %T cannot list conversations", store)
	}
	ids, err := lister.Conversations(ctx)
	if err != nil {
		return nil, err
	}
	m := &Model{Clock: &Clock{}, recorded: map[string]rerun.Take{}}
	for _, id := range ids {
		events, err := store.Events(ctx, id)
		if err != nil {
			return nil, err
		}
		turns, err := rerun.Turns(events)
		if err != nil {
			return nil, fmt.Errorf("evalfake: %s: %w", id, err)
		}
		for _, t := range turns {
			m.recorded[id+"\x00"+t.Text] = t.Recorded
		}
	}
	return m, nil
}

// Asked is every configuration a turn was asked under, in order.
func (m *Model) Asked() []evalsuite.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]evalsuite.Config(nil), m.asked...)
}

// Engine builds an engine for cfg, named as an Ollama engine would name it.
func (m *Model) Engine(cfg evalsuite.Config) (session.Engine, journal.Versions, error) {
	v := journal.Versions{
		Model: cfg.Model, Prompt: ollama.PromptVersion(cfg.Prompt), ToolSchema: ollama.ToolSchemaVersion(cfg.Tools),
	}
	return engine{m: m, cfg: cfg}, v, nil
}

type engine struct {
	m   *Model
	cfg evalsuite.Config
}

func (e engine) answer(in session.Input) (Answer, bool) {
	if answers := e.m.Scripts[e.cfg.Prompt][in.Text]; len(answers) > 0 {
		return answers[e.cfg.Seed%len(answers)], true
	}
	take, ok := e.m.recorded[in.ConversationID+"\x00"+in.Text]
	return Answer{Take: take}, ok
}

// Cannot is what a take says when it called a tool the engine was not
// offered, as household.Model's turns do when media_search is deferred.
const Cannot = "Sorry, I can't do that yet."

// Turn answers in.Text. It never calls a tool it was not offered, and a
// take that did says Cannot instead of what that tool would have told it.
func (e engine) Turn(_ context.Context, in session.Input) (<-chan session.Action, error) {
	e.m.mu.Lock()
	e.m.asked = append(e.m.asked, e.cfg)
	e.m.mu.Unlock()
	a, ok := e.answer(in)
	if !ok {
		return nil, fmt.Errorf("evalfake: %s never heard %q", in.ConversationID, in.Text)
	}
	wait := a.FirstSpeech
	if wait == 0 {
		wait = e.m.Latency
	}
	e.m.Clock.Advance(wait)
	var calls []session.Action
	speech := a.Take.Speech
	for i, c := range a.Take.Calls {
		if _, ok := e.cfg.Tools[c.Tool]; !ok {
			// What it said rested on a tool it no longer has.
			speech = []rerun.Speech{{Text: Cannot}}
			continue
		}
		calls = append(calls, session.ToolCall{ID: fmt.Sprintf("call_%d", i+1), Tool: c.Tool, Args: c.Args})
	}
	var out []session.Action
	if _, ok := e.cfg.Tools["speak"]; ok {
		for i, s := range speech {
			out = append(out, session.SpeechDelta{CallID: fmt.Sprintf("speak_%d", i+1), Text: s.Text + s.Unheard, Mode: session.ModeQueue, Last: true})
		}
	}
	out = append(out, calls...)
	end := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	if a.Fail != "" {
		end = session.TurnEnd{FinishReason: session.FinishError, Completion: fmt.Sprintf(`{"error":%q}`, a.Fail), Error: a.Fail}
	}
	ch := make(chan session.Action, len(out)+1)
	for _, act := range append(out, end) {
		ch <- act
	}
	close(ch)
	return ch, nil
}
