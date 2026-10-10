package household_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/kokoro"
	"github.com/teagan42/chorus/internal/provider/speaches"
	"github.com/teagan42/chorus/internal/registry"
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

// Every event names the ear and the voice it was heard and spoken by, as
// chorusd records them from its providers (ADR-0032): the assistant speaks
// in the Kokoro voice voice.json gives it.
func TestTheDayNamesTheEarAndTheVoice(t *testing.T) {
	b, err := os.ReadFile("voice.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ Voices map[string]string }
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	want := household.Versions()
	if tts := kokoro.DefaultModel + "/" + s.Voices["assistant"]; want.STT != speaches.DefaultModel || want.TTS != tts {
		t.Errorf("the day ran under STT %q and TTS %q, want %q and %q", want.STT, want.TTS, speaches.DefaultModel, tts)
	}
	store, err := household.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(context.Background(), household.ConvWeather)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Versions != want {
			t.Fatalf("%s #%d ran under %+v, want %+v", e.Kind, e.Seq, e.Versions, want)
		}
	}
}

// The television talked over the living room's answer to Alice, and the
// gate refused it at speaker ID: it is no barge-in, and kept to tune the
// gate on (SPEC §4.3).
func TestTheTelevisionIsRefusedAtTheSpeakerGate(t *testing.T) {
	var playing bool
	var refused []household.Line
	for _, l := range household.Logs()[household.ConvJazz] {
		switch l.Rec.Kind {
		case journal.KindSpeechStarted:
			playing = true
		case journal.KindSpeechSpoken, journal.KindSpeechTruncated:
			playing = false
		case journal.KindBargeInRejected:
			if !playing {
				t.Errorf("a barge-in was refused at %v with nothing playing", l.At)
			}
			refused = append(refused, l)
		}
	}
	if len(refused) != 1 || refused[0].Rec.Fields["stage"] != "speaker_id" || refused[0].Rec.AudioRef != "blob://mic/jazz-tv" {
		t.Fatalf("refused = %+v, want the television at speaker_id", refused)
	}
	for _, c := range clips(t) {
		if c.Ref == "mic/jazz-tv" && c.Who != "tv" {
			t.Errorf("the refused clip is %s's voice, want the television's", c.Who)
		}
	}
}

// Teagan set a pasta timer and called it off before the water boiled: the
// house log says it was cancelled, by the call that cancelled it, and it
// never goes off.
func TestTheCancelledPastaTimerNeverGoesOff(t *testing.T) {
	calls := map[string]journal.Record{}
	for _, l := range household.Logs()[household.ConvPasta] {
		if l.Rec.Kind == journal.KindToolCalled {
			calls[l.Rec.Fields["call_id"]] = l.Rec
		}
	}
	var started, cancelled bool
	for _, l := range household.Logs()[journal.HouseTimers] {
		f := l.Rec.Fields
		if f["timer_id"] != household.PastaTimer {
			continue
		}
		switch l.Rec.Kind {
		case journal.KindTimerStarted:
			started = calls[f["call_id"]].Fields["tool"] == "timer_start" && f["conversation_id"] == household.ConvPasta
		case journal.KindTimerCancelled:
			c := calls[f["call_id"]]
			cancelled = c.Fields["tool"] == "timer_cancel" && strings.Contains(c.Fields["args_json"], household.PastaTimer) &&
				f["conversation_id"] == household.ConvPasta
		case journal.KindTimerFinished:
			t.Error("the cancelled pasta timer went off")
		}
	}
	if !started || !cancelled {
		t.Errorf("pasta timer started by its call %v, cancelled by its call %v", started, cancelled)
	}
}

// Every call the household's model made asks only for what its tool offered
// the model: a model shown no media_type cannot have asked for one. The
// session's own speak calls carry its fields, not the model's.
//
// verifies SPEC §3
func TestEveryCallAsksOnlyForWhatItsToolOffered(t *testing.T) {
	for id, lines := range household.Logs() {
		for _, l := range lines {
			f := l.Rec.Fields
			if l.Rec.Kind != journal.KindToolCalled || f["tool"] == "speak" {
				continue
			}
			spec, ok := registry.Specs[f["tool"]]
			if !ok {
				t.Errorf("%s: %s is no tool", id, f["tool"])
				continue
			}
			var args map[string]json.RawMessage
			if err := json.Unmarshal([]byte(f["args_json"]), &args); err != nil {
				t.Errorf("%s: %s %s: %v", id, f["call_id"], f["args_json"], err)
				continue
			}
			for name := range args {
				if !slices.ContainsFunc(spec.ModelParams, func(p registry.ParamSpec) bool { return p.Name == name }) {
					t.Errorf("%s: %s asks %s for %q, which it never offered", id, f["call_id"], f["tool"], name)
				}
			}
		}
	}
}

// Every slow call carries what to say while it works, and the session says
// it as chorusd journals it: a speak call of its own, right after the slow
// call, with the id <call>_ack, mode queue, and acknowledges naming the call.
// It plays out while the search runs, so the person hears it before the
// results land (ADR-0039).
//
// verifies SPEC §4.1, §11
func TestEverySlowCallIsAcknowledgedWhileItWorks(t *testing.T) {
	var n int
	for id, lines := range household.Logs() {
		for i, l := range lines {
			f := l.Rec.Fields
			if l.Rec.Kind != journal.KindToolCalled || !registry.Specs[f["tool"]].Slow {
				continue
			}
			n++
			o, err := registry.Split(f["args_json"])
			if err != nil || o.Acknowledgement == "" {
				t.Errorf("%s: %s %s carries no acknowledgement (%v)", id, f["call_id"], f["args_json"], err)
				continue
			}
			ack := f["call_id"] + "_ack"
			want, _ := json.Marshal(struct {
				Text         string `json:"text"`
				Mode         string `json:"mode"`
				Acknowledges string `json:"acknowledges"`
			}{o.Acknowledgement, "queue", f["call_id"]})
			if i+1 >= len(lines) {
				t.Errorf("%s: %s is the log's last event", id, f["call_id"])
				continue
			}
			next := lines[i+1]
			if g := next.Rec.Fields; next.Rec.Kind != journal.KindToolCalled || g["tool"] != "speak" ||
				g["call_id"] != ack || g["args_json"] != string(want) || next.At != l.At {
				t.Errorf("%s: after %s comes %s %v at %v, want speak %s %s as it is called", id, f["call_id"], next.Rec.Kind, g, next.At-l.At, ack, want)
			}
			var spoken, settled, landed time.Duration
			for _, m := range lines[i+1:] {
				g := m.Rec.Fields
				switch {
				case m.Rec.Kind == journal.KindSpeechSpoken && g["call_id"] == ack:
					if g["text"] != o.Acknowledgement {
						t.Errorf("%s: %s said %q, want %q", id, ack, g["text"], o.Acknowledgement)
					}
					spoken = m.At
				case m.Rec.Kind == journal.KindToolResult && g["call_id"] == ack && g["outcome"] == "ok":
					settled = m.At
				case m.Rec.Kind == journal.KindToolResult && g["call_id"] == f["call_id"]:
					landed = m.At
				}
			}
			if spoken == 0 || settled < spoken {
				t.Errorf("%s: %s never played out", id, ack)
			}
			if landed == 0 || landed < spoken {
				t.Errorf("%s: %s's results landed before its acknowledgement finished", id, f["call_id"])
			}
		}
	}
	if n != 3 {
		t.Errorf("%d slow calls, want Zeppelin's, jazz's and the quieter search", n)
	}
}
