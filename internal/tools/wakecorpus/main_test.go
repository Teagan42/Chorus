package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
)

var update = flag.Bool("update", false, "rewrite testdata/manifest.golden.json from this run")

type stepClock struct{ now time.Time }

func (c *stepClock) Now() time.Time { return c.now }

func rec(kind journal.Kind, audio string, fields ...string) journal.Record {
	r := journal.Record{Kind: kind, AudioRef: audio, Fields: map[string]string{}}
	for i := 0; i+1 < len(fields); i += 2 {
		r.Fields[fields[i]] = fields[i+1]
	}
	return r
}

// tone is s seconds of a 220 Hz hum at device format, so a clip is audio
// with a known length and never silence.
func tone(s float64) []byte {
	n := int(s * 16000)
	out := make([]byte, 0, 2*n)
	for i := range n {
		v := int16(8000 * math.Sin(2*math.Pi*220*float64(i)/16000))
		out = binary.LittleEndian.AppendUint16(out, uint16(v))
	}
	return out
}

// thursday is two satellites' own logs and the clips they kept: what the
// review UI reads the wake corpus from.
type thursday struct {
	store journal.Store
	blobs *blob.Memory
}

func put(t *testing.T, b *blob.Memory, key string, pcm []byte) {
	t.Helper()
	w, err := b.Create(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(pcm); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(); err != nil {
		t.Fatal(err)
	}
}

// household is the kitchen's and the office's Thursday. The kitchen woke on
// the dishwasher, on two guests, on the television (whose clip retention
// later pruned), on a wake the office answered, on Bob's button and on a
// wake the ear failed on. The office woke on a podcast whose clip is gone
// from the store, on a cough, and on a radio a reviewer discarded.
func household(t *testing.T) thursday {
	t.Helper()
	store := journal.NewMemStore()
	clk := &stepClock{}
	j := journal.New(store, clk, journal.Versions{
		Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7",
		STT: "istupakov/parakeet-tdt-0.6b-v2-onnx", TTS: "kokoro/af_heart",
	})
	day := time.Date(2025, time.October, 9, 0, 0, 0, 0, time.UTC)
	logs := []struct {
		conv string
		at   time.Duration
		r    journal.Record
	}{
		{"device:kitchen", 12*time.Hour + 8*time.Minute, rec(journal.KindPresenceChanged, "", "state", "present", "sensor", "room_presence")},
		{"device:kitchen", 14*time.Hour + 2*time.Minute, rec(journal.KindWakeRejected, "blob://wake/kitchen-dishwasher", "reason", "no_speech", "wake_word", "hey_eddie", "second_audio_ref", "blob://wake/kitchen-dishwasher.ch1")},
		{"device:kitchen", 19*time.Hour + 2*time.Minute, rec(journal.KindWakeRejected, "blob://wake/kitchen-guest", "reason", "unknown_speaker", "wake_word", "hey_eddie")},
		{"device:kitchen", 19*time.Hour + 40*time.Minute, rec(journal.KindWakeRejected, "blob://wake/kitchen-guest-2", "reason", "unknown_speaker", "wake_word", "hey_eddie")},
		{"device:kitchen", 20*time.Hour + 15*time.Minute, rec(journal.KindWakeRejected, "blob://wake/kitchen-tv", "reason", "low_confidence", "wake_word", "hey_eddie")},
		{"device:kitchen", 20*time.Hour + 16*time.Minute, rec(journal.KindWakeRejected, "blob://wake/kitchen-lost", "reason", "arbitrated", "wake_word", "hey_eddie", "lost_to", "office")},
		{"device:kitchen", 21*time.Hour + 15*time.Minute, rec(journal.KindWakeRejected, "blob://wake/kitchen-press", "reason", "no_speech", "wake_word", "button")},
		{"device:kitchen", 22*time.Hour + 5*time.Minute, rec(journal.KindWakeRejected, "blob://wake/kitchen-oven", "reason", "transcription_failed", "wake_word", "hey_eddie", "canned_call_id", "cn_0a1b")},
		{"device:kitchen", 23 * time.Hour, rec(journal.KindAudioDropped, "", "audio_ref", "blob://wake/kitchen-tv", "reason", "retention")},
		{"device:office", 7*time.Hour + 42*time.Minute, rec(journal.KindWakeRejected, "blob://wake/office-podcast", "reason", "no_speech", "wake_word", "hey_eddie")},
		{"device:office", 9*time.Hour + 30*time.Minute, rec(journal.KindWakeRejected, "blob://wake/office-cough", "reason", "no_speech", "wake_word", "hey_eddie", "second_audio_ref", "blob://wake/office-cough.ch1")},
		{"device:office", 16*time.Hour + 5*time.Minute, rec(journal.KindWakeRejected, "blob://wake/office-radio", "reason", "low_confidence", "wake_word", "hey_eddie")},
	}
	for _, l := range logs {
		clk.now = day.Add(l.at)
		if _, err := j.Append(context.Background(), l.conv, l.r); err != nil {
			t.Fatalf("append %s: %v", l.r.Kind, err)
		}
	}
	b := blob.NewMemory()
	for key, s := range map[string]float64{
		"wake/kitchen-dishwasher": 1.5, "wake/kitchen-dishwasher.ch1": 1.5,
		"wake/kitchen-guest": 2.25, "wake/kitchen-guest-2": 1.75,
		"wake/kitchen-lost": 1.0, "wake/kitchen-press": 1.0, "wake/kitchen-oven": 2.0,
		"wake/office-cough": 0.75, "wake/office-cough.ch1": 0.75, "wake/office-radio": 3.0,
	} {
		put(t, b, key, tone(s))
	}
	return thursday{store: store, blobs: b}
}

// exported is the wake corpus as the review UI ships it after a reviewer
// confirmed the first guest and discarded the office's radio: every hard
// negative but a held one nobody confirmed and a discarded one (ADR-0058).
func exported(t *testing.T, h thursday) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, conv := range []string{"device:kitchen", "device:office"} {
		ns, err := harvest.Negatives(context.Background(), h.store, conv)
		if err != nil {
			t.Fatal(err)
		}
		var ships []harvest.Negative
		for _, n := range ns {
			switch n.Audio {
			case "blob://wake/kitchen-guest":
				n.Confirmed = true
			case "blob://wake/kitchen-guest-2", "blob://wake/office-radio":
				continue
			}
			ships = append(ships, n)
		}
		if err := harvest.ExportNegatives(&buf, ships); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func defaults() config { return config{Channel: channelProcessed, Validation: 0.1, Testing: 0.1} }

func assembleTo(t *testing.T, export []byte, clips harvest.Clips, cfg config) (manifest, string) {
	t.Helper()
	out := t.TempDir()
	m, err := assemble(context.Background(), export, "chorus-wake-negatives.jsonl", clips, out, cfg)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return m, out
}

// The household's Thursday becomes a corpus the trainer reads, and the
// manifest names the journal row behind every clip and why each other row
// stayed out: a gone clip, one missing from the store, and none of the
// rejections HardNegative refuses (SPEC §9.3, ADR-0058, ADR-0072).
//
// verifies SPEC §9.3
func TestTheCorpusFromAHouseholdsThursdayMatchesItsGoldenManifest(t *testing.T) {
	h := household(t)
	export := exported(t, h)
	for _, never := range []string{"kitchen-lost", "kitchen-press", "kitchen-oven", "kitchen-guest-2", "office-radio"} {
		if bytes.Contains(export, []byte(never)) {
			t.Fatalf("the review UI's export carries %s, which it never ships:\n%s", never, export)
		}
	}
	// Held-out fractions wide enough that three clips reach all three splits.
	m, out := assembleTo(t, export, h.blobs, config{Channel: channelProcessed, Validation: 0.2, Testing: 0.45})

	got, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	golden := filepath.Join("testdata", "manifest.golden.json")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/tools/wakecorpus -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("manifest =\n%s\nwant\n%s", got, want)
	}
	onDisk, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, got) {
		t.Errorf("manifest.json on disk differs from the one returned:\n%s", onDisk)
	}

	ids := map[string]string{}
	for _, c := range m.Clips {
		ids[c.ID] = c.Set
	}
	want3 := map[string]string{"device:kitchen/2": setAuto, "device:kitchen/3": setConfirmed, "device:office/2": setAuto}
	if fmt.Sprint(ids) != fmt.Sprint(want3) {
		t.Errorf("clips = %v, want the dishwasher, the confirmed guest and the cough", ids)
	}
	skipped := map[string]string{}
	for _, s := range m.Skipped {
		skipped[s.ID] = s.Why
	}
	if skipped["device:kitchen/5"] != whyAudioGone || skipped["device:office/1"] != whyAudioMissing || len(skipped) != 2 {
		t.Errorf("skipped = %v, want the pruned television and the podcast missing from the store", skipped)
	}
}

