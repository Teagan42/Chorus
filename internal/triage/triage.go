// Package triage reads the journal for conversations worth a reviewer's time
// (SPEC §9.1): barge-ins, failures, repeated requests, slow answers and
// speaker flips. Like harvest, it is a reader of existing data; a signal is
// derived on read, never recorded.
package triage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/teaganglenn/chorus/internal/harvest"
	"github.com/teaganglenn/chorus/internal/journal"
)

// Kind names what made a conversation worth triage.
type Kind string

const (
	KindBargeIn     Kind = "barge-in"
	KindFailure     Kind = "failure"
	KindSpeakerFlip Kind = "speaker-flip"
	KindRepeated    Kind = "repeated"
	KindSlow        Kind = "slow"
)

// SlowAfter is SPEC §11's first-audio target. A turn whose first frame came
// later than this after the person stopped speaking is slow (ADR-0035).
const SlowAfter = 700 * time.Millisecond

// A repeat is the same person asking substantially the same thing again
// soon after: within RepeatWindow, with at least RepeatOverlap of the
// shorter ask's content words in the longer one. Fewer than repeatMinWords
// content words is an answer ("yes", "the first one"), not a request.
const (
	RepeatWindow   = 30 * time.Second
	RepeatOverlap  = 0.6
	repeatMinWords = 2
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
	// ask is the last utterance, to tell a repeat from a follow-up.
	type ask struct {
		at    time.Time
		text  string
		voice string // this utterance's own speaker_id: empty is unplaced
		words map[string]bool
		acted bool // it called a tool other than speak
	}
	var (
		out   []Signal
		cur   turnContext
		calls = map[string]pending{}
		last  *ask
		// timers are the house log's, by id: what each was set to say.
		timers = map[string]turnContext{}
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
			// Same voice means the same speaker_id on both utterances, not the
			// attribution carried over: an unplaced voice after Teagan may be
			// a guest. Both unplaced still counts, since without the speaker
			// sidecar every utterance is (ADR-0031).
			next := &ask{at: e.At, text: cur.utterance, voice: e.Fields["speaker_id"], words: contentWords(cur.utterance)}
			if last != nil && last.voice == next.voice && next.at.Sub(last.at) <= RepeatWindow && similar(last.words, next.words) {
				detail := fmt.Sprintf("asked again %.1f s after “%s”", next.at.Sub(last.at).Seconds(), last.text)
				if !last.acted {
					detail += " · no tool call on the first ask"
				}
				raise(e, KindRepeated, detail)
			}
			// An answer ("yes") is not a request, so the request it answered
			// stays the one a repeat is measured against.
			if len(next.words) >= repeatMinWords {
				last = next
			}
		case journal.KindToolCalled:
			calls[e.Fields["call_id"]] = pending{tool: e.Fields["tool"], turn: cur}
			if last != nil && e.Fields["tool"] != "speak" {
				last.acted = true
			}
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
		case journal.KindSpeechStarted:
			// No wait recorded means no stop to measure from, not a fast turn.
			ms, err := strconv.ParseInt(e.Fields["wait_ms"], 10, 64)
			if wait := time.Duration(ms) * time.Millisecond; err == nil && wait > SlowAfter {
				raise(e, KindSlow, fmt.Sprintf("first audio %.1f s after the ask · target %.1f s", wait.Seconds(), SlowAfter.Seconds()))
			}
		case journal.KindModelCompleted:
			if e.Fields["finish_reason"] == "error" {
				raise(e, KindFailure, "model finished with error")
			}
		case journal.KindTimerStarted:
			said := e.Fields["announcement"]
			if said == "" {
				said = strings.TrimSpace(e.Fields["label"] + " timer")
			}
			timers[e.Fields["timer_id"]] = turnContext{utterance: said, speaker: e.Fields["person"], satellite: e.Fields["satellite"]}
		case journal.KindTimerFinished:
			// A timer nobody heard go off is a failure the household felt.
			if o := e.Fields["outcome"]; o != "announced" {
				raiseIn(timers[e.Fields["timer_id"]], e, KindFailure, "timer "+e.Fields["timer_id"]+" "+o+": "+e.Fields["error"])
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

// fillers carry no request: articles, pronouns, politeness, the wake phrase.
var fillers = map[string]bool{
	"a": true, "an": true, "the": true, "to": true, "for": true, "of": true, "on": true,
	"in": true, "at": true, "and": true, "or": true, "is": true, "are": true, "it": true,
	"i": true, "me": true, "my": true, "you": true, "can": true, "could": true, "would": true,
	"please": true, "hey": true, "ok": true, "okay": true, "s": true, "that": true, "this": true,
	"eddie": true,
}

// contentWords is an ask's words, lowercased, without fillers.
func contentWords(text string) map[string]bool {
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if !fillers[w] {
			words[w] = true
		}
	}
	return words
}

// similar is the overlap coefficient: shared words over the shorter ask's.
func similar(a, b map[string]bool) bool {
	if len(a) < repeatMinWords || len(b) < repeatMinWords {
		return false
	}
	shared := 0
	for w := range a {
		if b[w] {
			shared++
		}
	}
	return float64(shared) >= RepeatOverlap*float64(min(len(a), len(b)))
}
