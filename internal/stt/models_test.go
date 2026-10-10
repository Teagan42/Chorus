//go:build models

// Model tier: the utterance stream end to end against a reachable speaches,
// fed a WAV of consented speech at the pace the bridge delivers it. Run with
// `task test:models`:
//
//	go test -tags=models ./internal/stt/ -stt-url http://127.0.0.1:8000 -stt-wav /path/to/0.wav
//
// Both are flags with no default so neither a household address nor a
// recording lives in the repo (CONTRIBUTING §7); without them this skips.
//
// The hermetic tier proves the cadence and the coalescing. This one measures
// what they cost against a real decode: how many partials a clip yields, how
// far each lags the audio, whether they converge on the final, and what
// Finish costs. Those numbers are what DefaultPartialEvery, DefaultMaxUtterance
// and the provider's timeout are set against (SPEC §11).
package stt_test

import (
	"context"
	"encoding/binary"
	"flag"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/provider/speaches"
	"github.com/teagan42/chorus/internal/stt"
)

var (
	endpoint = flag.String("stt-url", "", "speaches base URL; skips when empty")
	wavPath  = flag.String("stt-wav", "", "16 kHz s16le mono WAV of consented speech; skips when empty")
	// The default names words in `test_wavs/0.wav` of sherpa-onnx's published
	// Parakeet bundle, the one consented clip a dev box can fetch without
	// Hugging Face; its transcript is in that project's NeMo notes.
	expect = flag.String("stt-expect", "turning,certainly,portrait", "comma-separated words -stt-wav is known to contain")
)

// chunk is the firmware's send cadence (SPEC §3.2): 32 ms of device audio.
const chunk = 32 * time.Millisecond

const bytesPerSecond = bridge.SampleRate * bridge.BitsPerSample / 8

// streamBudget bounds the whole run. A slow CPU decodes below real time, and
// the final is a decode of everything held.
const streamBudget = 3 * time.Minute

// speech is the -stt-wav clip as device PCM. The header is checked rather
// than converted: a clip at another rate must be resampled before it is
// handed over, or the lag measured here is ffmpeg's, not the stream's.
func speech(t *testing.T) []byte {
	t.Helper()
	if *wavPath == "" {
		t.Skip("no -stt-wav")
	}
	raw, err := os.ReadFile(*wavPath)
	if err != nil {
		t.Fatalf("read %s: %v", *wavPath, err)
	}
	if len(raw) < 44 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		t.Fatalf("%s is not a RIFF/WAVE file", *wavPath)
	}
	var pcm []byte
	for off := 12; off+8 <= len(raw); {
		id, size := string(raw[off:off+4]), int(binary.LittleEndian.Uint32(raw[off+4:]))
		body := raw[off+8 : min(off+8+size, len(raw))]
		switch id {
		case "fmt ":
			format, channels := binary.LittleEndian.Uint16(body[0:]), binary.LittleEndian.Uint16(body[2:])
			rate, bits := binary.LittleEndian.Uint32(body[4:]), binary.LittleEndian.Uint16(body[14:])
			if format != 1 || channels != 1 || rate != bridge.SampleRate || bits != bridge.BitsPerSample {
				t.Fatalf("%s is format %d, %d ch, %d Hz, %d-bit; want pcm mono %d Hz %d-bit (resample with ffmpeg)",
					*wavPath, format, channels, rate, bits, bridge.SampleRate, bridge.BitsPerSample)
			}
		case "data":
			pcm = body
		}
		// Chunks are word-aligned; an odd-sized one is padded.
		off += 8 + size + size%2
	}
	if len(pcm) == 0 {
		t.Fatalf("%s has no data chunk", *wavPath)
	}
	return pcm
}

func seconds(n int) time.Duration {
	return time.Duration(n) * time.Second / bytesPerSecond
}

// words lower-cases a transcript and strips its punctuation, so "portrait."
// and "Portrait" both count as the word.
func words(text string) []string {
	return strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			return unicode.ToLower(r)
		}
		return ' '
	}, text))
}

// decode is one Transcribe the stream made, seen from inside the seam.
type decode struct {
	covers time.Duration
	took   time.Duration
	// behind is how much audio had arrived, past what this decode covered,
	// by the time it answered: the staleness of the partial it produced.
	behind time.Duration
	text   string
	err    error
}

// recorder wraps the provider so every decode's size, cost and staleness are
// observed, which the Partials channel alone cannot show: a replaced partial
// still cost a decode.
type recorder struct {
	stt.Transcriber
	written *atomic.Int64

	mu      sync.Mutex
	decodes []decode
}

