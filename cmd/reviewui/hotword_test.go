package main

import (
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// Teagan's "stop" over the forecast, the television's refused one, and her
// stop itself: the log says which hot phrase each was, so a reviewer can
// tell a stop from a correction without listening to every clip.
//
// verifies SPEC §4.3, §9.2
func TestTheLogSaysWhichHotPhraseEachWas(t *testing.T) {
	start := journal.Event{Seq: 1, Kind: journal.KindSessionOpened, At: thursday.Add(7 * time.Hour)}
	at := start.At.Add(4 * time.Second)
	for _, c := range []struct {
		e    journal.Event
		text string
		note string
	}{
		{
			journal.Event{Seq: 9, Kind: journal.KindBargeInDetected, At: at, Fields: map[string]string{"tts_position_ms": "1300", "hot_word": "stop"}},
			"barge-in at 1300 ms of playback", "hot phrase: stop",
		},
		{
			journal.Event{Seq: 8, Kind: journal.KindBargeInRejected, At: at, Fields: map[string]string{"stage": "speaker_id", "hot_word": "stop"}},
			"barge-in rejected at speaker_id", "hot phrase: stop",
		},
		{
			journal.Event{Seq: 11, Kind: journal.KindUtteranceTranscribed, At: at, Fields: map[string]string{"text": "Never mind.", "speaker_id": "teagan", "hot_word": "never_mind"}},
			"Never mind.", "hot phrase: never mind · the model was not asked",
		},
		{
			journal.Event{Seq: 12, Kind: journal.KindUtteranceTranscribed, At: at, Fields: map[string]string{"text": "Stop the music in the kitchen.", "speaker_id": "teagan"}},
			"Stop the music in the kitchen.", "",
		},
	} {
		row := logRowOf(c.e, start, nil)
		if row.Text != c.text || row.Note != c.note {
			t.Errorf("%s row = %q · %q, want %q · %q", c.e.Kind, row.Text, row.Note, c.text, c.note)
		}
		if c.note != "" && !strings.HasPrefix(row.Note, "hot phrase") {
			t.Errorf("%s note %q does not lead with the phrase", c.e.Kind, row.Note)
		}
	}
}
