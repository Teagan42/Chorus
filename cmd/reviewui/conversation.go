package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/ui"
	"github.com/teagan42/chorus/internal/triage"
)

// logRow is one journal event as the conversation page shows it.
type logRow struct {
	Anchor  string // seq-N, so Triage can link to the event
	Offset  string
	Seq     uint64
	Ref     string // #N, or where in the house log the event is
	Kind    ui.SigTag
	Who     string
	Text    string
	Unheard string // generated, never heard; struck through
	Note    string
	Lines   []string // what a recall told the model, one memory or conversation a line
	Audio   string
	Second  string // the XMOS's lighter-processed channel of the same span (ADR-0050)

	// Signal tags the event Triage raised on; SignalWhy says why, in the
	// text column where a sentence fits.
	Signal    *ui.SigTag
	SignalWhy string

	// Again opens the labels of the turn a repeat asked again.
	Again *ui.Button

	// Annotation is the turn's labels, under the utterance that opens it.
	Annotation *turnAnnotation

	// Wake is the reviewer's word on a rejected wake, under it.
	Wake *wakeView
}

var kindTones = map[journal.Kind]ui.Tone{
	journal.KindSessionOpened: ui.ToneConv, journal.KindSessionClosed: ui.ToneConv,
	journal.KindUtteranceTranscribed: ui.TonePeople, journal.KindBargeInDetected: ui.TonePeople,
	journal.KindBargeInRejected: ui.ToneMuted, journal.KindWakeRejected: ui.ToneMuted, journal.KindPresenceChanged: ui.ToneMuted,
	journal.KindSpeechStarted: ui.ToneVoice, journal.KindSpeechSpoken: ui.ToneVoice, journal.KindSpeechTruncated: ui.ToneVoice, journal.KindSpeechDiscarded: ui.ToneVoice,
	journal.KindToolCalled: ui.ToneHome, journal.KindToolResult: ui.ToneHome, journal.KindModelCompleted: ui.ToneMuted,
	journal.KindAnnouncementMade: ui.ToneVoice,
	journal.KindTimerStarted:     ui.ToneHome, journal.KindTimerCancelled: ui.ToneHome, journal.KindTimerFinished: ui.ToneHome,
	journal.KindMemoryRecalled: ui.ToneConv, journal.KindConversationSummarized: ui.ToneConv,
	journal.KindConfirmationRequested: ui.ToneHome, journal.KindConfirmationGiven: ui.ToneHome,
	journal.KindModelFailed: ui.ToneMuted, journal.KindSpeechFailed: ui.ToneVoice,
}

// logContext is what a row needs from the events before it: the call a
// confirmation names, and what the person last said.
type logContext struct {
	calls map[string]journal.Event
	heard journal.Event
	loc   *time.Location
}

func (lc *logContext) see(e journal.Event) {
	switch e.Kind {
	case journal.KindToolCalled:
		if lc.calls == nil {
			lc.calls = map[string]journal.Event{}
		}
		lc.calls[e.Fields["call_id"]] = e
	case journal.KindUtteranceTranscribed:
		lc.heard = e
	}
}

// call is how a confirmation row names the call it held or let run.
func (lc *logContext) call(id string) (tool, args string) {
	if lc == nil {
		return "call " + id, ""
	}
	c, ok := lc.calls[id]
	if !ok {
		return "call " + id, ""
	}
	return c.Fields["tool"], c.Fields["args_json"]
}

