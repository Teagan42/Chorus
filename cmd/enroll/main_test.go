package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/provider/speakerid"
)

// dim is small so a test can read a vector; the command never assumes 192.
const dim = 4

func axis(i int) []float32 {
	v := make([]float32, dim)
	v[i] = 1
	return v
}

// noisy perturbs v deterministically: the same voice on a different phrase.
func noisy(v []float32, eps float64, seed uint64) []float32 {
	r := rand.New(rand.NewPCG(seed, seed))
	out := make([]float32, len(v))
	for i := range v {
		out[i] = v[i] + float32((2*r.Float64()-1)*eps)
	}
	return out
}

// fakeEmbedder maps audio to the vector a test chose for it, so enrollment
// runs without a sidecar. An unknown utterance is an error, which is what a
// sidecar that is down looks like.
type fakeEmbedder struct {
	model string
	by    map[string][]float32
	calls int
}

func newFake() *fakeEmbedder {
	return &fakeEmbedder{model: "fake", by: map[string][]float32{}}
}

func (f *fakeEmbedder) Embed(_ context.Context, pcm []byte) ([]float32, error) {
	f.calls++
	v, ok := f.by[string(pcm)]
	if !ok {
		return nil, errors.New("sidecar unreachable")
	}
	return v, nil
}

func (f *fakeEmbedder) Model() string { return f.model }
func (f *fakeEmbedder) Dim() int      { return dim }

// phrase is 300 ms of s16le derived from the words, so two phrases are two
// different byte strings the fake can tell apart. Not speech: the embedder
// is fake, so only the container's shape matters here.
func phrase(words string) []byte {
	r := rand.New(rand.NewPCG(uint64(len(words)), uint64(words[0])))
	n := bridge.SampleRate * 300 / 1000
	out := make([]byte, 2*n)
	for i := range n {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16((2*r.Float64()-1)*8000)))
	}
	return out
}

// writeWAV lays pcm into a canonical RIFF/WAVE at the given format. The
// format fields are the test's to choose, so a wrong one can be refused.
func writeWAV(t *testing.T, path string, pcm []byte, rate, channels, bits int) string {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*channels*bits/8))
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels*bits/8))
	_ = binary.Write(&b, binary.LittleEndian, uint16(bits))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// deviceWAV is a take in the satellite's own format.
func deviceWAV(t *testing.T, dir, name string, pcm []byte) string {
	t.Helper()
	return writeWAV(t, filepath.Join(dir, name), pcm, bridge.SampleRate, 1, bridge.BitsPerSample)
}

// kitchen is Teagan's three guided phrases, said in the kitchen, with the
// voice the fake hears them in.
func kitchen(t *testing.T, dir string, emb *fakeEmbedder) []string {
	t.Helper()
	var paths []string
	for i, words := range []string{"what's the weather", "set a timer for ten minutes", "turn off the kitchen lights"} {
		pcm := phrase(words)
		emb.by[string(pcm)] = noisy(axis(0), 0.05, uint64(i+1))
		paths = append(paths, deviceWAV(t, dir, "teagan-kitchen-"+string(rune('1'+i))+".wav", pcm))
	}
	return paths
}

// rig is the command with every seam injected: nothing reads the real
// environment or dials anything (CONTRIBUTING: hermetic means hermetic).
type rig struct {
	emb         *fakeEmbedder
	env         map[string]string
	out, report bytes.Buffer
	embedderFor []string
}

func newRig() *rig {
	return &rig{emb: newFake(), env: map[string]string{speakerIDURLEnv: "http://speakerid.test:8890"}}
}

func (r *rig) run(args ...string) error {
	return run(context.Background(), args, deps{
		out:    &r.out,
		report: &r.report,
		getenv: func(k string) string { return r.env[k] },
		newEmbedder: func(baseURL string) (identity.Embedder, error) {
			r.embedderFor = append(r.embedderFor, baseURL)
			return r.emb, nil
		},
	})
}

func wavFlags(paths []string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, "-wav", p)
	}
	return out
}

