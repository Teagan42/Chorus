package session_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

const tomHolland = `[{"title":"Uncharted","year":2022,"starring":["Tom Holland","Mark Wahlberg"]}]`

// searchTool keeps the arguments it was called with and holds the search
// until released, so a test can hear what played while it worked.
type searchTool struct {
	mu      sync.Mutex
	args    []string
	release chan struct{}
}

func newSearch() *searchTool { return &searchTool{release: make(chan struct{})} }

func (s *searchTool) Invoke(ctx context.Context, args string) (string, error) {
	s.mu.Lock()
	s.args = append(s.args, args)
	s.mu.Unlock()
	select {
	case <-s.release:
		return tomHolland, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *searchTool) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.args...)
}

// Alan asks for a film like Indiana Jones with Tom Holland in it. The model
// searches the library and says, in the call itself, what to say while it
// looks. The person hears it while the search runs, and the library never
// sees it.
//
// verifies SPEC §4.1, §11
func TestASlowToolsAcknowledgementPlaysWhileItWorks(t *testing.T) {
	search := newSearch()
	m := &model{answer: func(in session.Input) []session.Action {
		if in.Dialogue[len(in.Dialogue)-1].Kind == journal.EntryHeard {
			return []session.Action{session.ToolCall{
				ID: "call_c1", Tool: "media_search",
				Args: `{"query":"adventure films starring Tom Holland","acknowledgement":"Let me look through the library."}`,
			}, done}
		}
		return []session.Action{said("call_s1", "Try Uncharted, from 2022."), done}
	}}
	r := modelRig(t, m, map[string]session.Tool{"media_search": search})
	s := r.open(t, "alan")

	errc := heard(s, "recommend a movie like indiana jones starring tom holland")
	if got := r.speaker.wrote(t); got != "Let me look through the library." {
		t.Errorf("heard %q while the search ran, want the acknowledgement", got)
	}
	close(search.release)
	wait(t, errc)

	if got := search.called(); len(got) != 1 || got[0] != `{"query":"adventure films starring Tom Holland"}` {
		t.Errorf("the library was asked %q, want the query alone", got)
	}
	st := r.state(t, s.ConversationID())
	if want := []string{"Let me look through the library.", "Try Uncharted, from 2022."}; !reflect.DeepEqual(st.Spoken, want) {
		t.Errorf("spoken = %q, want %q", st.Spoken, want)
	}
	ack := callByID(t, st, "call_c1_ack")
	if ack.Tool != "speak" || ack.Outcome != "ok" {
		t.Errorf("acknowledgement call = %+v, want a speak that played", ack)
	}
	for _, e := range st.Dialogue {
		if e.CallID == "call_c1_ack" && e.Acknowledges != "call_c1" {
			t.Errorf("acknowledgement in the dialogue = %+v, want it tied to call_c1", e)
		}
	}
}

// A slow call that is held for the person's yes says nothing: "Opening the
// garage now." before Alan agreed would be a lie. Once he has said yes, the
// call runs and says it.
//
// verifies SPEC §6, §11
func TestAHeldSlowCallSaysNothingUntilItRuns(t *testing.T) {
	opener := newSearch()
	close(opener.release)
	specs := map[string]registry.ToolSpec{
		"speak": registry.Specs["speak"],
		"garage_door": {
			Name: "garage_door", OnInterrupt: registry.InterruptUninterruptible,
			RequiresConfirmation: true, Slow: true, Timeout: 20 * time.Second,
		},
	}
	const open = `{"action":"open","acknowledgement":"Opening the garage now."}`
	m := &model{answer: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		_, isHeld := held(in)
		switch {
		case last.Kind == journal.EntryHeard && last.Text == "open the garage":
			return []session.Action{session.ToolCall{ID: "call_c1", Tool: "garage_door", Args: open}, done}
		case last.Kind == journal.EntryHeard && last.Text == "yes":
			nonce, _ := held(session.Input{Dialogue: in.Dialogue[:len(in.Dialogue)-2]})
			return []session.Action{session.ToolCall{ID: "call_c2", Tool: "garage_door", Args: withNonce(open, nonce)}, done}
		case isHeld:
			return []session.Action{said("call_s1", "Open the garage?"), done}
		}
		return []session.Action{done}
	}}
	r := newRigWith(t, nil, map[string]session.Tool{"garage_door": opener}, specs, func(c *session.Config) { c.Engine = m })
	s := r.open(t, "alan")

	wait(t, heard(s, "open the garage"))
	if got := r.state(t, s.ConversationID()).Spoken; !reflect.DeepEqual(got, []string{"Open the garage?"}) {
		t.Errorf("spoken while held = %q, want only the question", got)
	}

	wait(t, heard(s, "yes"))
	if got := opener.called(); len(got) != 1 || got[0] != `{"action":"open"}` {
		t.Errorf("the opener was called with %q, want the action alone", got)
	}
	if got := r.state(t, s.ConversationID()).Spoken; !reflect.DeepEqual(got, []string{"Open the garage?", "Opening the garage now."}) {
		t.Errorf("spoken = %q, want the question, then the acknowledgement once it ran", got)
	}
}

// A model that leaves the acknowledgement out still gets its search: the
// person waits in silence, which is the model's failure to fix, not a reason
// to refuse the call.
//
// verifies SPEC §7
func TestASlowCallWithNothingToSayStillRuns(t *testing.T) {
	search := newSearch()
	close(search.release)
	m := &model{answer: func(in session.Input) []session.Action {
		if in.Dialogue[len(in.Dialogue)-1].Kind == journal.EntryHeard {
			return []session.Action{session.ToolCall{ID: "call_c1", Tool: "media_search", Args: `{"query":"Spider-Man"}`}, done}
		}
		return []session.Action{done}
	}}
	r := modelRig(t, m, map[string]session.Tool{"media_search": search})
	s := r.open(t, "alan")

	wait(t, heard(s, "find spider-man"))

	if got := search.called(); len(got) != 1 || got[0] != `{"query":"Spider-Man"}` {
		t.Errorf("the library was asked %q", got)
	}
	if got := r.state(t, s.ConversationID()).Spoken; len(got) != 0 {
		t.Errorf("spoken = %q, want nothing", got)
	}
}