func (r *recorder) Transcribe(ctx context.Context, pcm []byte) (stt.Result, error) {
	start := time.Now()
	res, err := r.Transcriber.Transcribe(ctx, pcm)
	d := decode{
		covers: seconds(len(pcm)),
		took:   time.Since(start),
		behind: seconds(int(r.written.Load()) - len(pcm)),
		text:   res.Text,
		err:    err,
	}
	r.mu.Lock()
	r.decodes = append(r.decodes, d)
	r.mu.Unlock()
	return res, err
}

type arrival struct {
	at   time.Duration
	text string
}

// The clip arrives one 32 ms chunk at a time, in real time, because what is
// measured is how the stream keeps up with a speaker: written all at once,
// every cadence would coalesce into one decode and the numbers would be the
// provider's, not the stream's. The wall clock is the instrument here, as in
// the provider tiers; the hermetic tier owns the behaviour.
//
// verifies SPEC §4.3, §11
func TestAnUtteranceStreamedAtTheBridgesPace(t *testing.T) {
	if *endpoint == "" {
		t.Skip("no -stt-url")
	}
	pcm := speech(t)
	tr, err := speaches.New(speaches.Config{BaseURL: *endpoint})
	if err != nil {
		t.Fatalf("new transcriber: %v", err)
	}
	rec := &recorder{Transcriber: tr, written: &atomic.Int64{}}

	ctx, cancel := context.WithTimeout(context.Background(), streamBudget)
	defer cancel()
	u, err := stt.Open(ctx, rec, stt.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		<-u.Done()
	})

	start := time.Now()
	var arrivals []arrival
	collected := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(collected)
		for {
			select {
			case r := <-u.Partials():
				arrivals = append(arrivals, arrival{at: time.Since(start), text: r.Text})
			case <-stop:
				return
			}
		}
	}()

	chunkBytes := int(chunk*bridge.SampleRate/time.Second) * (bridge.BitsPerSample / 8)
	tick := time.NewTicker(chunk)
	defer tick.Stop()
	for off := 0; off < len(pcm); off += chunkBytes {
		<-tick.C
		end := min(off+chunkBytes, len(pcm))
		if err := u.Write(pcm[off:end]); err != nil {
			t.Fatalf("write at %v: %v", seconds(off), err)
		}
		rec.written.Store(int64(end))
	}
	streamed := time.Since(start)

	finishAt := time.Now()
	final, err := u.Finish(ctx)
	finishTook := time.Since(finishAt)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	close(stop)
	<-collected
	// A partial published just before Finish may still be unread.
	select {
	case r := <-u.Partials():
		arrivals = append(arrivals, arrival{at: time.Since(start), text: r.Text})
	default:
	}

	clip := seconds(len(pcm))
	t.Logf("%v of speech streamed in %v as %d chunks of %v; cadence %v -> %d cadence boundaries",
		clip, streamed.Round(time.Millisecond), (len(pcm)+chunkBytes-1)/chunkBytes, chunk,
		seconds(stt.DefaultPartialEvery), len(pcm)/stt.DefaultPartialEvery)
	rec.mu.Lock()
	decodes := slices.Clone(rec.decodes)
	rec.mu.Unlock()
	for i, d := range decodes {
		kind := "partial"
		if i == len(decodes)-1 {
			kind = "final"
		}
		if d.err != nil {
			t.Logf("%s decode %d: %v of audio failed after %v: %v", kind, i+1, d.covers, d.took.Round(time.Millisecond), d.err)
			continue
		}
		t.Logf("%s decode %d: %v of audio in %v, %v behind the mic -> %q",
			kind, i+1, d.covers, d.took.Round(time.Millisecond), d.behind.Round(time.Millisecond), d.text)
	}
	for i, a := range arrivals {
		t.Logf("partial %d read at %v: %q", i+1, a.at.Round(time.Millisecond), a.text)
	}
	t.Logf("finish: %q in %v", final.Text, finishTook.Round(time.Millisecond))

	// A partial is the gate's third stage (SPEC §4.3): a clip this long that
	// yields none means the stream cannot keep up with speech at all.
	if len(arrivals) == 0 {
		t.Fatal("no partial arrived")
	}
	heard := words(final.Text)
	for _, w := range strings.Split(*expect, ",") {
		if w = strings.TrimSpace(strings.ToLower(w)); w != "" && !slices.Contains(heard, w) {
			t.Errorf("the final is missing %q", w)
		}
	}
	// Convergence: the last partial covers all but the tail of the clip, so
	// its words should be a prefix of the final's. Logged as a count rather
	// than asserted, because a re-decode may revise an earlier word, and that
	// is the model's business, not the stream's.
	last := words(arrivals[len(arrivals)-1].text)
	agree := 0
	for agree < len(last) && agree < len(heard) && last[agree] == heard[agree] {
		agree++
	}
	t.Logf("the last partial agrees with the final on %d of its %d words; the final has %d", agree, len(last), len(heard))
}
