package rerun_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/rerun"
	"github.com/teagan42/chorus/internal/session"
)

// "just close it" means nothing on its own. Each turn keeps the dialogue the
// live session's first ask was sent, folded by the reducer through its
// utterance, so an eval can ask it as chorusd did.
//
// verifies SPEC §4.4, §8
func TestATurnKeepsTheDialogueItsFirstAskWasSent(t *testing.T) {
	events := garageLog(t)
	ts := turns(t, events)
	for _, turn := range ts {
		var state journal.State
		for _, e := range events {
			if e.Seq > turn.Seq {
				break
			}
			var err error
			if state, err = journal.Reduce(state, e); err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(turn.Dialogue, state.Dialogue) {
			t.Errorf("turn #%d dialogue = %+v, want the reducer's through its utterance %+v", turn.Seq, turn.Dialogue, state.Dialogue)
		}
		last := turn.Dialogue[len(turn.Dialogue)-1]
		if last.Kind != journal.EntryHeard || last.Text != turn.Text {
			t.Errorf("turn #%d dialogue ends on %+v, want its own utterance", turn.Seq, last)
		}
	}
	if got := ts[1].Dialogue[0]; got.Text != ts[0].Text {
		t.Errorf("the second turn's dialogue opens on %+v, want the first ask", got)
	}
}

// Replay asks a recorded turn on its own (ADR-0037), and still does: Run
// sends no dialogue, and Input is exactly what it sends.
//
// verifies SPEC §9.2
func TestReplayStillAsksATurnOnItsOwn(t *testing.T) {
	ts := turns(t, garageLog(t))
	eng := &scripted{}
	if _, err := rerun.Run(context.Background(), eng, "conv-garage", ts[1]); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := eng.asked[0]; got.Dialogue != nil {
		t.Errorf("the re-run was sent a dialogue of %d steps, want none", len(got.Dialogue))
	}
	if want := rerun.Input("conv-garage", ts[1]); !reflect.DeepEqual(eng.asked[0], want) {
		t.Errorf("Run asked %+v, want Input's %+v", eng.asked[0], want)
	}
}

// Ask is Run for an input the caller built: it collects the same take.
//
// verifies SPEC §9.2
func TestAskCollectsATakeFromAnyInput(t *testing.T) {
	ts := turns(t, garageLog(t))
	in := rerun.Input("conv-garage", ts[0])
	in.Dialogue = ts[0].Dialogue
	sc := &scripted{answers: map[string][]session.Action{in.Text: then(
		say("s1", "Kitchen lights off. ", "The garage door is open."),
		call("c1", "ha_get_state", `{"entity_id":"cover.garage_door"}`),
		end("stop"),
	)}}
	take, err := rerun.Ask(context.Background(), sc, in)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if take.Said() != "Kitchen lights off. The garage door is open." || len(take.Calls) != 1 || take.Finish != "stop" {
		t.Errorf("take = %+v", take)
	}
	if got := sc.asked[0]; !reflect.DeepEqual(got, in) {
		t.Errorf("asked %+v, want the input as given", got)
	}
}
