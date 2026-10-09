//go:build models

// Model tier: needs a reachable Smart Turn sidecar. Run with `task test:models`,
// pointing it at one:
//
//	go test -tags=models ./internal/provider/smartturn/ -smartturn-url http://127.0.0.1:8891
//
// The endpoint is a flag with no default so no household address lives in the
// repo. Without it these skip.
//
// What is tested here is the half the hermetic tier cannot reach: that the
// real sidecar still honours the contract the client is built for, and what
// a verdict costs. With -smartturn-wavs naming <dir>/complete/*.wav and
// <dir>/incomplete/*.wav (16 kHz s16le mono, from outside the repo per
// CONTRIBUTING §7), every take is judged and then streamed in real time
// through listen.Semantic, and the quiet each one waited through is printed:
// that is the number the endpointer exists to shrink (ADR-0036).
package smartturn_test

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/listen"
	"github.com/teaganglenn/chorus/internal/provider/smartturn"
)

var (
	endpoint = flag.String("smartturn-url", "", "Smart Turn sidecar base URL; skips when empty")
	wavs     = flag.String("smartturn-wavs", "", "directory of complete/*.wav and incomplete/*.wav; skips when empty")
)

// judgeBudget is generous on purpose: the first call after boot may still be
// warming the model. The cost is logged; the endpointer does not wait past
// listen.DefaultSilence for it anyway.
const judgeBudget = 10 * time.Second

func client(t *testing.T) *smartturn.Client {
	t.Helper()
	if *endpoint == "" {
		t.Skip("no -smartturn-url")
	}
	c, err := smartturn.New(smartturn.Config{BaseURL: *endpoint})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

// hum is seconds of a voiced-sounding harmonic tone: not speech, but enough
// for the contract.
func hum(seconds float64) []byte {
	n := int(seconds * bridge.SampleRate)
	out := make([]byte, 2*n)
	for i := range n {
		t := float64(i) / bridge.SampleRate
		var v float64
		for h := 1; h <= 6; h++ {
			v += math.Sin(2*math.Pi*140*float64(h)*t) / float64(h)
		}
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(v*4000)))
	}
	return out
}

// judge runs one call and logs what it cost.
func judge(t *testing.T, c *smartturn.Client, name string, pcm []byte) smartturn.Verdict {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), judgeBudget)
	defer cancel()
	start := time.Now()
	v, err := c.Judge(ctx, pcm)
	if err != nil {
		t.Fatalf("judge %s: %v", name, err)
	}
	// Logged every run: this is most of what a semantic endpoint costs over
	// its 200 ms pause, and a regression is invisible in a pass/fail.
	t.Logf("%-24s %5.1f s -> complete=%-5v p=%.3f in %v", name, seconds(pcm), v.Complete, v.Probability, time.Since(start).Round(time.Millisecond))
	return v
}

func seconds(pcm []byte) float64 { return float64(len(pcm)) / (2 * bridge.SampleRate) }

// The contract the endpointer is built on, against the real sidecar: the
// checkpoint, a probability, and the same audio twice is the same verdict.
//
// verifies SPEC §4.5, §10
func TestARealSidecarJudgesToTheContract(t *testing.T) {
	c := client(t)
	audio := hum(1.5)
	v := judge(t, c, "hum", audio)
	if v.Model != smartturn.DefaultModel {
		t.Errorf("model = %q", v.Model)
	}
	if v.Complete != (v.Probability > 0.5) {
		t.Errorf("complete = %v at p=%v: the cut is not upstream's", v.Complete, v.Probability)
	}
	if again := judge(t, c, "hum again", audio); again != v {
		t.Errorf("the same audio judged %+v then %+v", v, again)
	}
	// A turn past the window is accepted whole.
	judge(t, c, "long hum", hum(11))
}

func TestTooLittleAudioFailsTheAsk(t *testing.T) {
	c := client(t)
	if _, err := c.Judge(context.Background(), hum(0.05)); err == nil {
		t.Error("50 ms was judged")
	}
}

type take struct {
	name     string
	complete bool
	pcm      []byte
}

func readTakes(t *testing.T, dir string) []take {
	t.Helper()
	var out []take
	for _, kind := range []string{"complete", "incomplete"} {
		paths, err := filepath.Glob(filepath.Join(dir, kind, "*.wav"))
		if err != nil {
			t.Fatalf("glob: %v", err)
		}
		for _, p := range paths {
			pcm, err := readWAV(p)
			if err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			out = append(out, take{name: kind + "/" + filepath.Base(p), complete: kind == "complete", pcm: pcm})
		}
	}
	return out
}

