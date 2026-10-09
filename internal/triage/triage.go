// Package triage reads the journal for conversations worth a reviewer's time
// (SPEC §9.1): barge-ins, failures and speaker flips. Like harvest, it is a
// reader of existing data; a signal is derived on read, never recorded.
//
// Slow turns and repeated requests are not signalled yet. Speech events are
// recorded when playback ends, not at its first frame, so the journal cannot
// say how long a person waited for audio; and "repeated" needs a similarity
// rule nobody has chosen.
package triage

import (
	"context"
	"fmt"
	"time"

	"github.com/teaganglenn/chorus/internal/harvest"
	"github.com/teaganglenn/chorus/internal/journal"
)

// Kind names what made a conversation worth triage.
type Kind string

const (
	KindBargeIn     Kind = "barge-in"
	KindFailure     Kind = "failure"
	KindSpeakerFlip Kind = "speaker-flip"
)

// Signal is one reason to look at a conversation, anchored to the event that
// raised it and the utterance that turn answered.
type Signal struct {
	Kind           Kind
	ConversationID string
	Seq            uint64
	At             time.Time

	// Utterance is what the person said in the turn the signal belongs to,
	// with the voice and satellite it came from.
	Utterance string
	Speaker   string
	Satellite string

	// Session is the seq of the session_opened that turn ran under: where a
	// late result belongs once the conversation has moved on.
	Session uint64

	// Detail says why, in a line: the failed tool, the flip, the correction.
	Detail string

	// PairID names the harvested candidate for a barge-in.
	PairID string
}

// turnContext is where the conversation stood when an event was recorded.
type turnContext struct {
	utterance, speaker, satellite string
	session                       uint64
}

// Scan returns the conversation's signals in log order.
func Scan(ctx context.Context, store journal.Store, conversationID string) ([]Signal, error) {
	events, err := store.Events(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	pairs, err := harvest.Harvest(ctx, store, conversationID)
	if err != nil {
		return nil, err
	}
	cutPairs := map[uint64]harvest.Pair{}
	for _, p := range pairs {
		cutPairs[p.Seq.Cut] = p
	}

	// A call keeps the turn that made it: a detach-policy tool outlives the
	// barge-in, so its result can land after the next utterance.
	type pending struct {
		tool string
		turn turnContext
	}
	var (
		out   []Signal
		cur   turnContext
		calls = map[string]pending{}
	)
	raiseIn := func(turn turnContext, e journal.Event, k Kind, detail string) {
		out = append(out, Signal{
			Kind: k, ConversationID: conversationID, Seq: e.Seq, At: e.At,
			Utterance: turn.utterance, Speaker: turn.speaker, Satellite: turn.satellite,
			Session: turn.session, Detail: detail,
		})
	}
	raise := func(e journal.Event, k Kind, detail string) { raiseIn(cur, e, k, detail) }
	for _, e := range events {
		if e.Speculative {
			continue
		}
		switch e.Kind {
		case journal.KindSessionOpened:
			cur.satellite, cur.session = e.Fields["satellite"], e.Seq
			if cur.speaker == "" {
				cur.speaker = e.Fields["speaker_id"]
			}
		case journal.KindUtteranceTranscribed:
			// An unidentified voice is not a flip: speaker ID abstained.
			if sp := e.Fields["speaker_id"]; sp != "" {
				prev := cur.speaker
				cur.utterance, cur.speaker = e.Fields["text"], sp
				if prev != "" && prev != sp {
					raise(e, KindSpeakerFlip, prev+" → "+sp)
				}
			} else {
				cur.utterance = e.Fields["text"]
			}
		case journal.KindToolCalled:
			calls[e.Fields["call_id"]] = pending{tool: e.Fields["tool"], turn: cur}
		case journal.KindToolResult:
			c, ok := calls[e.Fields["call_id"]]
			if !ok {
				c.turn = cur
			}
			// Cancelled is a barge-in working; detached is a tool outliving
			// the turn by design. Neither failed.
			if o := e.Fields["outcome"]; o == "error" || o == "timed_out" {
				raiseIn(c.turn, e, KindFailure, c.tool+" "+o)
			}
		case journal.KindModelCompleted:
			if e.Fields["finish_reason"] == "error" {
				raise(e, KindFailure, "model finished with error")
			}
		case journal.KindSessionClosed:
			if r := e.Fields["reason"]; r == "error" || r == "device_lost" {
				raise(e, KindFailure, "session closed: "+r)
			}
		}
		if p, ok := cutPairs[e.Seq]; ok {
			raise(e, KindBargeIn, fmt.Sprintf("cut → “%s”", p.Heard))
			out[len(out)-1].PairID = p.ID
		}
	}
	return out, nil
}
