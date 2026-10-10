package journal

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// Call is one tool invocation and its outcome, correlated by call id.
type Call struct {
	ID      string
	Tool    string
	Args    string
	Outcome string
	Result  string
}

// State is derived, never stored. The log is the truth (SPEC §8).
type State struct {
	ConversationID string
	Satellite      string
	Speaker        string
	CloseReason    string
	Heard          []string
	Spoken         []string
	Unspoken       []string
	Completions    []string
	BargeInAt      []time.Duration
	Calls          []Call

	// Room is where the satellite the conversation is on stands, as the
	// last session_opened recorded it: what a turn is told (SPEC §5).
	Room string

	// Dialogue is the conversation as the model is told it on its next ask:
	// what was heard, what was said, and every call and result, in order.
	Dialogue []Entry

	// Confirmations are the calls held for the person's yes, in the order
	// they were held, and what became of their nonces (SPEC §6).
	Confirmations []Confirmation

	// Recalled is what the model is told it remembers, and RecalledFor whose
	// memories they are: the last memory_recalled, newest first (SPEC §5).
	// RecalledSummaries are that person's recent conversations from it, and
	// RecalledRankedBy the embedding model that chose them, if one did.
	Recalled          []Memory
	RecalledFor       string
	RecalledSummaries []Summary
	RecalledRankedBy  string

	// HeardAt is when the last utterance was logged: the time a turn is
	// told it is, so a replay is told the same.
	HeardAt time.Time

	// Participants are the identified people who spoke, in the order they
	// first did: whom the conversation's summary is kept for (SPEC §5).
	Participants []string

	// Summary is what the model wrote when the conversation last ended.
	Summary string

	// Announced marks a conversation a session opened with no wake word,
	// and Announcements what it said that nobody in it asked for (SPEC §4).
	Announced     bool
	Announcements []Announcement

	// Timers are every timer the log set, in the order they were set, and
	// what became of each. Only HouseTimers holds any (ADR-0045).
	Timers []Timer

	LastSeq     uint64
	Speculative int
	Open        bool
	Interrupted bool
}

// Overrides substitute recorded nondeterministic inputs, which is how a
// counterfactual replay asks "what if the tool had said this instead".
type Overrides struct {
	// Completions is keyed by sequence number; a completion has no other id.
	Completions map[uint64]string
	ToolResults map[string]string
}

// Replay rebuilds state for one conversation from its log. It is the test that
// keeps the reducer pure: if replay drifts, the log stopped being the runtime.
func Replay(ctx context.Context, store Store, conversationID string, ov Overrides) (State, error) {
	events, err := store.Events(ctx, conversationID)
	if err != nil {
		return State{}, fmt.Errorf("replay %s: %w", conversationID, err)
	}
	state := State{ConversationID: conversationID}
	for _, e := range events {
		state, err = Reduce(state, apply(e, ov))
		if err != nil {
			return State{}, fmt.Errorf("replay %s seq %d: %w", conversationID, e.Seq, err)
		}
	}
	return state, nil
}

// apply rewrites the nondeterministic payload of one event. Overrides never
// touch the log itself; replay is read-only.
func apply(e Event, ov Overrides) Event {
	var field, value string
	switch e.Kind {
	case KindModelCompleted:
		field, value = "completion_json", ov.Completions[e.Seq]
	case KindToolResult:
		field, value = "result_json", ov.ToolResults[e.Fields["call_id"]]
	}
	if value == "" {
		return e
	}
	fields := make(map[string]string, len(e.Fields))
	for k, v := range e.Fields {
		fields[k] = v
	}
	fields[field] = value
	e.Fields = fields
	return e
}