// A clip lands in the trainer's layout as the WAV microWakeWord reads:
// 16 kHz, 16-bit, mono, holding exactly the PCM the store kept.
//
// verifies SPEC §9.3
func TestAClipIsWrittenAsA16kHzMonoWAVOfItsPCM(t *testing.T) {
	h := household(t)
	m, out := assembleTo(t, exported(t, h), h.blobs, defaults())
	for _, c := range m.Clips {
		wav, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(c.Path)))
		if err != nil {
			t.Fatal(err)
		}
		pcm, ok := h.blobs.Bytes(c.Audio)
		if !ok {
			t.Fatalf("%s names %s, which the store does not hold", c.ID, c.Audio)
		}
		f := readWAV(t, wav)
		if f.rate != 16000 || f.channels != 1 || f.bits != 16 || f.format != 1 {
			t.Errorf("%s: %d Hz, %d channel(s), %d-bit, format %d; want 16000 Hz mono 16-bit PCM", c.Path, f.rate, f.channels, f.bits, f.format)
		}
		if !bytes.Equal(f.data, pcm) {
			t.Errorf("%s holds %d bytes of audio, want the %d the store kept", c.Path, len(f.data), len(pcm))
		}
		if want := float64(len(pcm)) / 32000; c.Seconds != want {
			t.Errorf("%s: seconds = %v, want %v", c.ID, c.Seconds, want)
		}
		if !strings.HasPrefix(c.Path, "negatives/"+c.Set+"/"+c.Split+"/") {
			t.Errorf("%s is at %s, outside negatives/%s/%s", c.ID, c.Path, c.Set, c.Split)
		}
	}
}

