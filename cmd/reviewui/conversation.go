package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/reviewui/ui"
	"github.com/teaganglenn/chorus/internal/triage"
)

// logRow is one journal event as the conversation page shows it.
type logRow struct {
	Anchor  string // seq-N, so Triage can link to the event
	Offset  string
	Seq     uint64
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
	journal.KindSpeechSpoken: ui.ToneVoice, journal.KindSpeechTruncated: ui.ToneVoice, journal.KindSpeechDiscarded: ui.ToneVoice,
	journal.KindToolCalled: ui.ToneHome, journal.KindToolResult: ui.ToneHome, journal.KindModelCompleted: ui.ToneMuted,
}

// logRowOf says what one event means in a line, with its audio when the
// event has some. A truncation plays only what the DAC reached.
func logRowOf(e journal.Event, start journal.Event) logRow {
	f := e.Fields
	row := logRow{
		Anchor: fmt.Sprintf("seq-%d", e.Seq), Seq: e.Seq,
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
	}
	return row
}

func (s *server) conversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	events, err := s.journal.Events(r.Context(), id)
	var (
		sigs  []triage.Signal
		pairs []pair
	)
	if err == nil && len(events) > 0 {
		sigs, err = triage.Scan(r.Context(), s.journal, id)
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
	rows := make([]logRow, 0, len(events))
	for _, e := range events {
		row := logRowOf(e, events[0])
		if sig, ok := bySeq[e.Seq]; ok {
			tag := signalTags[sig.Kind]
			row.Signal, row.SignalWhy = &tag, sig.Detail
		}
		rows = append(rows, row)
	}

	loc := s.now().Location()
	var rooms []string
	for _, ses := range c.sessions {
		if len(rooms) == 0 || rooms[len(rooms)-1] != ses.satellite {
			rooms = append(rooms, ses.satellite)
		}
	}
	title := c.first
	if title == "" {
		title = id
	}
	s.render(w, "page-conversation", map[string]any{
		"Doc":    ui.Doc{Title: "Conversation · " + id, Static: "/static"},
		"Header": ui.NewAppHeader(ui.StepBrowse, unreviewedCount(pairs), "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "01 · Browse", Trace: true, Title: title, SubtitleMono: true,
			Subtitle: fmt.Sprintf("%s · %s · %s · %s", id, c.speaker, strings.Join(rooms, " → "), c.start.In(loc).Format("Mon 2 Jan 15:04")),
			Actions: []ui.Button{
				{Label: "‹ The day", Href: ui.Routes[ui.StepBrowse] + "?day=" + c.start.In(loc).Format("2006-01-02")},
				{Label: "Replay ›", Href: replayHref(id)},
			},
		},
		"Rows": rows,
	})
}