// Teagan enrolls from three kitchen phrases: the file appears beside the
// path given, holds her centroid under the embedder's model, is private, and
// the command says who, how many, where, and that chorusd must restart.
//
// verifies SPEC §5
func TestTeaganEnrollsFromThreeKitchenPhrases(t *testing.T) {
	dir := t.TempDir()
	r := newRig()
	path := filepath.Join(dir, identity.DefaultPath)
	args := append([]string{"add", "-id", "teagan", "-name", "Teagan", "-identities", path}, wavFlags(kitchen(t, dir, r.emb))...)
	if err := r.run(args...); err != nil {
		t.Fatalf("add: %v", err)
	}

	ids, err := identity.Load(path)
	if err != nil {
		t.Fatalf("load what add wrote: %v", err)
	}
	if ids.Model != "fake" || ids.Dim != dim {
		t.Errorf("file records %s at %d dims, want the embedder's fake at %d", ids.Model, ids.Dim, dim)
	}
	if len(ids.People) != 1 || ids.People[0].ID != "teagan" || ids.People[0].Name != "Teagan" || ids.People[0].Utterances != 3 {
		t.Fatalf("enrolled %+v", ids.People)
	}
	if out, _ := ids.Match(axis(0), identity.Thresholds{}); out.PersonID != "teagan" {
		t.Errorf("teagan does not match her own voice: %+v", out)
	}
	if r.emb.calls != 3 {
		t.Errorf("embedded %d times for 3 phrases", r.emb.calls)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode is %o, want 600: a voiceprint is a credential", mode)
	}
	for _, want := range []string{"teagan", "Teagan", "3 utterances", path, "restart chorusd"} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("output does not say %q:\n%s", want, r.out.String())
		}
	}
}

// Alice brings two phrases from the office. One phrase's embedding is that
// phrase as much as the voice, so she is refused before anything is embedded
// or written, and told how many it takes.
//
// verifies SPEC §5
func TestAliceWithTwoPhrasesIsRefused(t *testing.T) {
	dir := t.TempDir()
	r := newRig()
	path := filepath.Join(dir, identity.DefaultPath)
	var paths []string
	for i, words := range []string{"is anyone home", "play something quiet"} {
		pcm := phrase(words)
		r.emb.by[string(pcm)] = noisy(axis(1), 0.05, uint64(i+1))
		paths = append(paths, deviceWAV(t, dir, "alice-office-"+string(rune('1'+i))+".wav", pcm))
	}
	err := r.run(append([]string{"add", "-id", "alice", "-identities", path}, wavFlags(paths)...)...)
	if err == nil {
		t.Fatal("alice was enrolled from two phrases")
	}
	if !strings.Contains(err.Error(), "2") || !strings.Contains(err.Error(), "at least 3") {
		t.Errorf("error does not say how many were given and how many it takes: %v", err)
	}
	if r.emb.calls != 0 {
		t.Errorf("embedded %d phrases before refusing", r.emb.calls)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a refused enrollment left a file behind")
	}
}

// A take that is not the satellite's own format is refused by name, saying
// what it is instead. Nothing is resampled: the centroid must come from the
// audio the device will produce, or the thresholds measured for it do not hold.
//
// verifies SPEC §5, §10
func TestAFileInAnotherFormatIsRefusedByName(t *testing.T) {
	dir := t.TempDir()
	r := newRig()
	path := filepath.Join(dir, identity.DefaultPath)
	good := kitchen(t, dir, r.emb)
	pcm := phrase("remind me to call alan")

	cases := []struct {
		name, file string
		write      func(string) string
		want       string
	}{
		{"44.1 kHz", "alan-office-44k.wav", func(p string) string { return writeWAV(t, p, pcm, 44100, 1, 16) }, "44100 Hz"},
		{"stereo", "alan-office-stereo.wav", func(p string) string { return writeWAV(t, p, pcm, 16000, 2, 16) }, "2 ch"},
		{"8-bit", "alan-office-8bit.wav", func(p string) string { return writeWAV(t, p, pcm, 16000, 1, 8) }, "8 bit"},
		{"not a wav", "alan-office.mp3", func(p string) string {
			if err := os.WriteFile(p, []byte("ID3 not audio we can read"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, "not a RIFF WAVE"},
		{"missing", "alan-office-missing.wav", func(p string) string { return p }, "no such file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r.emb.calls = 0
			bad := c.write(filepath.Join(dir, c.file))
			paths := append([]string{good[0], good[1]}, bad)
			err := r.run(append([]string{"add", "-id", "alan", "-identities", path}, wavFlags(paths)...)...)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), c.file) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should name %s and say %q, got: %v", c.file, c.want, err)
			}
			if r.emb.calls != 0 {
				t.Errorf("embedded %d phrases before refusing", r.emb.calls)
			}
			if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Error("a refused enrollment left a file behind")
			}
		})
	}
}

