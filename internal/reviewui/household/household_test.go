package household_test

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// cited is every audio-bearing event in the day, by blob ref.
func cited(t *testing.T) map[string]household.Line {
	t.Helper()
	out := map[string]household.Line{}
	for _, lines := range household.Logs() {
		for _, l := range lines {
			if l.Rec.AudioRef != "" {
				out[strings.TrimPrefix(l.Rec.AudioRef, "blob://")] = l
			}
			if ref := l.Rec.Fields["second_audio_ref"]; ref != "" {
				out[strings.TrimPrefix(ref, "blob://")] = l
			}
		}
	}
	return out
}

func clips(t *testing.T) []household.Clip {
	t.Helper()
	cs, err := household.Clips()
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// pcm reads a clip back out of the blob store as samples in [-1, 1].
func pcm(t *testing.T, ref string) []float64 {
	t.Helper()
	blobs, err := household.Blobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rc, err := blobs.Open(context.Background(), "blob://"+ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float64, len(b)/2)
	for i := range out {
		out[i] = float64(int16(binary.LittleEndian.Uint16(b[2*i:]))) / 32768
	}
	return out
}

func rms(x []float64) float64 {
	var s float64
	for _, v := range x {
		s += v * v
	}
	return math.Sqrt(s / float64(max(1, len(x))))
}

// The journal and the recordings agree: every ref an event cites has a clip,
// and no clip sits in the store that nothing cites.
func TestEveryCitedRecordingExistsAndNothingElse(t *testing.T) {
	refs := cited(t)
	scripted := map[string]bool{}
	for _, c := range clips(t) {
		scripted[c.Ref] = true
		if _, ok := refs[c.Ref]; !ok {
			t.Errorf("clip %s is cited by no event", c.Ref)
		}
	}
	for ref := range refs {
		if !scripted[ref] {
			t.Errorf("an event cites %s, which voice.json does not script", ref)
		}
	}
}

// Each clip is device audio of exactly the scripted length, and it is not
// silence: the demo's play buttons have something to play.
func TestEveryClipIsTheScriptedLengthAndAudible(t *testing.T) {
	for _, c := range clips(t) {
		x := pcm(t, c.Ref)
		if len(x) != c.Frames() {
			t.Errorf("%s holds %d frames, want %d (%.2f s at 16 kHz)", c.Ref, len(x), c.Frames(), c.Seconds)
		}
		if r := rms(x); r < 0.01 {
			t.Errorf("%s is silent (rms %.4f)", c.Ref, r)
		}
	}
}

// The script says what the journal heard: a transcript is what its speaker
// said, and a spoken answer is the text the turn recorded.
func TestTheVoicesSayWhatTheJournalRecorded(t *testing.T) {
	refs := cited(t)
	for _, c := range clips(t) {
		f := refs[c.Ref].Rec.Fields
		switch refs[c.Ref].Rec.Kind {
		case journal.KindUtteranceTranscribed:
			if c.Say != f["text"] || c.Who != f["speaker_id"] {
				t.Errorf("%s: %s says %q, journal heard %s say %q", c.Ref, c.Who, c.Say, f["speaker_id"], f["text"])
			}
		case journal.KindSpeechSpoken:
			if c.Who != "assistant" || c.Say != f["text"] {
				t.Errorf("%s: %s says %q, journal spoke %q", c.Ref, c.Who, c.Say, f["text"])
			}
		case journal.KindSpeechTruncated:
			if c.Say != f["spoken_text"] || c.Unheard != f["unspoken_text"] || strconv.Itoa(c.Heard) != f["frames_played"] {
				t.Errorf("%s: scripted %q | %q cut at %d, journal %q | %q cut at %s",
					c.Ref, c.Say, c.Unheard, c.Heard, f["spoken_text"], f["unspoken_text"], f["frames_played"])
			}
		}
	}
}

// A cut answer stops between words: the halves are spoken apart, so the
// moment the DAC stopped is quiet, and both halves have speech in them.
func TestACutAnswerStopsBetweenWords(t *testing.T) {
	var n int
	for _, c := range clips(t) {
		if c.Unheard == "" {
			continue
		}
		n++
		x := pcm(t, c.Ref)
		gap := x[c.Heard : c.Heard+household.DeviceRate/50] // 20 ms after the cut
		if r := rms(gap); r > 0.005 {
			t.Errorf("%s: the cut at frame %d lands mid-word (rms %.4f)", c.Ref, c.Heard, r)
		}
		if rms(x[:c.Heard]) < 0.01 || rms(x[c.Heard:]) < 0.01 {
			t.Errorf("%s: one side of the cut is silent", c.Ref)
		}
	}
	if n != 3 {
		t.Errorf("%d cut answers, want the weather, Zeppelin and jazz cuts", n)
	}
}

// The barge-in is detected as far into playback as the DAC got.
func TestEveryBargeInIsDetectedWhereTheCutLands(t *testing.T) {
	for id, lines := range household.Logs() {
		for i, l := range lines {
			if l.Rec.Kind != journal.KindBargeInDetected {
				continue
			}
			cut := lines[i+1].Rec
			frames, _ := strconv.Atoi(cut.Fields["frames_played"])
			if want := strconv.Itoa(frames * 1000 / household.DeviceRate); l.Rec.Fields["tts_position_ms"] != want {
				t.Errorf("%s: barge-in at %s ms, but the DAC cut at %s ms", id, l.Rec.Fields["tts_position_ms"], want)
			}
		}
	}
}

// The kitchen's Satellite1 streams both XMOS outputs, so the dishwasher it
// rejected is kept on both channels: the corpus can target either (SPEC §9.3).
func TestTheKitchensRejectedWakeKeepsBothChannels(t *testing.T) {
	var second string
	for _, l := range household.Logs()["device:kitchen"] {
		if l.Rec.Kind == journal.KindWakeRejected {
			second = l.Rec.Fields["second_audio_ref"]
		}
	}
	if second != "blob://wake/kitchen-dishwasher-second" {
		t.Fatalf("the dishwasher's second channel is %q", second)
	}
	if a, b := pcm(t, "wake/kitchen-dishwasher"), pcm(t, "wake/kitchen-dishwasher-second"); len(a) != len(b) {
		t.Errorf("the two channels hold %d and %d frames of the same span", len(a), len(b))
	}
}

// Every answer the household heard says when it was first heard, once per
// turn, naming the turn's first speak call and how long its person waited:
// since the transcript, plus the endpointer's pause before it (ADR-0035).
// An announcement answers nobody, so it records no start.
//
// verifies SPEC §11
func TestEveryAnswerSaysWhenItWasFirstHeard(t *testing.T) {
	for id, lines := range household.Logs() {
		var (
			asked     time.Duration
			first     string
			started   []household.Line
			announced bool
		)
		check := func() {
			switch {
			case first == "" && len(started) > 0:
				t.Errorf("%s: a turn nobody answered aloud records %d starts", id, len(started))
			case first == "":
			case len(started) != 1:
				t.Errorf("%s: the turn %s answered records %d starts, want 1", id, first, len(started))
			default:
				f := started[0].Rec.Fields
				if f["call_id"] != first {
					t.Errorf("%s: first audio names %s, want the turn's first speak call %s", id, f["call_id"], first)
				}
				want := (started[0].At - asked + household.Trailing).Round(time.Millisecond).Milliseconds()
				if f["wait_ms"] != strconv.FormatInt(want, 10) {
					t.Errorf("%s: %s waited %s ms, but its log says %d", id, first, f["wait_ms"], want)
				}
			}
		}
		for _, l := range lines {
			switch l.Rec.Kind {
			case journal.KindSessionOpened:
				announced = l.Rec.Fields["announced"] == "true"
			case journal.KindUtteranceTranscribed:
				check()
				asked, first, started = l.At, "", nil
			case journal.KindToolCalled:
				if l.Rec.Fields["tool"] == "speak" && first == "" && !announced {
					first = l.Rec.Fields["call_id"]
				}
			case journal.KindSpeechStarted:
				started = append(started, l)
			case journal.KindSpeechSpoken, journal.KindSpeechTruncated:
				if !announced && len(started) == 0 {
					t.Errorf("%s: %s is heard before its first frame is", id, l.Rec.AudioRef)
				}
			}
		}
		check()
	}
}

// Speech names the speak call it played, as the Speaking child records it,
// so the dialogue a re-run is told puts it where the model said it.
func TestSpeechNamesTheCallItPlayed(t *testing.T) {
	for id, lines := range household.Logs() {
		speaks := map[string]bool{}
		for _, l := range lines {
			f := l.Rec.Fields
			switch l.Rec.Kind {
			case journal.KindToolCalled:
				speaks[f["call_id"]] = f["tool"] == "speak"
			case journal.KindSpeechSpoken, journal.KindSpeechTruncated:
				if !speaks[f["call_id"]] {
					t.Errorf("%s: %s names call %q, which no earlier speak is", id, l.Rec.AudioRef, f["call_id"])
				}
			}
		}
	}
}
