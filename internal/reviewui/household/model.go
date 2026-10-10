package household

import (
	"context"
	"fmt"
	"sync"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/rerun"
	sess "github.com/teagan42/chorus/internal/session"
)

// Model stands in for the household's model wherever no real one may be
// asked, as on the hosted demo. The day ran under tools@7, which offered
// media_search; the registry's schema chorusd offers now is tools@8, which
// defers it (SPEC §14). Under the default prompt and tools@8 it answers each
// turn as the journal recorded it, which is what a deterministic model would
// do, but for the search: offered no media_search, a turn that searched for
// music says it cannot. Under an edited prompt or a reviewer's own tool
// schema it answers as the household's model did once told to be brief:
// lead with the count, offer the first, stop. It never calls a tool it was
// not offered.
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
			Calls:  []rerun.Call{{Tool: "media_search", Args: `{"query":"Led Zeppelin","limit":5}`}},
			Speech: []rerun.Speech{{Text: "I found three albums. Want Led Zeppelin one?"}},
		}, true
	case "and put on some jazz":
		return rerun.Take{
			Calls:  []rerun.Call{{Tool: "media_search", Args: `{"query":"jazz","limit":3}`}},
			Speech: []rerun.Speech{{Text: "Three jazz playlists. Playing Late Night Jazz."}},
		}, true
	case "is the garage door closed":
		// Checks the sensor that answers, and says nothing until it has.
		return rerun.Take{
			Calls: []rerun.Call{{Tool: "ha_get_state", Args: `{"entity_id":"binary_sensor.garage_door_contact"}`}},
		}, true
	case "set a timer for the oven":
		return rerun.Take{Speech: []rerun.Speech{{Text: "How long for the oven?"}}}, true
	}
	return rerun.Take{}, false
}

// unsearched is what a turn that searched for music says when it is offered
// nothing to search with: that it cannot, not results it never found.
func unsearched(text string) (rerun.Take, bool) {
	switch text {
	case "play something by zeppelin":
		return rerun.Take{Speech: []rerun.Speech{{Text: "Sorry, I can't search for music yet, so I can't find Led Zeppelin for you."}}}, true
	case "and put on some jazz":
		return rerun.Take{Speech: []rerun.Speech{{Text: "Sorry, I can't search for music yet, so I can't pick a jazz playlist."}}}, true
	case "something quieter":
		// Nor play a playlist only a search would have found.
		return rerun.Take{Speech: []rerun.Speech{{Text: "Sorry, I can't search for music yet, so I can't find anything quieter."}}}, true
	}
	return rerun.Take{}, false
}

// offeredSchema is the version of registry.Offered as the household's model
// knows it: the tools chorusd offers today, not the day's tools@7.
const offeredSchema = "tools@8"

// Engines builds Replay's engine for a model, prompt and tools, as
// reviewui's engine factory does against Ollama.
func (m *Model) Engines(model, prompt string, tools map[string]registry.ToolSpec) (sess.Engine, journal.Versions, error) {
	v := journal.Versions{Model: model, Prompt: Versions().Prompt, ToolSchema: offeredSchema}
	if prompt != ollama.DefaultPrompt {
		v.Prompt = "sys@edited"
	}
	if ollama.ToolSchema(tools) != ollama.ToolSchema(registry.Offered()) {
		v.ToolSchema = "tools@edited"
	}
	// The registry moving on from the day's schema is not the reviewer's
	// edit; only what they changed makes the model brief.
	edited := v.Prompt == "sys@edited" || v.ToolSchema == "tools@edited"
	return engine{m: m, edited: edited, offered: tools}, v, nil
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
	m       *Model
	edited  bool
	offered map[string]registry.ToolSpec
}

func (e engine) Turn(ctx context.Context, in sess.Input) (<-chan sess.Action, error) {
	if err := e.m.load(ctx); err != nil {
		return nil, err
	}
	take, ok := e.m.recorded[in.ConversationID+"\x00"+in.Text]
	if b, changed := brief(in.Text); e.edited && changed {
		take, ok = b, true
	}
	if u, searched := unsearched(in.Text); ok && searched {
		if _, offered := e.offered["media_search"]; !offered {
			take = u
		}
	}
	if !ok {
		return nil, fmt.Errorf("household model: %s never heard %q", in.ConversationID, in.Text)
	}
	var out []sess.Action
	for i, c := range take.Calls {
		if _, ok := e.offered[c.Tool]; ok {
			out = append(out, sess.ToolCall{ID: fmt.Sprintf("call_%d", i+1), Tool: c.Tool, Args: c.Args})
		}
	}
	// A replayed take has no playback, so it says the unheard part too.
	for i, s := range take.Speech {
		if _, ok := e.offered["speak"]; ok {
			out = append(out, sess.SpeechDelta{CallID: fmt.Sprintf("speak_%d", i+1), Text: s.Text + s.Unheard, Mode: sess.ModeQueue, Last: true})
		}
	}
	out = append(out, sess.TurnEnd{FinishReason: "stop", Completion: "{}"})
	ch := make(chan sess.Action, len(out))
	for _, a := range out {
		ch <- a
	}
	close(ch)
	return ch, nil
}