// Reduce folds one event into state. Pure: same state plus same event yields
// the same result, with no clock, no I/O, and no randomness.
func Reduce(s State, e Event) (State, error) {
	if _, ok := Meta[e.Kind]; !ok {
		return s, fmt.Errorf("undeclared event kind %q", e.Kind)
	}
	if !Handled(e.Kind) {
		return s, fmt.Errorf("unhandled event kind %q", e.Kind)
	}
	s.LastSeq = e.Seq

	// Speculative work is recorded but must not reach committed state, or
	// replay lies about what the system actually did (SPEC §11).
	if e.Speculative {
		s.Speculative++
		return s, nil
	}

	switch e.Kind {
	case KindSessionOpened:
		s.Open = true
		s.Satellite = e.Fields["satellite"]
		s.Room = e.Fields["room"]
		s.Speaker = e.Fields["speaker_id"]
		s.Participants = participate(s.Participants, s.Speaker)
		s.Announced = s.Announced || e.Fields["announced"] == "true"
		// A resumed wake reopens the log the migration closed, so the reason the
		// previous session ended no longer describes this conversation (§4.5).
		s.CloseReason = ""
	case KindSessionClosed:
		s.Open = false
		s.CloseReason = e.Fields["reason"]
	case KindUtteranceTranscribed:
		s.Heard = append(s.Heard, e.Fields["text"])
		s.Speaker = Attribute(s.Speaker, e.Fields["speaker_id"], e.Fields["speaker_match"])
		// Attributed as the session attributes it: a voice judged to be
		// nobody's is a guest's, and one nothing judged is still the current
		// speaker's (SPEC §5).
		s.Dialogue = appendEntry(s.Dialogue, Entry{Kind: EntryHeard, Text: e.Fields["text"], Speaker: s.Speaker})
		s.HeardAt = e.At.UTC()
		s.Participants = participate(s.Participants, e.Fields["speaker_id"])
		s.Confirmations = s.answered(e.Fields["text"], e.Fields["speaker_id"])
	case KindModelCompleted:
		s.Completions = append(s.Completions, e.Fields["completion_json"])
	case KindMemoryRecalled:
		ms, err := decodeMemories(e.Fields["memories_json"])
		if err != nil {
			return s, err
		}
		ss, err := decodeSummaries(e.Fields["summaries_json"])
		if err != nil {
			return s, err
		}
		s.Recalled, s.RecalledFor, s.RecalledSummaries = ms, e.Fields["person"], ss
		s.RecalledRankedBy = e.Fields["ranked_by"]
	case KindConversationSummarized:
		s.Summary = e.Fields["summary"]
	case KindToolCalled:
		s.Dialogue = s.called(e.Fields["call_id"], e.Fields["tool"], e.Fields["args_json"])
		s.Calls = append(s.Calls, Call{
			ID:   e.Fields["call_id"],
			Tool: e.Fields["tool"],
			Args: e.Fields["args_json"],
		})
	case KindToolResult:
		id := e.Fields["call_id"]
		i := indexOfCall(s.Calls, id)
		if i < 0 {
			return s, fmt.Errorf("result for unknown call %q", id)
		}
		calls := make([]Call, len(s.Calls))
		copy(calls, s.Calls)
		calls[i].Outcome = e.Fields["outcome"]
		calls[i].Result = e.Fields["result_json"]
		s.Calls = calls
		s.Dialogue = s.resulted(id, calls[i].Tool, calls[i].Outcome, calls[i].Result)
	case KindConfirmationRequested:
		c, ok := s.requested(e.Fields["call_id"], e.Fields["nonce"], e.Fields["presented"])
		if !ok {
			return s, fmt.Errorf("confirmation for unknown call %q", e.Fields["call_id"])
		}
		s.Confirmations = c
	case KindConfirmationGiven:
		c, ok := s.given(e.Fields["call_id"], e.Fields["nonce"])
		if !ok {
			return s, fmt.Errorf("confirmation given on unknown nonce %q", e.Fields["nonce"])
		}
		s.Confirmations = c
	case KindSpeechSpoken:
		s.Spoken = append(s.Spoken, e.Fields["text"])
		s.Dialogue = s.played(e.Fields["call_id"], e.Fields["text"], false)
	case KindSpeechTruncated:
		// Heard and unheard text stay separate: the model may only see what
		// the user actually heard (SPEC §4.2).
		s.Spoken = append(s.Spoken, e.Fields["spoken_text"])
		s.Unspoken = append(s.Unspoken, e.Fields["unspoken_text"])
		s.Interrupted = true
		s.Dialogue = s.played(e.Fields["call_id"], e.Fields["spoken_text"], true)
	case KindSpeechDiscarded:
		// Never played, so it joins Unspoken only. The model must not believe
		// the user heard it (SPEC §4.4).
		s.Unspoken = append(s.Unspoken, e.Fields["unspoken_text"])
	case KindBargeInDetected:
		ms, err := strconv.Atoi(e.Fields["tts_position_ms"])
		if err != nil {
			return s, fmt.Errorf("tts_position_ms %q: %w", e.Fields["tts_position_ms"], err)
		}
		s.BargeInAt = append(s.BargeInAt, time.Duration(ms)*time.Millisecond)
	case KindAnnouncementMade:
		s.Announcements = append(slices.Clip(s.Announcements), Announcement{
			CallID: e.Fields["call_id"], Text: e.Fields["text"], Source: e.Fields["source"],
			TimerID: e.Fields["timer_id"], RequestedBy: e.Fields["requested_by"],
			FromSatellite: e.Fields["from_satellite"], FromConversation: e.Fields["from_conversation"],
			StartConversation: e.Fields["start_conversation"] == "true",
		})
	case KindTimerStarted:
		t, err := s.started(e.Fields)
		if err != nil {
			return s, err
		}
		s.Timers = t
	case KindTimerCancelled:
		t, err := s.ended(e.Fields["timer_id"], TimerCancelled, "", "")
		if err != nil {
			return s, err
		}
		s.Timers = t
	case KindTimerFinished:
		t, err := s.ended(e.Fields["timer_id"], TimerFinished, e.Fields["outcome"], e.Fields["conversation_id"])
		if err != nil {
			return s, err
		}
		s.Timers = t
	case KindBargeInRejected, KindWakeRejected:
		// Tuning corpus only; a rejected candidate changes no state.
	case KindPresenceChanged:
		// The room's, in a device log: no conversation's state (ADR-0050).
	case KindSpeechStarted:
		// Timing only: what was heard is the spoken or truncated event that
		// closes the same speech (ADR-0035).
	}
	return s, nil
}

