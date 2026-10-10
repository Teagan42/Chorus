package session

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/teagan42/chorus/internal/journal"
)

// Announcement sources, as announcement_made records them.
const (
	SourceTimer   = "timer"
	SourceRequest = "request"
)

// ErrClosed is an announcement offered to a session that has ended or is
// ending. The caller opens a new one.
var ErrClosed = errors.New("session: closed")

// Announcement is something to say on a satellite that nobody there asked
// for: proactive speech, which is a session with no wake word (SPEC §4).
type Announcement struct {
	Text   string
	Source string

	// TimerID is the timer going off; empty for a request.
	TimerID string

	// RequestedBy, FromSatellite and FromConversation are who asked for it
	// and where, or who set the timer and where.
	RequestedBy      string
	FromSatellite    string
	FromConversation string

	// StartConversation lets whoever is in the room answer with no wake word.
	StartConversation bool

	// Heard, when set, is sent once, never blocking, whether any of it was
	// heard. Queued is not heard: the session may end, or the speaker fail.
	Heard chan<- bool
}

// Announce opens a session with no wake word to say a. It closes once said,
// unless a asks for an answer: then the silence backstop closes it (SPEC
// §4.5).
func (sup *Supervisor) Announce(ctx context.Context, satellite string, a Announcement) (*Session, error) {
	s, err := sup.open(ctx, Wake{Satellite: satellite}, true)
	if err != nil {
		return nil, err
	}
	if err := s.Announce(ctx, a); err != nil {
		_ = s.Close(context.Background(), "error")
		return nil, err
	}
	go s.closeWhenSaid()
	return s, nil
}

// Announce says a, queued behind whatever this session is saying. It is
// journalled here, so the next ask is told what was said, and why.
func (s *Session) Announce(_ context.Context, a Announcement) error {
	text := strings.TrimSpace(a.Text)
	if text == "" {
		return errors.New("session: an announcement needs something to say")
	}
	s.annMu.Lock()
	defer s.annMu.Unlock()
	if s.ending || s.ctx.Err() != nil {
		return ErrClosed
	}
	id := "an_" + newID()[:8]
	fields := map[string]string{
		"text": text, "call_id": id, "source": a.Source, "timer_id": a.TimerID,
		"requested_by": a.RequestedBy, "from_satellite": a.FromSatellite,
		"from_conversation":  a.FromConversation,
		"start_conversation": strconv.FormatBool(a.StartConversation),
	}
	for k, v := range fields {
		if v == "" {
			delete(fields, k)
		}
	}
	if err := s.record(journal.Record{Kind: journal.KindAnnouncementMade, Fields: fields}); err != nil {
		return err
	}
	// Strings: marshalling cannot fail.
	args, _ := json.Marshal(struct {
		Text string `json:"text"`
		Mode Mode   `json:"mode"`
	}{Text: text, Mode: ModeQueue})
	if err := s.record(journal.Record{
		Kind:   journal.KindToolCalled,
		Fields: map[string]string{"tool": toolSpeak, "call_id": id, "args_json": string(args)},
	}); err != nil {
		return err
	}
	if !s.speech.announce(id, text, a.Heard) {
		// Ended between the check and the queue: recorded as never heard.
		s.fail(s.record(journal.Record{
			Kind:   journal.KindSpeechDiscarded,
			Fields: map[string]string{"unspoken_text": text, "reason": "session_closed"},
		}))
		s.result(id, "cancelled", "")
		return ErrClosed
	}
	s.answerable = s.answerable || a.StartConversation
	s.poke()
	return nil
}

// closeWhenSaid ends an announcing session once everything it was given to
// say has played, unless something it said asked for an answer.
func (s *Session) closeWhenSaid() {
	for {
		s.speech.waitIdle()
		s.annMu.Lock()
		if s.answerable || s.ending {
			s.annMu.Unlock()
			return
		}
		if s.speech.busy() {
			// Another announcement was queued behind the last one.
			s.annMu.Unlock()
			continue
		}
		s.ending = true
		s.annMu.Unlock()
		s.fail(s.Close(context.Background(), "announced"))
		return
	}
}

// Answerable reports whether whoever is in the room may answer this
// session with no wake word: it was woken, or an announcement asked them to.
func (s *Session) Answerable() bool {
	s.annMu.Lock()
	defer s.annMu.Unlock()
	return s.answerable
}
