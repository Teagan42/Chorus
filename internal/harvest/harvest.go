// Package harvest reads barge-in corrections out of the journal as preference
// candidates (SPEC §9.1). It is a reader of existing data, like replay: the
// log already holds the heard/unheard split, the correction, and what was
// said next, so a candidate is derived, never recorded (SPEC §8).
//
// A candidate is not a training pair yet. What the assistant said after the
// correction answers the correction, not the prompt the rejected turn
// answered, so the chosen side is left for the Curate step (ADR-0026).
//
// A satellite's rejected wakes are read the same way, as the wake-word
// corpus's hard negatives (SPEC §9.3).
package harvest

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// Source names where a candidate came from. Only barge-ins are harvested
// automatically; annotation and replay sources belong to the review UI.
type Source string

const (
	// SourceBargeIn is a pair cut from an interruption (SPEC §9.1).
	SourceBargeIn Source = "barge-in"
	// SourceAnnotation is a turn a reviewer labelled, with what it should
	// have done as the chosen side (SPEC §9.2).
	SourceAnnotation Source = "annotation"
	// SourceReplay is a turn whose re-run a reviewer promoted over what it
	// recorded (SPEC §9.2).
	SourceReplay Source = "replay"
)

// ContextTurns bounds how many turns before the rejected one the prompt
// carries. Four is enough to make a correction trainable in context without
// turning every pair into a transcript of the whole conversation.
const ContextTurns = 4