// The second person joins the file the first one made; a reused id is refused
// and the household is left as it was.
//
// verifies SPEC §5
func TestASecondPersonJoinsTheHousehold(t *testing.T) {
	dir := t.TempDir()
	r := newRig()
	path := filepath.Join(dir, identity.DefaultPath)
	if err := r.run(append([]string{"add", "-id", "teagan", "-name", "Teagan", "-identities", path}, wavFlags(kitchen(t, dir, r.emb))...)...); err != nil {
		t.Fatalf("add teagan: %v", err)
	}
	var paths []string
	for i, words := range []string{"lock the front door", "what time is it", "start the dishwasher"} {
		pcm := phrase(words)
		r.emb.by[string(pcm)] = noisy(axis(1), 0.05, uint64(i+1))
		paths = append(paths, deviceWAV(t, dir, "alan-hall-"+string(rune('1'+i))+".wav", pcm))
	}
	if err := r.run(append([]string{"add", "-id", "alan", "-name", "Alan", "-identities", path}, wavFlags(paths)...)...); err != nil {
		t.Fatalf("add alan: %v", err)
	}
	ids, err := identity.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids.Household(); strings.Join(got, ",") != "teagan,alan" {
		t.Errorf("household is %v", got)
	}

	err = r.run(append([]string{"add", "-id", "alan", "-identities", path}, wavFlags(paths)...)...)
	if err == nil || !strings.Contains(err.Error(), "alan") || !strings.Contains(err.Error(), "already enrolled") {
		t.Errorf("enrolling alan twice: %v", err)
	}
	ids, err = identity.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids.People) != 2 {
		t.Errorf("household has %d people after a refused enrollment", len(ids.People))
	}
}