// Every take judged whole, then streamed through the endpointer at the
// device's pace: the quiet it waited through after the last word is what the
// household waits before anything else happens. Finished turns should score
// above unfinished ones on average; single takes can and do go the wrong way,
// which is what the endpointer's hold and silence are for.
//
// verifies SPEC §4.5, §11
func TestARealCorpusEndsTurnsSoonerThanTheSilence(t *testing.T) {
	c := client(t)
	if *wavs == "" {
		t.Skip("no -smartturn-wavs")
	}
	takes := readTakes(t, *wavs)
	var kinds [2]int
	for _, tk := range takes {
		if tk.complete {
			kinds[1]++
		} else {
			kinds[0]++
		}
	}
	// Asked for a corpus and given none is a mistake, not a skip.
	if kinds[0] == 0 || kinds[1] == 0 {
		t.Fatalf("%s: %d complete and %d incomplete takes; want both. `task smartturn:corpus` renders them", *wavs, kinds[1], kinds[0])
	}
	var sum [2]float64
	var n [2]int
	right := 0
	for _, tk := range takes {
		v := judge(t, c, tk.name, tk.pcm)
		i := 0
		if tk.complete {
			i = 1
		}
		sum[i] += v.Probability
		n[i]++
		if v.Complete == tk.complete {
			right++
		}
	}
	t.Logf("verdicts right on %d of %d takes; mean p complete %.3f, incomplete %.3f", right, len(takes), sum[1]/float64(n[1]), sum[0]/float64(n[0]))
	if sum[1]/float64(n[1]) <= sum[0]/float64(n[0]) {
		t.Errorf("finished turns do not score above unfinished ones")
	}

	for _, tk := range takes {
		waited := stream(t, c, tk.pcm)
		t.Logf("%-24s ended %4d ms after the last loud chunk (Energy: %d ms)", tk.name, waited.Milliseconds(), listen.DefaultSilence*1000/(2*bridge.SampleRate))
	}
}

// stream feeds a take and then quiet through a Semantic endpointer at 32 ms a
// chunk, the satellite's pace, and returns the quiet its End waited through.
func stream(t *testing.T, c *smartturn.Client, pcm []byte) time.Duration {
	t.Helper()
	const chunk = 1024
	ep := listen.NewSemantic(c)
	tick := time.NewTicker(32 * time.Millisecond)
	defer tick.Stop()
	quiet := make([]byte, chunk)
	limit := len(pcm) + listen.DefaultHold + 4*chunk
	for off := 0; off < limit; off += chunk {
		<-tick.C
		in := quiet
		if off < len(pcm) {
			in = pcm[off:min(off+chunk, len(pcm))]
		}
		if ep.Feed(in) == listen.End {
			return time.Duration(ep.Trailing()) * time.Second / (2 * bridge.SampleRate)
		}
	}
	t.Fatalf("no End within the hold")
	return 0
}

// readWAV returns the data chunk of a canonical PCM WAV in the device's format.
func readWAV(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF WAVE file")
	}
	var fmtSeen bool
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:]))
		body := b[off+8 : min(off+8+size, len(b))]
		switch id {
		case "fmt ":
			if len(body) < 16 {
				return nil, errors.New("fmt chunk is short")
			}
			format, channels := binary.LittleEndian.Uint16(body), binary.LittleEndian.Uint16(body[2:])
			rate, bits := binary.LittleEndian.Uint32(body[4:]), binary.LittleEndian.Uint16(body[14:])
			if format != 1 || channels != 1 || rate != bridge.SampleRate || int(bits) != bridge.BitsPerSample {
				return nil, fmt.Errorf("format %d, %d ch, %d Hz, %d bit; want PCM mono %d Hz %d bit",
					format, channels, rate, bits, bridge.SampleRate, bridge.BitsPerSample)
			}
			fmtSeen = true
		case "data":
			if !fmtSeen {
				return nil, errors.New("data chunk before fmt chunk")
			}
			return body, nil
		}
		// Chunks are word-aligned; an odd size carries a pad byte.
		off += 8 + size + size%2
	}
	return nil, io.ErrUnexpectedEOF
}