type wavFile struct {
	format, channels, bits uint16
	rate                   uint32
	data                   []byte
}

func readWAV(t *testing.T, b []byte) wavFile {
	t.Helper()
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:16]) != "WAVEfmt " || string(b[36:40]) != "data" {
		t.Fatalf("not a canonical PCM WAV: % x", b[:min(len(b), 44)])
	}
	if got := binary.LittleEndian.Uint32(b[4:]); int(got) != len(b)-8 {
		t.Errorf("RIFF size %d, want %d", got, len(b)-8)
	}
	n := binary.LittleEndian.Uint32(b[40:])
	if int(n) != len(b)-44 {
		t.Fatalf("data size %d, file holds %d", n, len(b)-44)
	}
	return wavFile{
		format: binary.LittleEndian.Uint16(b[20:]), channels: binary.LittleEndian.Uint16(b[22:]),
		rate: binary.LittleEndian.Uint32(b[24:]), bits: binary.LittleEndian.Uint16(b[34:]), data: b[44:],
	}
}

// Retraining may target the XMOS's second output (SPEC §9.3). Then each clip
// is that channel, and a rejection from a one-channel device stays out.
//
// verifies SPEC §9.3
func TestTheSecondChannelCanBeTheCorpus(t *testing.T) {
	h := household(t)
	cfg := defaults()
	cfg.Channel = channelSecond
	m, _ := assembleTo(t, exported(t, h), h.blobs, cfg)
	audio := map[string]string{}
	for _, c := range m.Clips {
		audio[c.ID] = c.Audio
	}
	if audio["device:kitchen/2"] != "blob://wake/kitchen-dishwasher.ch1" || audio["device:office/2"] != "blob://wake/office-cough.ch1" || len(audio) != 2 {
		t.Errorf("clips = %v, want the second channel of the dishwasher and the cough", audio)
	}
	for _, s := range m.Skipped {
		if s.ID == "device:kitchen/3" && s.Why != whyNoSecondChannel {
			t.Errorf("the guest, kept on one channel, is skipped as %q", s.Why)
		}
	}
}

