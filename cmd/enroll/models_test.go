//go:build models

// Model tier: the command end to end against a real speaker-ID sidecar.
//
//	go test -tags=models ./cmd/enroll/ -speakerid-url http://127.0.0.1:8890
//
// The endpoint is a flag with no default so no household address lives in
// the repo. Without it this skips. The audio is synthetic (CONTRIBUTING §7):
// what is tested is that the file enroll writes is one chorusd loads and
// resolves against, not who is speaking.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"math"
	"path/filepath"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/provider/speakerid"
)

var endpoint = flag.String("speakerid-url", "", "speaker-ID sidecar base URL; skips when empty")

// voiced is seconds of a harmonic tone at f0 with a slow tremolo: periodic
// the way speech is, so the model sees something other than noise.
func voiced(f0, seconds float64, phase float64) []byte {
	n := int(seconds * bridge.SampleRate)
	out := make([]byte, 2*n)
	for i := range n {
		t := float64(i) / bridge.SampleRate
		var v float64
		for h := 1; h <= 6; h++ {
			v += math.Sin(2*math.Pi*f0*float64(h)*t+phase) / float64(h)
		}
		v *= 0.3 * (1 + 0.2*math.Sin(2*math.Pi*5*t))
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(v*math.MaxInt16)))
	}
	return out
}

// A real enrollment through the command: three takes of one "voice" go in,
// and the file that comes out loads under the sidecar's declared model and
// identifies a fourth take of the same voice.
//
// verifies SPEC §5
func TestARealSidecarEnrollsThroughTheCommand(t *testing.T) {
	if *endpoint == "" {
		t.Skip("no -speakerid-url")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, identity.DefaultPath)
	args := []string{"add", "-id", "teagan", "-name", "Teagan", "-identities", path, "-speakerid-url", *endpoint}
	for i, phase := range []float64{0, 0.7, 1.4} {
		args = append(args, "-wav", deviceWAV(t, dir, "teagan-kitchen-"+string(rune('1'+i))+".wav", voiced(140, 2, phase)))
	}
	var out, report bytes.Buffer
	d := deps{out: &out, report: &report, getenv: func(string) string { return "" }, newEmbedder: speakerIDEmbedder}
	if err := run(context.Background(), args, d); err != nil {
		t.Fatalf("add: %v\n%s", err, report.String())
	}
	t.Log(out.String())

	ids, err := identity.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if ids.Model != speakerid.DefaultModel || ids.Dim != speakerid.DefaultDim {
		t.Errorf("file records %s at %d dims", ids.Model, ids.Dim)
	}
	emb, err := speakerIDEmbedder(*endpoint)
	if err != nil {
		t.Fatal(err)
	}
	r, err := identity.NewResolver(emb, ids, identity.Thresholds{})
	if err != nil {
		t.Fatalf("the file does not resolve against the sidecar it was enrolled with: %v", err)
	}
	res, err := r.Resolve(context.Background(), voiced(140, 2, 2.1))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	t.Logf("a fourth take resolves as %q (%s) at %.3f", res.PersonID, res.Reason, res.Score)
	if res.PersonID != "teagan" {
		t.Errorf("the enrolled voice resolved as %q (%s) at %.3f", res.PersonID, res.Reason, res.Score)
	}
}