// logRowOf says what one event means in a line, with its audio when the
// event has some. A truncation plays only what the DAC reached. lc, nil for
// the house log, holds what came before.
func logRowOf(e journal.Event, start journal.Event, lc *logContext) logRow {
	f := e.Fields
	row := logRow{
		Anchor: fmt.Sprintf("seq-%d", e.Seq), Seq: e.Seq, Ref: fmt.Sprintf("#%d", e.Seq),
		Offset: fmt.Sprintf("+%.1f s", e.At.Sub(start.At).Seconds()),
		Kind:   ui.SigTag{Text: strings.ReplaceAll(string(e.Kind), "_", " "), Tone: kindTones[e.Kind]},
		Who:    string(e.Actor),
	}
	if e.AudioRef != "" {
		row.Audio = audioSrc(e.AudioRef)
	}
	if ref := f["second_audio_ref"]; ref != "" {
		row.Second = audioSrc(ref)
	}
	switch e.Kind {
	case journal.KindSessionOpened:
		row.Text = "opened on " + f["satellite"]
		row.Note = "speaker " + f["speaker_id"]
		if f["resumed"] == "true" {
			row.Note += " · resumed"
		}
		if f["announced"] == "true" {
			row.Text, row.Note = "opened on "+f["satellite"]+" to announce", "no wake word"
		}
	case journal.KindSessionClosed:
		row.Text, row.Note = "closed: "+f["reason"], f["satellite"]
	case journal.KindUtteranceTranscribed:
		row.Who, row.Text = f["speaker_id"], f["text"]
	case journal.KindBargeInDetected, journal.KindBargeInRejected:
		row.Text = "barge-in at " + f["tts_position_ms"] + " ms of playback"
		if e.Kind == journal.KindBargeInRejected {
			row.Text = "barge-in rejected at " + f["stage"]
		}
		// The blob is the whole utterance; the gate judged its prefix.
		if row.Audio != "" && f["audio_frames"] != "" {
			row.Audio += "&to=" + f["audio_frames"]
		}
	case journal.KindWakeRejected:
		row.Text = "wake rejected: " + f["reason"]
	case journal.KindPresenceChanged:
		row.Text, row.Note = "presence: "+f["state"], f["sensor"]
	case journal.KindSpeechStarted:
		row.Text, row.Note = "first audio", "call "+f["call_id"]
		if ms, err := strconv.Atoi(f["wait_ms"]); err == nil {
			row.Text = fmt.Sprintf("first audio %.2f s after the ask", float64(ms)/1000)
		}
	case journal.KindSpeechSpoken:
		row.Text = f["text"]
	case journal.KindSpeechTruncated:
		row.Text, row.Unheard = f["spoken_text"], f["unspoken_text"]
		row.Note = f["frames_played"] + " frames played"
		if f["reason"] != "" {
			row.Note += " · cut: " + f["reason"]
		}
		if row.Audio != "" {
			row.Audio += "&to=" + f["frames_played"]
		}
	case journal.KindSpeechDiscarded:
		row.Unheard, row.Note = f["unspoken_text"], "discarded: "+f["reason"]
	case journal.KindToolCalled:
		row.Text, row.Note = f["tool"], f["args_json"]
	case journal.KindToolResult:
		row.Text, row.Note = f["outcome"], f["call_id"]
	case journal.KindModelCompleted:
		row.Text = "completed: " + f["finish_reason"]
	case journal.KindModelFailed:
		row.Text, row.Note = "model failed: "+f["reason"], failedBecause(f)
	case journal.KindSpeechFailed:
		row.Text, row.Note = "voice failed: "+f["reason"], "call "+f["call_id"]
		if why := failedBecause(f); why != "" {
			row.Note += " · " + why
		}
	case journal.KindAnnouncementMade:
		row.Text, row.Note = f["text"], announcedBecause(f)
	case journal.KindTimerStarted:
		row.Who, row.Text = f["person"], "set "+timerName(f["label"])+" for "+seconds(f["seconds"])+" on "+f["satellite"]
		row.Note = f["timer_id"] + " · goes off " + f["fires_at"]
	case journal.KindTimerCancelled:
		row.Text, row.Note = "cancelled", f["timer_id"]
	case journal.KindTimerFinished:
		row.Text, row.Note = "went off: "+f["outcome"], f["timer_id"]
		if f["error"] != "" {
			row.Note += " · " + f["error"]
		}
	case journal.KindMemoryRecalled:
		loc := time.UTC
		if lc != nil && lc.loc != nil {
			loc = lc.loc
		}
		row.Who = f["person"]
		row.Text, row.Lines, row.Note = recalled(f, loc)
	case journal.KindConversationSummarized:
		row.Text, row.Note = f["summary"], "kept for "+people(f["people_json"])
		if f["summary"] == "" {
			row.Text = "no summary"
		}
		if f["error"] != "" {
			row.Note += " · " + f["error"]
		}
	case journal.KindConfirmationRequested:
		tool, args := lc.call(f["call_id"])
		row.Text, row.Note = "held "+tool+" for the person's yes", args
		row.Lines = []string{"nonce " + f["nonce"] + " · " + f["call_id"]}
		if f["refused"] != "" {
			row.Lines = append(row.Lines, "refused "+f["presented"]+": "+f["refused"])
		}
	case journal.KindConfirmationGiven:
		tool, args := lc.call(f["call_id"])
		row.Text, row.Note = tool+" ran on a yes", args
		row.Lines = []string{"nonce " + f["nonce"] + " · " + f["call_id"]}
		if lc != nil && lc.heard.Seq != 0 {
			h := lc.heard.Fields
			row.Lines = append(row.Lines, fmt.Sprintf("redeemed after #%d, %s: “%s”", lc.heard.Seq, h["speaker_id"], h["text"]))
		}
	}
	return row
}