// Attribute is who an utterance belongs to, given who the conversation's
// current speaker is and how the utterance's voice matched the household.
// The session and the reducer both call it, so a replay attributes every
// turn as it ran (SPEC §5).
//
// A match names its person. A voice that was judged and matched nobody,
// below the threshold or too close between two people, is a guest's: handing
// it to whoever spoke before would tell a dinner guest that person's
// memories and let them forget them (ADR-0048). A voice nothing judged keeps
// the current speaker: no identifier, or an embedder that failed this once,
// is no evidence of someone else, and logs from before the match was
// recorded attribute exactly as they always did.
func Attribute(current, speakerID, match string) string {
	switch {
	case speakerID != "":
		return speakerID
	case Unmatched(match):
		return ""
	}
	return current
}

// Unmatched reports a speaker_match that judged the voice and found it was
// nobody the household enrolled: below the threshold, or too close between
// two people to hand either one's context to (SPEC §5).
func Unmatched(match string) bool { return match == "below_threshold" || match == "ambiguous" }

// participate adds an identified speaker the first time they speak.
func participate(people []string, id string) []string {
	if id == "" || slices.Contains(people, id) {
		return people
	}
	return append(slices.Clip(people), id)
}

func indexOfCall(calls []Call, id string) int {
	for i, c := range calls {
		if c.ID == id {
			return i
		}
	}
	return -1
}

// handled is the reducer's exhaustiveness claim, asserted against AllKinds.
var handled = map[Kind]bool{
	KindAnnouncementMade:       true,
	KindBargeInDetected:        true,
	KindBargeInRejected:        true,
	KindConfirmationGiven:      true,
	KindConfirmationRequested:  true,
	KindConversationSummarized: true,
	KindMemoryRecalled:         true,
	KindModelCompleted:         true,
	KindPresenceChanged:        true,
	KindSessionClosed:          true,
	KindSessionOpened:          true,
	KindSpeechDiscarded:        true,
	KindSpeechSpoken:           true,
	KindSpeechStarted:          true,
	KindSpeechTruncated:        true,
	KindTimerCancelled:         true,
	KindTimerFinished:          true,
	KindTimerStarted:           true,
	KindToolCalled:             true,
	KindToolResult:             true,
	KindUtteranceTranscribed:   true,
	KindWakeRejected:           true,
}

// Handled reports whether the reducer folds this kind. A new generated kind
// fails the exhaustiveness test rather than being silently dropped.
func Handled(k Kind) bool { return handled[k] }