// An export from an older review UI, or one edited by hand, may carry a row
// the rules keep out of the corpus. Each is refused again here, by the same
// rules the review UI ships by, rather than trusted (ADR-0058, ADR-0060,
// ADR-0072).
//
// verifies SPEC §9.3
func TestARowTheCorpusRulesRefuseIsSkippedNotTrusted(t *testing.T) {
	h := household(t)
	forged := []string{
		`{"id":"device:kitchen/6","label":0,"source":"stage2_reject","reason":"arbitrated","wake_word":"hey_eddie","satellite":"kitchen","at":"2025-10-09T20:16:00Z","audio":"blob://wake/kitchen-lost","second_audio":"","confirmed":false,"conversation_id":"device:kitchen","seq":6}`,
		`{"id":"device:kitchen/7","label":0,"source":"stage2_reject","reason":"no_speech","wake_word":"button","satellite":"kitchen","at":"2025-10-09T21:15:00Z","audio":"blob://wake/kitchen-press","second_audio":"","confirmed":false,"conversation_id":"device:kitchen","seq":7}`,
		`{"id":"device:kitchen/8","label":0,"source":"stage2_reject","reason":"transcription_failed","wake_word":"hey_eddie","satellite":"kitchen","at":"2025-10-09T22:05:00Z","audio":"blob://wake/kitchen-oven","second_audio":"","confirmed":false,"conversation_id":"device:kitchen","seq":8}`,
		`{"id":"device:kitchen/4","label":0,"source":"stage2_reject","reason":"unknown_speaker","wake_word":"hey_eddie","satellite":"kitchen","at":"2025-10-09T19:40:00Z","audio":"blob://wake/kitchen-guest-2","second_audio":"","confirmed":false,"conversation_id":"device:kitchen","seq":4}`,
	}
	m, _ := assembleTo(t, []byte(strings.Join(forged, "\n")+"\n"), h.blobs, defaults())
	if len(m.Clips) != 0 {
		t.Errorf("clips = %+v, want none of them", m.Clips)
	}
	got := map[string]string{}
	for _, s := range m.Skipped {
		got[s.ID] = s.Why
	}
	want := map[string]string{
		"device:kitchen/6": whyNotHardNegative, "device:kitchen/7": whyNotHardNegative,
		"device:kitchen/8": whyNotHardNegative, "device:kitchen/4": whyHeldUnconfirmed,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("skipped = %v, want %v", got, want)
	}
}