// recalled says what a turn was told it remembers, and how it was chosen.
func recalled(f map[string]string, loc *time.Location) (text string, lines []string, note string) {
	var ms []journal.Memory
	var ss []journal.Summary
	// An unreadable list shows as told nothing; the raw field is still in the log.
	_ = json.Unmarshal([]byte(f["memories_json"]), &ms)
	if raw := f["summaries_json"]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &ss)
	}
	text = "told " + counted(len(ms), "memory", "memories") + " and " + counted(len(ss), "earlier conversation", "earlier conversations")
	if len(ms) == 0 && len(ss) == 0 {
		text = "told nothing is remembered"
	}
	if f["ranked_by"] != "" {
		note = "chosen by relevance · " + f["ranked_by"]
	}
	return text, memoryLines(f["person"], ms, ss, loc), note
}

// memoryLines is a line per memory, then per earlier conversation, as a
// reviewer reads what the model was told about person.
func memoryLines(person string, ms []journal.Memory, ss []journal.Summary, loc *time.Location) []string {
	var lines []string
	for _, m := range ms {
		line := m.Fact
		if m.Person != person {
			line += " · " + m.Person + "'s, shared"
		}
		lines = append(lines, line)
	}
	for _, sum := range ss {
		lines = append(lines, sum.At.In(loc).Format("Mon 2 Jan 15:04")+" · "+sum.Text)
	}
	return lines
}

