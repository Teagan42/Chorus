package household

import (
	"context"
	"fmt"
	"sync"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/provider/ollama"
	"github.com/teaganglenn/chorus/internal/rerun"
	sess "github.com/teaganglenn/chorus/internal/session"
)

// Model stands in for the household's model wherever no real one may be
// asked, as on the hosted demo. Under the default prompt it answers each
// turn as the journal recorded it, which is what a deterministic model would
// do. Under any edited prompt it answers as the household's model did once
// told to be brief: lead with the count, offer the first, stop.
type Model struct {
	store journal.Store

	once     sync.Once
	recorded map[string]rerun.Take // conversation + "\x00" + text
	err      error
}

// NewModel answers from the conversations in store.
func NewModel(store journal.Store) *Model { return &Model{store: store} }

// brief is the take the edited prompt changes, by transcript; every other
// turn answers as recorded.
func brief(text string) (rerun.Take, bool) {
	switch text {
	case "what's the weather today":
		return rerun.Take{
			Calls:  []rerun.Call{{Tool: "ha_get_state", Args: `{"entity_id":"weather.home"}`}},
			Speech: []rerun.Speech{{Text: "Rain from three, high of fourteen. Take an umbrella."}},
		}, true
	case "play something by zeppelin":
		return rerun.Take{
			Calls:  []rerun.Call{{Tool: "media_search", Args: `{"query":"Led Zeppelin","media_type":"album","limit":5}`}},
			Speech: []rerun.Speech{{Text: "I found three albums. Want Led Zeppelin one?"}},
		}, true
	case "and put on some jazz":
		return rerun.Take{
			Calls:  []rerun.Call{{Tool: "media_search", Args: `{"query":"jazz","media_type":"playlist","limit":3}`}},
			Speech: []rerun.Speech{{Text: "Three jazz playlists. Playing Late Night Jazz."}},
		}, true
	case "set a timer for the oven":
		return rerun.Take{Speech: []rerun.Speech{{Text: "How long for the oven?"}}}, true
	}
	return rerun.Take{}, false
}

// Engines builds Replay's engine for a model and prompt, as reviewui's
// engine factory does against Ollama.
func (m *Model) Engines(model, prompt string) (sess.Engine, journal.Versions, error) {
	v := journal.Versions{Model: model, Prompt: "sys@edited", ToolSchema: Versions().ToolSchema}
	if prompt == ollama.DefaultPrompt {
		v.Prompt = Versions().Prompt
	}
	return engine{m: m, edited: prompt != ollama.DefaultPrompt}, v, nil
}

func (m *Model) load(ctx context.Context) error {
	m.once.Do(func() {
		lister, ok := m.store.(journal.Lister)
		if !ok {
			m.err = fmt.Errorf("household model: %T cannot list conversations", m.store)
			return
		}
		ids, err := lister.Conversations(ctx)
		if err != nil {
			m.err = err
			return
		}
		m.recorded = map[string]rerun.Take{}
		for _, id := range ids {
			events, err := m.store.Events(ctx, id)
			if err != nil {
				m.err = err
				return
			}
			turns, err := rerun.Turns(events)
			if err != nil {
				m.err = fmt.Errorf("household model: %s: %w", id, err)
				return
			}
			for _, t := range turns {
				m.recorded[id+"\x00"+t.Text] = t.Recorded
			}
		}
	})
	return m.err
}

type engine struct {
	m      *Model
	edited bool
}

func (e engine) Turn(ctx context.Context, in sess.Input) (<-chan sess.Action, error) {
	if err := e.m.load(ctx); err != nil {
		return nil, err
	}
	take, ok := e.m.recorded[in.ConversationID+"\x00"+in.Text]
	if b, changed := brief(in.Text); e.edited && changed {
		take, ok = b, true
	}
	if !ok {
		return nil, fmt.Errorf("household model: %s never heard %q", in.ConversationID, in.Text)
	}
	var out []sess.Action
	for i, c := range take.Calls {
		out = append(out, sess.ToolCall{ID: fmt.Sprintf("call_%d", i+1), Tool: c.Tool, Args: c.Args})
	}
	// A replayed take has no playback, so it says the unheard part too.
	for i, s := range take.Speech {
		out = append(out, sess.SpeechDelta{CallID: fmt.Sprintf("speak_%d", i+1), Text: s.Text + s.Unheard, Mode: sess.ModeQueue, Last: true})
	}
	out = append(out, sess.TurnEnd{FinishReason: "stop", Completion: "{}"})
	ch := make(chan sess.Action, len(out))
	for _, a := range out {
		ch <- a
	}
	close(ch)
	return ch, nil
}
