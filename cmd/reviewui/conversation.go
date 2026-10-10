package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/reviewui/ui"
	"github.com/teaganglenn/chorus/internal/triage"
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
	Audio   string

	// Signal tags the event Triage raised on; SignalWhy says why, in the
	// text column where a sentence fits.
	Signal    *ui.SigTag
	SignalWhy string
}

var kindTones = map[journal.Kind]ui.Tone{
	journal.KindSessionOpened: ui.ToneConv, journal.KindSessionClosed: ui.ToneConv,
	journal.KindUtteranceTranscribed: ui.TonePeople, journal.KindBargeInDetected: ui.TonePeople,
	journal.KindBargeInRejected: ui.ToneMuted, journal.KindWakeRejected: ui.ToneMuted,
	journal.KindSpeechStarted: ui.ToneVoice, journal.KindSpeechSpoken: ui.ToneVoice, journal.KindSpeechTruncated: ui.ToneVoice, journal.KindSpeechDiscarded: ui.ToneVoice,
	journal.KindToolCalled: ui.ToneHome, journal.KindToolResult: ui.ToneHome, journal.KindModelCompleted: ui.ToneMuted,
	journal.KindAnnouncementMade: ui.ToneVoice,
	journal.KindTimerStarted:     ui.ToneHome, journal.KindTimerCancelled: ui.ToneHome, journal.KindTimerFinished: ui.ToneHome,
}

// logRowOf says what one event means in a line, with its audio when the
// event has some. A truncation plays only what the DAC reached.
func logRowOf(e journal.Event, start journal.Event) logRow {
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
	case journal.KindBargeInDetected:
		row.Text = "barge-in at " + f["tts_position_ms"] + " ms of playback"
	case journal.KindBargeInRejected:
		row.Text = "barge-in rejected at " + f["stage"]
	case journal.KindWakeRejected:
		row.Text = "wake rejected: " + f["reason"]
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
	}
	return row
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
	row := logRowOf(e, start)
	row.Anchor, row.Ref = fmt.Sprintf("house-%d", e.Seq), fmt.Sprintf("house #%d", e.Seq)
	return row
}

func (s *server) conversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	events, err := s.journal.Events(r.Context(), id)
	var (
		sigs  []triage.Signal
		pairs []pair
		house []journal.Event
	)
	if err == nil && len(events) > 0 {
		sigs, err = triage.Scan(r.Context(), s.journal, id)
	}
	if err == nil && len(events) > 0 && id != houseLog {
		house, err = timersOf(r, s.journal, id)
	}
	if err == nil && len(events) > 0 {
		pairs, err = s.pairs(r.Context())
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
	for _, sig := range sigs {
		bySeq[sig.Seq] = sig
	}
	c := summarize(id, events, sigs)
	rows := make([]logRow, 0, len(events)+len(house))
	for _, e := range events {
		for len(house) > 0 && !house[0].At.After(e.At) {
			rows = append(rows, houseRowOf(house[0], events[0]))
			house = house[1:]
		}
		row := logRowOf(e, events[0])
		if sig, ok := bySeq[e.Seq]; ok {
			tag := signalTags[sig.Kind]
			row.Signal, row.SignalWhy = &tag, sig.Detail
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