// A file that is not the wake corpus is refused whole: a positive, a row
// from another source, a row naming no satellite's log, or a clip that is
// not 16-bit audio would each put the wrong thing in front of the trainer.
//
// verifies SPEC §9.3
func TestAFileThatIsNotTheWakeCorpusIsRefused(t *testing.T) {
	h := household(t)
	put(t, h.blobs, "wake/odd", []byte{1, 2, 3})
	row := func(edit string) string {
		base := `{"id":"device:office/2","label":0,"source":"stage2_reject","reason":"no_speech","wake_word":"hey_eddie","satellite":"office","at":"2025-10-09T09:30:00Z","audio":"blob://wake/office-cough","second_audio":"","confirmed":false,"conversation_id":"device:office","seq":2}`
		return strings.Replace(base, `"label":0`, edit, 1) + "\n"
	}
	for name, export := range map[string]string{
		"a positive":      row(`"label":1`),
		"another source":  strings.Replace(row(`"label":0`), "stage2_reject", "barge_in", 1),
		"no device log":   strings.Replace(row(`"label":0`), `"conversation_id":"device:office"`, `"conversation_id":"conv-1"`, 1),
		"the same row":    row(`"label":0`) + row(`"label":0`),
		"not json":        "id,label\n",
		"odd-length clip": strings.Replace(row(`"label":0`), "blob://wake/office-cough", "blob://wake/odd", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := assemble(context.Background(), []byte(export), "x.jsonl", h.blobs, t.TempDir(), defaults()); err == nil {
				t.Errorf("assembled %q without complaint", export)
			}
		})
	}
}

// A clip's split depends on its satellite and hour alone, so the same
// export always splits the same way, a television's run of false wakes
// stays on one side, and a larger held-out fraction only moves clips into
// it (SPEC §9.3).
//
// verifies SPEC §9.3
func TestTheSplitIsDeterministicByHourAndOnlyGrows(t *testing.T) {
	small, large := config{Validation: 0.1, Testing: 0.1}, config{Validation: 0.1, Testing: 0.3}
	counts := map[string]int{}
	for i := range 2000 {
		at := time.Date(2025, time.October, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
		s := small.split("kitchen", at.Format(time.RFC3339))
		counts[s]++
		if again := small.split("kitchen", at.Add(59*time.Minute).Format(time.RFC3339)); again != s {
			t.Fatalf("%s: %s, but %s later in the same hour", at, s, again)
		}
		if s == splitTesting && large.split("kitchen", at.Format(time.RFC3339)) != splitTesting {
			t.Fatalf("%s left testing when the testing fraction grew", at)
		}
	}
	for split, want := range map[string]float64{splitTraining: 0.8, splitValidation: 0.1, splitTesting: 0.1} {
		if got := float64(counts[split]) / 2000; math.Abs(got-want) > 0.03 {
			t.Errorf("%s holds %.3f of the hours, want about %.2f", split, got, want)
		}
	}
	if small.split("kitchen", "2025-10-09T14:02:00Z") == "" {
		t.Error("no split")
	}
}

// Assembling again replaces the last corpus, so a clip a reviewer has since
// discarded leaves it; a directory that holds anything else is refused.
//
// verifies SPEC §9.3
func TestAssemblingAgainReplacesTheCorpusAndNothingElse(t *testing.T) {
	h := household(t)
	out := t.TempDir()
	if _, err := assemble(context.Background(), exported(t, h), "a.jsonl", h.blobs, out, defaults()); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(out, "negatives", "auto", "training", "gone.wav")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := assemble(context.Background(), exported(t, h), "a.jsonl", h.blobs, out, defaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a clip from an older corpus survived: %v", err)
	}

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "notes.txt"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := assemble(context.Background(), exported(t, h), "a.jsonl", h.blobs, other, defaults()); err == nil {
		t.Error("assembled into a directory that holds something other than a corpus")
	}
}

// The fractions and the channel are checked before anything is written.
//
// verifies SPEC §9.3
func TestABadConfigIsRefused(t *testing.T) {
	for _, cfg := range []config{
		{Channel: "raw", Validation: 0.1, Testing: 0.1},
		{Channel: channelProcessed, Validation: -0.1, Testing: 0.1},
		{Channel: channelProcessed, Validation: 0.5, Testing: 0.5},
	} {
		if err := cfg.check(); err == nil {
			t.Errorf("%+v passed", cfg)
		}
	}
	if err := defaults().check(); err != nil {
		t.Errorf("the defaults fail: %v", err)
	}
}