func counted(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// people is a JSON array of person ids as a list a sentence can hold.
func people(raw string) string {
	var ids []string
	if json.Unmarshal([]byte(raw), &ids) != nil || len(ids) == 0 {
		return "nobody"
	}
	return strings.Join(ids, ", ")
}

// announcedBecause says why an announcement was made, in a note.
func announcedBecause(f map[string]string) string {
	note := "asked for by " + f["requested_by"] + " from " + f["from_satellite"]
	if f["source"] == "timer" {
		note = "timer " + f["timer_id"] + " went off"
	}
	if f["start_conversation"] == "true" {
		note += " · listening for an answer"
	}
	return note
}

func timerName(label string) string {
	if label == "" {
		return "a timer"
	}
	return "the " + label + " timer"
}

// seconds is a timer's length as a person would say it.
func seconds(n string) string {
	s, err := strconv.Atoi(n)
	if err != nil {
		return n + " s"
	}
	return (time.Duration(s) * time.Second).String()
}

// timersOf is what the house log says of conversation id: the timers set in
// it, and the one going off that opened it.
func timersOf(r *http.Request, store journal.Store, id string) ([]journal.Event, error) {
	events, err := store.Events(r.Context(), houseLog)
	if err != nil {
		return nil, err
	}
	var out []journal.Event
	for _, e := range events {
		if e.Fields["conversation_id"] == id {
			out = append(out, e)
		}
	}
	return out, nil
}

// houseRowOf is a house log event among a conversation's own, anchored
// apart from them.
func houseRowOf(e journal.Event, start journal.Event) logRow {
	row := logRowOf(e, start, nil)
	row.Anchor, row.Ref = fmt.Sprintf("house-%d", e.Seq), fmt.Sprintf("house #%d", e.Seq)
	return row
}

func (s *server) conversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	events, err := s.journal.Events(r.Context(), id)
	var (
		sigs, positives []triage.Signal
		pairs           []pair
		house           []journal.Event
		annos           map[uint64]curation.Annotation
	)
	if err == nil && len(events) > 0 {
		sigs, positives, err = triage.ScanAll(r.Context(), s.journal, id)
	}
	if err == nil && len(events) > 0 && id != houseLog {
		house, err = timersOf(r, s.journal, id)
	}
	if err == nil && len(events) > 0 {
		pairs, err = s.pairs(r.Context(), nil)
	}
	if err == nil && len(events) > 0 {
		annos, err = s.decisions.Annotations(r.Context(), id)
	}
	var wakes map[uint64]curation.WakeVerdict
	if err == nil && len(events) > 0 && strings.HasPrefix(id, devicePrefix) {
		wakes, err = s.decisions.WakeVerdicts(r.Context(), id)
	}
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(events) == 0 {
		http.NotFound(w, r)
		return
	}

	bySeq := map[uint64]triage.Signal{}
	again := map[uint64]triage.Signal{}
	for _, sig := range append(positives, sigs...) {
		bySeq[sig.Seq] = sig
		if sig.Kind == triage.KindRepeated {
			again[sig.First] = sig
		}
	}
	c := summarize(id, events, sigs)
	rows := make([]logRow, 0, len(events)+len(house))
	lc := logContext{loc: s.now().Location()}
	for _, e := range events {
		for len(house) > 0 && !house[0].At.After(e.At) {
			rows = append(rows, houseRowOf(house[0], events[0]))
			house = house[1:]
		}
		row := logRowOf(e, events[0], &lc)
		lc.see(e)
		if e.Kind == journal.KindUtteranceTranscribed {
			a, ok := annos[e.Seq]
			if !ok {
				a = curation.Annotation{ConversationID: id, Seq: e.Seq}
			}
			v := annotationView(id, e.Seq, a)
			if r, ok := again[e.Seq]; ok {
				v.Again = againNote(r)
			}
			row.Annotation = &v
		}
		if e.Kind == journal.KindWakeRejected && wakes != nil {
			n := negative{Negative: harvest.Negative{Seq: e.Seq, Reason: e.Fields["reason"]}, status: wakes[e.Seq].Status}
			v := wakeVerdictView(id, n)
			row.Wake = &v
		}
		if sig, ok := bySeq[e.Seq]; ok {
			tag := signalTags[sig.Kind]
			row.Signal, row.SignalWhy = &tag, sig.Detail
			if sig.Kind == triage.KindRepeated {
				row.Again = &ui.Button{Label: "Label the first answer ›", Href: fmt.Sprintf("#turn-%d-labels", sig.First)}
			}
		}
		rows = append(rows, row)
	}
	for _, e := range house {
		rows = append(rows, houseRowOf(e, events[0]))
	}

	loc := s.now().Location()
	var rooms []string
	for _, ses := range c.sessions {
		if len(rooms) == 0 || rooms[len(rooms)-1] != ses.satellite {
			rooms = append(rooms, ses.satellite)
		}
	}
	actions := []ui.Button{{Label: "‹ The day", Href: ui.Routes[ui.StepBrowse] + "?day=" + c.start.In(loc).Format("2006-01-02")}}
	if c.turns > 0 {
		// An announcement nobody answered has no turn to ask again.
		actions = append(actions, ui.Button{Label: "Replay ›", Href: replayHref(id)})
	}
	title := c.first
	if title == "" {
		title = id
	}
	s.render(w, "page-conversation", map[string]any{
		"Doc":    s.doc("Conversation · " + id),
		"Header": ui.NewAppHeader(ui.StepBrowse, unreviewedCount(pairs), "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "01 · Browse", Trace: true, Title: title, SubtitleMono: true,
			Subtitle: fmt.Sprintf("%s · %s · %s · %s", id, c.who(), strings.Join(rooms, " → "), c.start.In(loc).Format("Mon 2 Jan 15:04")),
			Actions:  actions,
		},
		"Rows": rows,
	})
}

// failedBecause is what a failure reported, and the canned line said for it
// (ADR-0051).
func failedBecause(f map[string]string) string {
	var parts []string
	if f["error"] != "" {
		parts = append(parts, f["error"])
	}
	if f["canned_call_id"] != "" {
		parts = append(parts, "apologised in "+f["canned_call_id"])
	}
	return strings.Join(parts, " · ")
}
