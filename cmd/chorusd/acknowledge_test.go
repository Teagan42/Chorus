package main

import (
	"context"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// Alan asks the kitchen satellite for a film like Indiana Jones starring Tom
// Holland. The model only calls the library search, with something to say
// while it looks; the daemon plays that on the satellite while the search
// runs, and the answer after it.
//
// verifies SPEC §4.1, §11
func TestTheKitchenSaysSomethingWhileTheLibrarySearches(t *testing.T) {
	const (
		looking = "Let me look through the library."
		answer  = "Try Uncharted, from 2022."
	)
	searching := make(chan string, 1)
	release := make(chan struct{})
	library := session.ToolFunc(func(ctx context.Context, args string) (string, error) {
		searching <- args
		select {
		case <-release:
			return `[{"title":"Uncharted","year":2022,"starring":["Tom Holland","Mark Wahlberg"]}]`, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	eng := &scriptEngine{decide: func(in session.Input) []session.Action {
		if in.Dialogue[len(in.Dialogue)-1].Kind == journal.EntryHeard {
			return []session.Action{session.ToolCall{
				ID: "call_c1", Tool: "media_search",
				Args: `{"query":"adventure films starring Tom Holland","acknowledgement":"` + looking + `"}`,
			}, done}
		}
		return []session.Action{session.SpeechDelta{CallID: "call_s1", Text: answer, Last: true}, done}
	}}
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = eng
		d.tools = map[string]session.Tool{"media_search": library}
	})
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("recommend a movie like indiana jones starring tom holland", alan))

	if got := <-searching; got != `{"query":"adventure films starring Tom Holland"}` {
		t.Errorf("the library was asked %s, want the query alone", got)
	}
	// The acknowledgement reaches the satellite while the search is still out.
	dev.AwaitTTS(t, 2*len(looking))
	dev.PlayAll(t)
	r.store.awaitKind(t, journal.KindSpeechSpoken, 1)
	close(release)
	dev.AwaitTTS(t, 2*len(looking)+2*len(answer))
	dev.PlayAll(t)
	r.store.awaitKind(t, journal.KindSpeechSpoken, 2)

	var spoken []string
	for _, e := range r.store.ofKind(journal.KindSpeechSpoken) {
		spoken = append(spoken, e.Fields["text"])
	}
	if len(spoken) != 2 || spoken[0] != looking || spoken[1] != answer {
		t.Errorf("spoken = %q, want %q while it searched, then %q", spoken, looking, answer)
	}
}