// Message is one role-tagged line of the prompt, in the chat shape training
// loaders expect. Name carries the speaker id, since a household has more
// than one voice (SPEC §5). ToolCalls are what an assistant side did beside
// what it said, as the model's template emits them together (SPEC §4.1).
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Name      string     `json:"name,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall is one action in a message, in the OpenAI function-call shape
// chat templates read. Speaking is the content, never a call (ADR-0003).
type ToolCall struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// Function is a call's tool and its arguments, as a JSON object.
type Function struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Seq locates the events a candidate was cut from, so a reviewer can open
// the log at the exact point and replay can reproduce it.
type Seq struct {
	// Prompt is the utterance the rejected turn answered.
	Prompt uint64 `json:"prompt"`
	// BargeIn is the detection that cut the turn.
	BargeIn uint64 `json:"barge_in"`
	// Cut is the first speech event the barge-in truncated or discarded.
	Cut uint64 `json:"cut"`
	// Correction is the utterance that followed the cut.
	Correction uint64 `json:"correction"`
}

// Audio holds the blob references a reviewer needs for inline playback,
// which is non-negotiable for judging a voice assistant (SPEC §9.2).
type Audio struct {
	// Rejected is every clip the user heard in the rejected turn, in order.
	Rejected []string `json:"rejected"`
	// BargeIn is the mic audio that passed the detection gate.
	BargeIn string `json:"barge_in"`
	// Correction is the mic audio of the correction.
	Correction string `json:"correction"`
	// AsSaid is every clip the user heard in the answering turn, in order.
	AsSaid []string `json:"as_said"`
}

// Pair is one barge-in harvested as a preference candidate. Rejected,
// RejectedUnheard, Heard and AsSaid are the four texts the Curate step
// edits; Chosen is what it decides.
type Pair struct {
	ID             string
	ConversationID string
	Source         Source

	// Prompt is the heard transcript the rejected turn answered, preceded by
	// up to ContextTurns earlier turns. Only text the user actually heard
	// appears on the assistant side (SPEC §4.4).
	Prompt []Message

	// Rejected is what the user heard of the rejected turn, in the order it
	// was heard. RejectedUnheard continues it verbatim: the two concatenated
	// are the turn as generated.
	Rejected        string
	RejectedUnheard string

	// Heard is the correction the user gave, with the voice it came from.
	Heard        string
	HeardSpeaker string

	// AsSaid is what the user heard of the turn that answered the correction.
	// AsSaidCut reports that this turn was interrupted too.
	AsSaid    string
	AsSaidCut bool

	// Chosen is empty and Curated false until a reviewer decides. AsSaid is
	// not copied here by default: it answers the correction, not the prompt.
	Chosen  string
	Curated bool

	// Versions is the configuration that produced the rejected turn, taken
	// from its completion. Attributed is false when the turn recorded none,
	// in which case the candidate is kept for review but cannot train.
	Versions   journal.Versions
	Attributed bool

	// Calls is every tool the rejected turn dispatched. Context for the
	// reviewer, never pair text.
	Calls []journal.Call

	// Recalled is what the rejected turn was told it remembers. A pair
	// trained without it teaches a model to state facts it was never given
	// (SPEC §5).
	Recalled []journal.Memory

	// RecalledSummaries are the person's earlier conversations the rejected
	// turn was told of, and HeardAt the time it was told it was: an answer
	// about yesterday is only right against both (SPEC §5).
	RecalledSummaries []journal.Summary
	HeardAt           time.Time

	// BargeInPositionMS is how far into playback the interruption landed,
	// as the listener snapshotted it at detection. The DAC keeps playing for
	// the stop's flight time, so this lags the real cut.
	BargeInPositionMS int

	// BargeInFrames is how much of Audio.BargeIn the gate judged, in device
	// frames: the blob is the whole utterance's. Zero is all of it.
	BargeInFrames int

	// CutFrames is the DAC-confirmed truncation point: frames_played on the
	// truncated speech event, relative to that clip's own audio. Zero when
	// the barge-in only discarded queued clips and played none of the cut.
	CutFrames int

	Seq   Seq
	Audio Audio

	// Labels are the reviewer's words for what went wrong, on an annotation
	// pair (SPEC §9.2).
	Labels []string

	// ChosenVersions and ChosenCalls say where a replay pair's chosen side
	// came from: the re-run's configuration and the calls it would make.
	ChosenVersions journal.Versions
	ChosenCalls    []journal.Call
}

// Turn is one turn as the rejected side of a pair would carry it: what it
// was asked, in context, and what it said and did. Annotation and replay
// pairs are cut from the turns a reviewer picks.
type Turn struct {
	// Seq is the turn's utterance, which is how Replay names it too, and
	// AskAudio that utterance's mic audio.
	Seq      uint64
	Prompt   []Message
	Speaker  string
	AskAudio string

	// Said and Unheard split what it generated as Pair's Rejected sides do.
	Said, Unheard string
	Audio         []string

	Calls      []journal.Call
	Versions   journal.Versions
	Attributed bool

	Recalled          []journal.Memory
	RecalledSummaries []journal.Summary
	HeardAt           time.Time
}

// Pair is the turn as the rejected side of a pair whose chosen side came
// from source, keyed conversation/turn/source so it never collides with a
// barge-in's conversation/cut.
func (t Turn) Pair(conversationID string, source Source, chosen string) Pair {
	return Pair{
		ID:                fmt.Sprintf("%s/%d/%s", conversationID, t.Seq, source),
		ConversationID:    conversationID,
		Source:            source,
		Prompt:            t.Prompt,
		Rejected:          t.Said,
		RejectedUnheard:   t.Unheard,
		HeardSpeaker:      t.Speaker,
		Chosen:            chosen,
		Versions:          t.Versions,
		Attributed:        t.Attributed,
		Calls:             t.Calls,
		Recalled:          t.Recalled,
		RecalledSummaries: t.RecalledSummaries,
		HeardAt:           t.HeardAt,
		Seq:               Seq{Prompt: t.Seq},
		Audio:             Audio{Rejected: t.Audio},
	}
}

// Result is one conversation's harvest. Uncorrected cuts are counted, not
// dropped: a log full of them is a gate-tuning problem (SPEC §4.3).
type Result struct {
	Pairs []Pair

	// Turns is every turn in log order, for the pairs a reviewer cuts.
	Turns []Turn

	// Uncorrected counts barge-ins whose cut was never answered: the session
	// closed first, or another barge-in landed before any utterance.
	Uncorrected int
}

// Harvest returns the conversation's candidates in log order.
func Harvest(ctx context.Context, store journal.Store, conversationID string) ([]Pair, error) {
	res, err := Scan(ctx, store, conversationID)
	if err != nil {
		return nil, err
	}
	return res.Pairs, nil
}

// Scan walks the log once in sequence order and returns candidates with the
// counts around them. Same log, same result: nothing here reads a clock.
func Scan(ctx context.Context, store journal.Store, conversationID string) (Result, error) {
	events, err := store.Events(ctx, conversationID)
	if err != nil {
		return Result{}, fmt.Errorf("harvest %s: %w", conversationID, err)
	}
	if len(events) == 0 {
		return Result{}, fmt.Errorf("harvest %s: no events", conversationID)
	}
	w := walker{conversationID: conversationID}
	for _, e := range events {
		if err := w.fold(e); err != nil {
			return Result{}, fmt.Errorf("harvest %s seq %d: %w", conversationID, e.Seq, err)
		}
	}
	w.closeTurn(nil)
	return w.result, nil
}

// turn is everything between one utterance and the next. The session records
// no turn event; every turn starts with an utterance and nothing else does.
type turn struct {
	promptSeq  uint64
	heard      string
	speaker    string
	heardAudio string

	spoken      []string
	spokenAudio []string
	calls       []journal.Call
	recalled    []journal.Memory
	summaries   []journal.Summary
	heardAt     time.Time

	versions  journal.Versions
	completed bool

	// bargeIn is the detection in this turn, if any. A truncation without
	// one is a preempt or a migration, which nobody corrected.
	bargeIn    *bargeIn
	cut        bool
	cutSeq     uint64
	cutFrames  int
	unheard    []string
	firstIsCut bool

	// closed marks a session close that was not a migration. The turn still
	// collects the completion recorded after it; the next utterance is a new
	// session's, not a correction.
	closed bool
}

// bargeIn is what a detection contributes to the pair cut from it.
type bargeIn struct {
	seq        uint64
	positionMS int
	audioRef   string
	frames     int
}

// draft is a cut turn waiting on the turn that answers its correction.
type draft struct {
	rejected turn
	prompt   []Message
	heard    journal.Event
}

type walker struct {
	conversationID string
	history        []turn
	cur            *turn
	answering      *draft
	result         Result

	// recalled and summaries are the last memory_recalled. A turn whose
	// memories had not changed recorded none, and was told these.
	recalled  []journal.Memory
	summaries []journal.Summary

	// unchosen are the speak calls that said an announcement, or a canned
	// line for a failed model or voice. Nobody's turn chose those words, so
	// they are no side of a pair (ADR-0045, ADR-0051).
	unchosen map[string]bool
}

// skip marks a speak call as said by nobody's turn.
func (w *walker) skip(callID string) {
	if callID == "" {
		return
	}
	if w.unchosen == nil {
		w.unchosen = map[string]bool{}
	}
	w.unchosen[callID] = true
}

func (w *walker) fold(e journal.Event) error {
	if e.Speculative {
		return nil
	}
	if w.unchosen[e.Fields["call_id"]] && e.Kind != journal.KindAnnouncementMade {
		return nil
	}
	switch e.Kind {
	case journal.KindAnnouncementMade:
		w.skip(e.Fields["call_id"])
	case journal.KindModelFailed, journal.KindSpeechFailed:
		// The failure itself is no correction and no answer; the line said
		// for it is nobody's choice (ADR-0051).
		w.skip(e.Fields["canned_call_id"])
	case journal.KindUtteranceTranscribed:
		w.closeTurn(&e)
		w.cur = &turn{
			promptSeq: e.Seq, heard: e.Fields["text"],
			speaker: e.Fields["speaker_id"], heardAudio: e.AudioRef,
			recalled: w.recalled, summaries: w.summaries, heardAt: e.At.UTC(),
		}
	case journal.KindMemoryRecalled:
		var ms []journal.Memory
		if err := json.Unmarshal([]byte(e.Fields["memories_json"]), &ms); err != nil {
			return fmt.Errorf("memories_json: %w", err)
		}
		var ss []journal.Summary
		if raw := e.Fields["summaries_json"]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &ss); err != nil {
				return fmt.Errorf("summaries_json: %w", err)
			}
		}
		w.recalled, w.summaries = ms, ss
		if w.cur != nil {
			w.cur.recalled, w.cur.summaries = ms, ss
		}
	case journal.KindSessionClosed:
		if w.cur != nil && e.Fields["reason"] != "migrated" {
			w.cur.closed = true
		}
	case journal.KindBargeInDetected:
		if w.cur == nil {
			return nil
		}
		ms, err := strconv.Atoi(e.Fields["tts_position_ms"])
		if err != nil {
			return fmt.Errorf("tts_position_ms %q: %w", e.Fields["tts_position_ms"], err)
		}
		if w.cur.cut {
			// A second interruption before any correction: the first cut was never answered.
			w.result.Uncorrected++
			w.cur.cut, w.cur.unheard, w.cur.cutSeq = false, nil, 0
		}
		frames := 0
		if f := e.Fields["audio_frames"]; f != "" {
			if frames, err = strconv.Atoi(f); err != nil {
				return fmt.Errorf("audio_frames %q: %w", f, err)
			}
		}
		w.cur.bargeIn = &bargeIn{seq: e.Seq, positionMS: ms, audioRef: e.AudioRef, frames: frames}
	case journal.KindSpeechSpoken:
		if w.cur != nil {
			w.cur.spoken = append(w.cur.spoken, e.Fields["text"])
			w.cur.spokenAudio = append(w.cur.spokenAudio, e.AudioRef)
		}
	case journal.KindSpeechTruncated:
		if w.cur == nil {
			return nil
		}
		w.cur.spoken = append(w.cur.spoken, e.Fields["spoken_text"])
		w.cur.spokenAudio = append(w.cur.spokenAudio, e.AudioRef)
		// Heard either way; only the person's cut is half of a pair. A voice
		// that failed is not a correction (ADR-0051).
		if w.cur.bargeIn != nil && cutByPerson(e.Fields["reason"]) {
			frames, err := strconv.Atoi(e.Fields["frames_played"])
			if err != nil {
				return fmt.Errorf("frames_played %q: %w", e.Fields["frames_played"], err)
			}
			w.cur.cutFrames = frames
			w.cur.markCut(e)
			// The playing utterance heads the queue whichever child recorded
			// first; speech events carry no call id to order by.
			w.cur.unheard = append([]string{e.Fields["unspoken_text"]}, w.cur.unheard...)
			w.cur.firstIsCut = true
		}
	case journal.KindSpeechDiscarded:
		if w.cur != nil && w.cur.bargeIn != nil && e.Fields["reason"] == "barge_in" {
			w.cur.markCut(e)
			w.cur.unheard = append(w.cur.unheard, e.Fields["unspoken_text"])
		}
	case journal.KindToolCalled:
		if w.cur != nil {
			w.cur.calls = append(w.cur.calls, journal.Call{
				ID: e.Fields["call_id"], Tool: e.Fields["tool"], Args: e.Fields["args_json"],
			})
		}
	case journal.KindToolResult:
		if w.cur != nil {
			id := e.Fields["call_id"]
			for i := range w.cur.calls {
				if w.cur.calls[i].ID == id {
					w.cur.calls[i].Outcome = e.Fields["outcome"]
					w.cur.calls[i].Result = e.Fields["result_json"]
				}
			}
		}
	case journal.KindModelCompleted:
		if w.cur != nil {
			w.cur.versions = e.Versions
			w.cur.completed = true
		}
	case journal.KindSessionOpened, journal.KindBargeInRejected, journal.KindWakeRejected, journal.KindSpeechStarted,
		journal.KindConfirmationRequested, journal.KindConfirmationGiven, journal.KindConversationSummarized,
		journal.KindTimerStarted, journal.KindTimerCancelled, journal.KindTimerFinished,
		journal.KindPresenceChanged:
		// A resumed open continues the same log; a barge-in rejection tunes the
		// gate and a wake rejection is a hard negative (Negatives), not a turn; a
		// start is when speech was heard, and the pair is what (ADR-0035). A
		// held call's outcome is its tool_result; the nonce is the audit's. A
		// summary is the conversation's, written after its last turn. Timers
		// are the house log's and presence a device log's: neither holds turns.
	default:
		return fmt.Errorf("unhandled event kind %q", e.Kind)
	}
	return nil
}

// cutByPerson reports a truncation the barge-in made. A log from before
// truncations named their reason recorded only barge-ins and preempts, and a
// preempt never follows a barge-in in the same turn.
func cutByPerson(reason string) bool { return reason == "" || reason == "barge_in" }

func (t *turn) markCut(e journal.Event) {
	if !t.cut {
		t.cut, t.cutSeq = true, e.Seq
	}
}

// closeTurn ends the current turn at the next utterance (nil at end of log),
// answering a waiting draft and drafting its own cut.
func (w *walker) closeTurn(next *journal.Event) {
	if w.cur == nil {
		return
	}
	t := *w.cur
	w.cur = nil

	if d := w.answering; d != nil {
		w.answering = nil
		w.result.Pairs = append(w.result.Pairs, w.finish(d, t))
	}
	prompt := w.prompt(t)
	said, unheard := t.said()
	w.result.Turns = append(w.result.Turns, Turn{
		Seq: t.promptSeq, Prompt: prompt, Speaker: t.speaker, AskAudio: t.heardAudio,
		Said: said, Unheard: unheard, Audio: t.spokenAudio,
		Calls: t.calls, Versions: t.versions, Attributed: t.completed && complete(t.versions),
		Recalled: t.recalled, RecalledSummaries: t.summaries, HeardAt: t.heardAt,
	})
	if t.cut {
		switch {
		case next == nil || t.closed:
			w.result.Uncorrected++
		default:
			w.answering = &draft{rejected: t, prompt: prompt, heard: *next}
		}
	}
	w.history = append(w.history, t)
}

// said is what the turn generated, heard and unheard, as a pair's rejected
// side carries it.
func (t turn) said() (heard, unheard string) {
	heard, unheard = joinSpeech(t.spoken), joinSpeech(t.unheard)
	// A discard is its own speak call; only a truncation's tail continues the heard text.
	if !t.firstIsCut && heard != "" && unheard != "" {
		unheard = " " + unheard
	}
	return heard, unheard
}

// prompt is the rejected turn's heard transcript after the heard half of the
// turns before it, oldest first.
func (w *walker) prompt(t turn) []Message {
	start := max(len(w.history)-ContextTurns, 0)
	var msgs []Message
	for _, h := range w.history[start:] {
		msgs = append(msgs, Message{Role: "user", Content: h.heard, Name: h.speaker})
		if len(h.spoken) > 0 {
			msgs = append(msgs, Message{Role: "assistant", Content: joinSpeech(h.spoken)})
		}
	}
	return append(msgs, Message{Role: "user", Content: t.heard, Name: t.speaker})
}

func (w *walker) finish(d *draft, answer turn) Pair {
	r := d.rejected
	rejected, unheard := r.said()
	return Pair{
		ID:                fmt.Sprintf("%s/%d", w.conversationID, r.cutSeq),
		ConversationID:    w.conversationID,
		Source:            SourceBargeIn,
		Prompt:            d.prompt,
		Rejected:          rejected,
		RejectedUnheard:   unheard,
		Heard:             d.heard.Fields["text"],
		HeardSpeaker:      d.heard.Fields["speaker_id"],
		AsSaid:            joinSpeech(answer.spoken),
		AsSaidCut:         answer.cut,
		Versions:          r.versions,
		Attributed:        r.completed && complete(r.versions),
		Calls:             r.calls,
		Recalled:          r.recalled,
		RecalledSummaries: r.summaries,
		HeardAt:           r.heardAt,
		BargeInPositionMS: r.bargeIn.positionMS,
		BargeInFrames:     r.bargeIn.frames,
		CutFrames:         r.cutFrames,
		Seq: Seq{
			Prompt: r.promptSeq, BargeIn: r.bargeIn.seq,
			Cut: r.cutSeq, Correction: d.heard.Seq,
		},
		Audio: Audio{
			Rejected:   r.spokenAudio,
			BargeIn:    r.bargeIn.audioRef,
			Correction: d.heard.AudioRef,
			AsSaid:     answer.spokenAudio,
		},
	}
}

// joinSpeech puts one space between utterances. Each utterance's own text is
// kept verbatim, so a truncation's unspoken tail still rejoins its head.
func joinSpeech(parts []string) string { return strings.Join(parts, " ") }

func complete(v journal.Versions) bool {
	return v.Model != "" && v.Prompt != "" && v.ToolSchema != ""
}
