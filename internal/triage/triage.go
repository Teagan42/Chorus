// Package triage reads the journal for conversations worth a reviewer's time
// (SPEC §9.1): barge-ins, failures, repeated requests, slow answers and
// speaker flips, and the weak positives nobody corrected. Like harvest, it
// is a reader of existing data; a signal is derived on read, never recorded.
package triage

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
)

// Kind names what made a conversation worth triage.
type Kind string

const (
	KindBargeIn     Kind = "barge-in"
	KindFailure     Kind = "failure"
	KindSpeakerFlip Kind = "speaker-flip"
	KindRepeated    Kind = "repeated"
	KindSlow        Kind = "slow"

	// KindWeakPositive is a completed turn nobody corrected (SPEC §9.1).
	// WeakPositives raises it, never Scan: it is no problem to look into.
	KindWeakPositive Kind = "weak-positive"
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

	// First is a repeat's earlier ask: the turn whose answer failed.
	First uint64
}

// turnContext is where the conversation stood when an event was recorded.
// turn is the seq of the utterance that opened it.
type turnContext struct {
	utterance, speaker, satellite string
	session, turn                 uint64
}

// Scan returns the conversation's signals in log order.
func Scan(ctx context.Context, store journal.Store, conversationID string) ([]Signal, error) {
	sigs, _, err := read(ctx, store, conversationID)
	return sigs, err
}

// WeakPositives returns the conversation's completed turns that nobody cut
// off, asked again or saw fail, in log order, each at its completion.
func WeakPositives(ctx context.Context, store journal.Store, conversationID string) ([]Signal, error) {
	_, pos, err := read(ctx, store, conversationID)
	return pos, err
}

// outcome is how one turn ended, for telling a weak positive.
type outcome struct {
	turn      turnContext
	done      *journal.Event
	said      []string
	corrected bool
}

func read(ctx context.Context, store journal.Store, conversationID string) ([]Signal, []Signal, error) {
	events, err := store.Events(ctx, conversationID)
	if err != nil {
		return nil, nil, err
	}
	pairs, err := harvest.Harvest(ctx, store, conversationID)
	if err != nil {
		return nil, nil, err
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
		seq   uint64
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
		// announced are the speak calls nobody in the conversation asked for
		// (SPEC §4): their words are not the open turn's answer.
		announced = map[string]bool{}
		// turns are how each turn ended, in log order.
		turns []*outcome
		ended = map[uint64]*outcome{}
	)
	correct := func(turn uint64) {
		if o := ended[turn]; o != nil {
			o.corrected = true
		}
	}
	signal := func(turn turnContext, e journal.Event, k Kind, detail string) Signal {
		return Signal{
			Kind: k, ConversationID: conversationID, Seq: e.Seq, At: e.At,
			Utterance: turn.utterance, Speaker: turn.speaker, Satellite: turn.satellite,
			Session: turn.session, Detail: detail,
		}
	}
	raiseIn := func(turn turnContext, e journal.Event, k Kind, detail string) {
		out = append(out, signal(turn, e, k, detail))
		if k == KindFailure {
			correct(turn.turn)
		}
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
			// Attributed as the session attributed it: a voice that matched
			// nobody is a guest's turn and a flip, while one nothing judged
			// is not, since speaker ID abstained (ADR-0049).
			prev := cur.speaker
			cur.utterance, cur.turn = e.Fields["text"], e.Seq
			o := &outcome{turn: cur}
			turns, ended[e.Seq] = append(turns, o), o
			cur.speaker = journal.Attribute(prev, e.Fields["speaker_id"], e.Fields["speaker_match"])
			if prev != "" && prev != cur.speaker {
				raise(e, KindSpeakerFlip, prev+" → "+cmp.Or(cur.speaker, "guest"))
			}
			// Same voice means the same speaker_id on both utterances, not the
			// attribution carried over: an unplaced voice after Teagan may be
			// a guest. Both unplaced still counts, since without the speaker
			// sidecar every utterance is (ADR-0031).
			next := &ask{seq: e.Seq, at: e.At, text: cur.utterance, voice: e.Fields["speaker_id"], words: contentWords(cur.utterance)}
			if last != nil && last.voice == next.voice && next.at.Sub(last.at) <= RepeatWindow && similar(last.words, next.words) {
				detail := fmt.Sprintf("asked again %.1f s after “%s”", next.at.Sub(last.at).Seconds(), last.text)
				if !last.acted {
					detail += " · no tool call on the first ask"
				}
				raise(e, KindRepeated, detail)
				out[len(out)-1].First = last.seq
				correct(last.seq)
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
		case journal.KindBargeInDetected:
			correct(cur.turn)
		case journal.KindAnnouncementMade:
			announced[e.Fields["call_id"]] = true
		case journal.KindSpeechSpoken, journal.KindSpeechTruncated:
			if o := ended[cur.turn]; o != nil && !announced[e.Fields["call_id"]] {
				o.said = append(o.said, cmp.Or(e.Fields["text"], e.Fields["spoken_text"]))
			}
		case journal.KindModelCompleted:
			if e.Fields["finish_reason"] == "error" {
				raise(e, KindFailure, "model finished with error")
			} else if o := ended[cur.turn]; o != nil {
				o.turn, o.done = cur, &e
			}
		case journal.KindModelFailed:
			// A model that never answered left no completion to raise on;
			// one that broke or stalled already raised on its completion.
			if e.Fields["reason"] == "unavailable" {
				raise(e, KindFailure, "model unavailable: "+e.Fields["error"])
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
	var pos []Signal
	for _, o := range turns {
		if o.done == nil || o.corrected {
			continue
		}
		detail := "not cut off, asked again or failed"
		if said := strings.Join(o.said, " "); said != "" {
			detail = "answered “" + said + "” · " + detail
		}
		pos = append(pos, signal(o.turn, *o.done, KindWeakPositive, detail))
	}
	return out, pos, nil
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