// A file enrolled under one model takes nobody embedded under another: the
// centroids would be scored against a different geometry forever after. The
// refusal is the same one chorusd gives at startup, naming both models.
//
// verifies SPEC §5
func TestAddingUnderAnotherModelIsRefused(t *testing.T) {
	dir := t.TempDir()
	r := newRig()
	path := filepath.Join(dir, identity.DefaultPath)
	if err := r.run(append([]string{"add", "-id", "teagan", "-identities", path}, wavFlags(kitchen(t, dir, r.emb))...)...); err != nil {
		t.Fatalf("add teagan: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	r.emb.model = "someone-elses"
	var paths []string
	for i, words := range []string{"lock the front door", "what time is it", "start the dishwasher"} {
		pcm := phrase(words)
		r.emb.by[string(pcm)] = noisy(axis(1), 0.05, uint64(i+1))
		paths = append(paths, deviceWAV(t, dir, "alan-hall-"+string(rune('1'+i))+".wav", pcm))
	}
	err = r.run(append([]string{"add", "-id", "alan", "-identities", path}, wavFlags(paths)...)...)
	if err == nil {
		t.Fatal("a household took an enrollment from a different model")
	}
	for _, want := range []string{"fake", "someone-elses"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a refused enrollment changed the file")
	}
}

// The sidecar is where chorusd finds it, SPEAKERID_URL, with the flag
// overriding; neither set is an error that names the variable, not a dial.
//
// verifies SPEC §13
func TestTheSidecarComesFromTheEnvironmentUnlessTheFlagSaysOtherwise(t *testing.T) {
	dir := t.TempDir()
	r := newRig()
	path := filepath.Join(dir, identity.DefaultPath)
	args := append([]string{"add", "-id", "teagan", "-identities", path}, wavFlags(kitchen(t, dir, r.emb))...)

	if err := r.run(args...); err != nil {
		t.Fatalf("add from the environment: %v", err)
	}
	if err := r.run("remove", "-id", "teagan", "-identities", path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := r.run(append(args, "-speakerid-url", "http://flag.test:1")...); err != nil {
		t.Fatalf("add from the flag: %v", err)
	}
	if strings.Join(r.embedderFor, " ") != "http://speakerid.test:8890 http://flag.test:1" {
		t.Errorf("embedders were built for %v", r.embedderFor)
	}

	delete(r.env, speakerIDURLEnv)
	r.embedderFor = nil
	err := r.run(append([]string{"add", "-id", "alan", "-identities", path}, wavFlags(kitchen(t, dir, r.emb))...)...)
	if err == nil || !strings.Contains(err.Error(), speakerIDURLEnv) {
		t.Errorf("with no sidecar configured: %v", err)
	}
	if len(r.embedderFor) != 0 {
		t.Error("an embedder was built with no URL")
	}
}

// With no -identities, the file goes where chorusd will look for it: beside
// devices.yaml, whichever inventory -devices names.
//
// verifies SPEC §13
func TestIdentitiesSitBesideTheInventoryByDefault(t *testing.T) {
	dir := t.TempDir()
	r := newRig()
	devices := filepath.Join(dir, "house", "devices.yaml")
	if err := os.MkdirAll(filepath.Dir(devices), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.run(append([]string{"add", "-id", "teagan", "-devices", devices}, wavFlags(kitchen(t, dir, r.emb))...)...); err != nil {
		t.Fatalf("add: %v", err)
	}
	want := filepath.Join(dir, "house", identity.DefaultPath)
	if _, err := identity.Load(want); err != nil {
		t.Errorf("nothing chorusd can load at %s: %v", want, err)
	}
	if !strings.Contains(r.out.String(), want) {
		t.Errorf("output does not say where the file went:\n%s", r.out.String())
	}
}

// Over the real client, each take's data chunk is what reaches the sidecar,
// as raw PCM at the embed path, and the file records the model the sidecar
// declared. The server is in-process; nothing leaves the test.
//
// verifies SPEC §10
func TestAddEmbedsEachTakeOverTheSpeakerIDClient(t *testing.T) {
	dir := t.TempDir()
	var bodies [][]byte
	var contentTypes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/embed" {
			http.NotFound(w, req)
			return
		}
		b, _ := io.ReadAll(req.Body)
		bodies = append(bodies, b)
		contentTypes = append(contentTypes, req.Header.Get("Content-Type"))
		// One voice, slightly different per take, at the width the client
		// is built for.
		e := make([]float32, speakerid.DefaultDim)
		e[0], e[1] = 1, float32(len(bodies))*0.01
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": e, "dim": speakerid.DefaultDim, "model": speakerid.DefaultModel})
	}))
	defer srv.Close()

	var takes [][]byte
	var paths []string
	for i, words := range []string{"what's the weather", "set a timer for ten minutes", "turn off the kitchen lights"} {
		pcm := phrase(words)
		takes = append(takes, pcm)
		paths = append(paths, deviceWAV(t, dir, "teagan-kitchen-"+string(rune('1'+i))+".wav", pcm))
	}
	path := filepath.Join(dir, identity.DefaultPath)
	var out, report bytes.Buffer
	err := run(context.Background(), append([]string{"add", "-id", "teagan", "-identities", path, "-speakerid-url", srv.URL}, wavFlags(paths)...), deps{
		out: &out, report: &report, getenv: func(string) string { return "" }, newEmbedder: speakerIDEmbedder,
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("the sidecar saw %d requests for 3 takes", len(bodies))
	}
	for i := range takes {
		if !bytes.Equal(bodies[i], takes[i]) {
			t.Errorf("take %d: the body is not the data chunk, byte for byte", i)
		}
		if contentTypes[i] != speakerid.ContentType {
			t.Errorf("take %d posted as %q", i, contentTypes[i])
		}
	}
	ids, err := identity.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if ids.Model != speakerid.DefaultModel || ids.Dim != speakerid.DefaultDim {
		t.Errorf("file records %s at %d dims", ids.Model, ids.Dim)
	}
}

// list names each person with how many phrases built their voiceprint, and
// nothing of the vector: the file is biometric and a terminal is not where it
// belongs. An absent file is nobody enrolled, not an error.
//
// verifies SPEC §5
func TestListNamesEachPersonWithoutTheirVoiceprint(t *testing.T) {
	dir := t.TempDir()
	r := newRig()
	path := filepath.Join(dir, identity.DefaultPath)

	if err := r.run("list", "-identities", path); err != nil {
		t.Fatalf("list with no file: %v", err)
	}
	if !strings.Contains(r.out.String(), "nobody is enrolled") || !strings.Contains(r.out.String(), path) {
		t.Errorf("list with no file said:\n%s", r.out.String())
	}

	ids := &identity.Identities{Model: "fake", Dim: dim}
	if err := ids.Enroll("teagan", "Teagan", [][]float32{axis(0), noisy(axis(0), 0.05, 1), noisy(axis(0), 0.05, 2)}); err != nil {
		t.Fatal(err)
	}
	if err := ids.Enroll("alan", "", [][]float32{axis(1), axis(1), axis(1), axis(1)}); err != nil {
		t.Fatal(err)
	}
	if err := ids.Save(path); err != nil {
		t.Fatal(err)
	}
	r.out.Reset()
	if err := r.run("list", "-identities", path); err != nil {
		t.Fatalf("list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(r.out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("list printed %d lines for 2 people:\n%s", len(lines), r.out.String())
	}
	if !strings.Contains(lines[0], "teagan") || !strings.Contains(lines[0], "Teagan") || !strings.Contains(lines[0], "3 utterances") {
		t.Errorf("teagan's line: %q", lines[0])
	}
	if !strings.Contains(lines[1], "alan") || !strings.Contains(lines[1], "4 utterances") {
		t.Errorf("alan's line: %q", lines[1])
	}
	for _, leak := range []string{"centroid", "0.", "[", "1 0 0"} {
		if strings.Contains(r.out.String(), leak) {
			t.Errorf("list printed something of the vectors (%q):\n%s", leak, r.out.String())
		}
	}
}

// remove drops one person and saves the rest; a stranger is refused by name,
// with who is enrolled, and the file is untouched.
//
// verifies SPEC §5
func TestRemoveDropsAPersonAndRefusesAStranger(t *testing.T) {
	dir := t.TempDir()
	r := newRig()
	path := filepath.Join(dir, identity.DefaultPath)
	ids := &identity.Identities{Model: "fake", Dim: dim}
	if err := ids.Enroll("teagan", "Teagan", [][]float32{axis(0), axis(0), axis(0)}); err != nil {
		t.Fatal(err)
	}
	if err := ids.Enroll("alan", "Alan", [][]float32{axis(1), axis(1), axis(1)}); err != nil {
		t.Fatal(err)
	}
	if err := ids.Save(path); err != nil {
		t.Fatal(err)
	}

	err := r.run("remove", "-id", "cass", "-identities", path)
	if err == nil {
		t.Fatal("a stranger was removed")
	}
	if !strings.Contains(err.Error(), "cass") || !strings.Contains(err.Error(), "not enrolled") {
		t.Errorf("error does not name the stranger: %v", err)
	}
	for _, id := range []string{"teagan", "alan"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error does not say who is enrolled: %v", err)
		}
	}

	if err := r.run("remove", "-id", "alan", "-identities", path); err != nil {
		t.Fatalf("remove alan: %v", err)
	}
	got, err := identity.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Household(), ",") != "teagan" {
		t.Errorf("household is %v after removing alan", got.Household())
	}
	for _, want := range []string{"alan", path, "restart chorusd"} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("output does not say %q:\n%s", want, r.out.String())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries after a save, want only the file", len(entries))
	}

	err = r.run("remove", "-id", "alan", "-identities", filepath.Join(dir, "nope.yaml"))
	if err == nil || !strings.Contains(err.Error(), "nobody is enrolled") {
		t.Errorf("removing from no file: %v", err)
	}
}

// A verb the command does not have, or a verb missing what it needs, is a
// usage error: exit 2, the convention flag.Parse and harvest share.
func TestUsageErrorsAreTheirOwnKind(t *testing.T) {
	r := newRig()
	cases := map[string][]string{
		"no verb":           {},
		"unknown verb":      {"enrol"},
		"add without id":    {"add", "-wav", "a.wav", "-wav", "b.wav", "-wav", "c.wav"},
		"add without wav":   {"add", "-id", "teagan"},
		"remove without id": {"remove"},
		"bad flag":          {"list", "-no-such-flag"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			err := r.run(args...)
			if !errors.Is(err, errUsage) {
				t.Errorf("%v: want a usage error, got %v", args, err)
			}
		})
	}
	if r.emb.calls != 0 {
		t.Error("a usage error reached the embedder")
	}
	if !strings.Contains(r.report.String(), "add") || !strings.Contains(r.report.String(), "list") || !strings.Contains(r.report.String(), "remove") {
		t.Errorf("usage does not name the verbs:\n%s", r.report.String())
	}
}
