package journal

import (
	"fmt"
	"slices"
	"strconv"
	"time"
)

// HouseTimers is the log every timer is kept in. A timer outlives the
// session that set it, and the daemon, which replays this log at startup
// (ADR-0045).
const HouseTimers = "house:timers"

// TimerStatus is where a timer is in its life.
type TimerStatus string

const (
	TimerRunning   TimerStatus = "running"
	TimerCancelled TimerStatus = "cancelled"
	TimerFinished  TimerStatus = "finished"
)

// Timer is one timer as its log says it stands.
type Timer struct {
	ID           string
	Label        string
	Announcement string
	Seconds      int
	FiresAt      time.Time

	// Satellite is where it was set, which is where it goes off.
	Satellite      string
	Person         string
	ConversationID string
	CallID         string

	Status TimerStatus

	// Outcome is what became of a finished timer's announcement, and
	// AnnouncedIn the conversation it was said in.
	Outcome     string
	AnnouncedIn string
}

// Announcement is something a conversation said that nobody in it asked
// for: a timer going off, or a request from another room (SPEC §4).
type Announcement struct {
	CallID            string
	Text              string
	Source            string
	TimerID           string
	RequestedBy       string
	FromSatellite     string
	FromConversation  string
	StartConversation bool
}

// Running are the timers still to go off, soonest first.
func (s State) Running() []Timer {
	var out []Timer
	for _, t := range s.Timers {
		if t.Status == TimerRunning {
			out = append(out, t)
		}
	}
	slices.SortStableFunc(out, func(a, b Timer) int { return a.FiresAt.Compare(b.FiresAt) })
	return out
}

// Timer finds one by id.
func (s State) Timer(id string) (Timer, bool) {
	if i := s.timer(id); i >= 0 {
		return s.Timers[i], true
	}
	return Timer{}, false
}

func (s State) timer(id string) int {
	return slices.IndexFunc(s.Timers, func(t Timer) bool { return t.ID == id })
}

// started folds a timer_started. A malformed or reused one is a log that
// lies about the house, so it fails the replay rather than being guessed at.
func (s State) started(f map[string]string) ([]Timer, error) {
	if s.timer(f["timer_id"]) >= 0 {
		return nil, fmt.Errorf("timer %q started twice", f["timer_id"])
	}
	secs, err := strconv.Atoi(f["seconds"])
	if err != nil {
		return nil, fmt.Errorf("timer %s seconds %q: %w", f["timer_id"], f["seconds"], err)
	}
	at, err := time.Parse(time.RFC3339Nano, f["fires_at"])
	if err != nil {
		return nil, fmt.Errorf("timer %s fires_at %q: %w", f["timer_id"], f["fires_at"], err)
	}
	return append(slices.Clip(s.Timers), Timer{
		ID: f["timer_id"], Label: f["label"], Announcement: f["announcement"],
		Seconds: secs, FiresAt: at.UTC(), Satellite: f["satellite"], Person: f["person"],
		ConversationID: f["conversation_id"], CallID: f["call_id"], Status: TimerRunning,
	}), nil
}

// ended moves a running timer to its end. A timer ends once: cancelling one
// that already went off, or the reverse, is not something the house did.
func (s State) ended(id string, status TimerStatus, outcome, in string) ([]Timer, error) {
	i := s.timer(id)
	if i < 0 {
		return nil, fmt.Errorf("timer %q %s but was never started", id, status)
	}
	if s.Timers[i].Status != TimerRunning {
		return nil, fmt.Errorf("timer %q %s after it %s", id, status, s.Timers[i].Status)
	}
	out := slices.Clone(s.Timers)
	out[i].Status, out[i].Outcome, out[i].AnnouncedIn = status, outcome, in
	return out, nil
}

// announcement is the one a speak call says, if it says one.
func (s State) announcement(callID string) *Announcement {
	for i := range s.Announcements {
		if s.Announcements[i].CallID == callID {
			a := s.Announcements[i]
			return &a
		}
	}
	return nil
}
