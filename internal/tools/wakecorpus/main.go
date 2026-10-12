// Command wakecorpus lays the review UI's wake corpus out as microWakeWord's
// trainer reads it: each shipped hard negative's clip as a 16 kHz WAV in a
// training, validation or testing directory, and a manifest naming the
// journal row behind every clip and why each other row stayed out
// (ADR-0078). `task wake:corpus` runs it:
//
//	task wake:corpus -- -negatives chorus-wake-negatives.jsonl
//
// The rows are the export's, which applies ADR-0058's shipping rule; each
// is checked again against the rules a hard negative must pass, so an
// export from an older build cannot put a real wake in front of the trainer.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/harvest"
)

// schemaVersion is the manifest's format, which waketrain reads.
const schemaVersion = 1

// The channels a corpus can be cut from (SPEC §9.3). The satellite's wake
// model listens to the processed one (esphome/packages/chorus-bridge.yaml).
const (
	channelProcessed = "processed"
	channelSecond    = "second"
)

// The trainer's split directories, by microWakeWord's names.
const (
	splitTraining   = "training"
	splitValidation = "validation"
	splitTesting    = "testing"
)

// The two piles a clip lands in, so the trainer can weight what a reviewer
// heard above what only the rule passed (ADR-0058).
const (
	setAuto      = "auto"
	setConfirmed = "confirmed"
)

// Why a row of the export is not in the corpus.
const (
	whyAudioGone       = "audio_gone"
	whyAudioMissing    = "audio_missing"
	whyNoSecondChannel = "no_second_channel"
	whyTooShort        = "too_short"
	whyNotHardNegative = "not_hard_negative"
	whyHeldUnconfirmed = "held_unconfirmed"
)

// minClipBytes is a tenth of a second of device audio: shorter, the
// trainer's 30 ms feature windows have nothing to slide over.
const minClipBytes = bridge.SampleRate / 10 * bridge.BitsPerSample / 8

// maxClipBytes bounds a clip as the dataset archive does: past ten minutes
// a blob is corrupt or not audio.
const maxClipBytes = 32 << 20

// config is what a run is asked for.
type config struct {
	Channel             string
	Validation, Testing float64
}

func (c config) check() error {
	switch {
	case c.Channel != channelProcessed && c.Channel != channelSecond:
		return fmt.Errorf("channel %q: want %s or %s", c.Channel, channelProcessed, channelSecond)
	case c.Validation < 0 || c.Testing < 0 || c.Validation+c.Testing >= 1:
		return fmt.Errorf("validation %.2f and testing %.2f must be at least 0 and leave some training", c.Validation, c.Testing)
	}
	return nil
}

// split is the directory a clip lands in: the first eight bytes of the
// SHA-256 of its satellite and hour, read as a fraction of 2^64, below
// testing is testing and below testing plus validation is validation. A
// run of false wakes from one programme stays on one side, and a larger
// held-out fraction only moves clips into it.
func (c config) split(satellite, at string) string {
	hour := at
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		hour = t.UTC().Format("2006-01-02T15")
	}
	sum := sha256.Sum256([]byte(satellite + "/" + hour))
	f := float64(binary.BigEndian.Uint64(sum[:8])) / math.Exp2(64)
	switch {
	case f < c.Testing:
		return splitTesting
	case f < c.Testing+c.Validation:
		return splitValidation
	}
	return splitTraining
}

// row is one line of /export/wake-negatives.jsonl (harvest.ExportNegatives).
type row struct {
	ID             string `json:"id"`
	Label          *int   `json:"label"`
	Source         string `json:"source"`
	Reason         string `json:"reason"`
	WakeWord       string `json:"wake_word"`
	Satellite      string `json:"satellite"`
	At             string `json:"at"`
	Audio          string `json:"audio"`
	SecondAudio    string `json:"second_audio"`
	Confirmed      bool   `json:"confirmed"`
	AudioGone      bool   `json:"audio_gone"`
	ConversationID string `json:"conversation_id"`
	Seq            uint64 `json:"seq"`
}

// manifest is the corpus's word on itself, and the provenance SPEC §9.3's
// corpus carries into a model: every journal row in it, and every one not.
type manifest struct {
	SchemaVersion      int                           `json:"schema_version"`
	Source             source                        `json:"source"`
	Channel            string                        `json:"channel"`
	Audio              string                        `json:"audio"`
	Split              string                        `json:"split"`
	ValidationFraction float64                       `json:"validation_fraction"`
	TestingFraction    float64                       `json:"testing_fraction"`
	Sets               map[string]map[string]summary `json:"sets"`
	Clips              []clip                        `json:"clips"`
	Skipped            []skipped                     `json:"skipped"`
}

type source struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Rows   int    `json:"rows"`
}

type summary struct {
	Clips   int     `json:"clips"`
	Seconds float64 `json:"seconds"`
}

type clip struct {
	ID             string  `json:"id"`
	ConversationID string  `json:"conversation_id"`
	Seq            uint64  `json:"seq"`
	Satellite      string  `json:"satellite"`
	At             string  `json:"at"`
	Reason         string  `json:"reason"`
	WakeWord       string  `json:"wake_word"`
	Confirmed      bool    `json:"confirmed"`
	Audio          string  `json:"audio"`
	Set            string  `json:"set"`
	Split          string  `json:"split"`
	Path           string  `json:"path"`
	Seconds        float64 `json:"seconds"`
}

type skipped struct {
	ID  string `json:"id"`
	Why string `json:"why"`
}

const (
	manifestName = "manifest.json"
	negativesDir = "negatives"
)

// assemble writes the corpus for export into out and returns its manifest.
// A missing clip is skipped and named; anything that says the file is not
// the wake corpus, or a clip that is not device audio, fails the whole run:
// a short corpus that looks whole is worse than none.
func assemble(ctx context.Context, export []byte, name string, clips harvest.Clips, out string, cfg config) (manifest, error) {
	if err := cfg.check(); err != nil {
		return manifest{}, err
	}
	rows, err := parse(export)
	if err != nil {
		return manifest{}, fmt.Errorf("%s: %w", name, err)
	}
	if err := prepare(out); err != nil {
		return manifest{}, err
	}
	sum := sha256.Sum256(export)
	m := manifest{
		SchemaVersion: schemaVersion,
		Source:        source{File: filepath.Base(name), SHA256: hex.EncodeToString(sum[:]), Rows: len(rows)},
		Channel:       cfg.Channel,
		Audio:         "WAV, 16 kHz, 16-bit signed, mono",
		Split: "sha256(satellite + \"/\" + the UTC hour of at)[:8] as a big-endian fraction of 2^64; " +
			"below testing_fraction is testing, below testing_fraction + validation_fraction is validation, the rest training",
		ValidationFraction: cfg.Validation,
		TestingFraction:    cfg.Testing,
		Sets:               map[string]map[string]summary{},
		Clips:              []clip{},
		Skipped:            []skipped{},
	}
	for _, r := range rows {
		c, why, err := place(ctx, r, clips, out, cfg)
		if err != nil {
			return manifest{}, fmt.Errorf("%s: %w", r.ID, err)
		}
		if why != "" {
			m.Skipped = append(m.Skipped, skipped{ID: r.ID, Why: why})
			continue
		}
		m.Clips = append(m.Clips, c)
		if m.Sets[c.Set] == nil {
			m.Sets[c.Set] = map[string]summary{}
		}
		s := m.Sets[c.Set][c.Split]
		s.Clips++
		s.Seconds = round(s.Seconds + c.Seconds)
		m.Sets[c.Set][c.Split] = s
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return manifest{}, fmt.Errorf("manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(out, manifestName), append(b, '\n'), 0o644); err != nil {
		return manifest{}, fmt.Errorf("manifest: %w", err)
	}
	return m, nil
}

// parse reads the export's rows and refuses a file that is not the wake
// corpus: a positive, another source, a row naming no satellite's log, or
// the same row twice.
func parse(export []byte) ([]row, error) {
	dec := json.NewDecoder(bytes.NewReader(export))
	dec.DisallowUnknownFields()
	var rows []row
	seen := map[string]bool{}
	for {
		var r row
		err := dec.Decode(&r)
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", len(rows)+1, err)
		}
		switch {
		case r.Label == nil || *r.Label != 0:
			return nil, fmt.Errorf("row %d (%s): not a negative (label 0)", len(rows)+1, r.ID)
		case r.Source != "stage2_reject":
			return nil, fmt.Errorf("row %d (%s): source %q is not a stage-two rejection", len(rows)+1, r.ID, r.Source)
		case !strings.HasPrefix(r.ConversationID, "device:") || r.ID != fmt.Sprintf("%s/%d", r.ConversationID, r.Seq):
			return nil, fmt.Errorf("row %d (%s): not a rejection in a satellite's own log", len(rows)+1, r.ID)
		case seen[r.ID]:
			return nil, fmt.Errorf("row %d (%s): listed twice", len(rows)+1, r.ID)
		}
		seen[r.ID] = true
		rows = append(rows, r)
	}
}

// prepare empties out of a previous corpus, and refuses a directory that
// holds anything else.
func prepare(out string) error {
	entries, err := os.ReadDir(out)
	if errors.Is(err, fs.ErrNotExist) {
		return os.MkdirAll(out, 0o755)
	}
	if err != nil {
		return fmt.Errorf("out %s: %w", out, err)
	}
	for _, e := range entries {
		if e.Name() != manifestName && e.Name() != negativesDir {
			return fmt.Errorf("out %s holds %s, so it is not a corpus this tool wrote; name an empty directory", out, e.Name())
		}
	}
	if err := os.RemoveAll(filepath.Join(out, negativesDir)); err != nil {
		return fmt.Errorf("out %s: %w", out, err)
	}
	return nil
}

// place writes one row's clip, or says why it is not in the corpus.
func place(ctx context.Context, r row, clips harvest.Clips, out string, cfg config) (clip, string, error) {
	if !harvest.HardNegative(map[string]string{"reason": r.Reason, "wake_word": r.WakeWord}) {
		return clip{}, whyNotHardNegative, nil
	}
	if (harvest.Negative{Reason: r.Reason}).Held() && !r.Confirmed {
		return clip{}, whyHeldUnconfirmed, nil
	}
	ref := r.Audio
	if cfg.Channel == channelSecond {
		ref = r.SecondAudio
		if ref == "" {
			return clip{}, whyNoSecondChannel, nil
		}
	} else if r.AudioGone {
		return clip{}, whyAudioGone, nil
	}
	pcm, err := read(ctx, clips, ref)
	if errors.Is(err, fs.ErrNotExist) {
		return clip{}, whyAudioMissing, nil
	}
	if err != nil {
		return clip{}, "", err
	}
	if len(pcm) < minClipBytes {
		return clip{}, whyTooShort, nil
	}

	satellite := strings.TrimPrefix(r.ConversationID, "device:")
	set := setAuto
	if r.Confirmed {
		set = setConfirmed
	}
	split := cfg.split(satellite, r.At)
	rel := path.Join(negativesDir, set, split, fmt.Sprintf("%s-%d.wav", fileSafe(satellite), r.Seq))
	full := filepath.Join(out, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return clip{}, "", err
	}
	if err := os.WriteFile(full, append(harvest.WAVHeader(len(pcm)), pcm...), 0o644); err != nil {
		return clip{}, "", err
	}
	return clip{
		ID: r.ID, ConversationID: r.ConversationID, Seq: r.Seq, Satellite: satellite, At: r.At,
		Reason: r.Reason, WakeWord: r.WakeWord, Confirmed: r.Confirmed, Audio: ref,
		Set: set, Split: split, Path: rel, Seconds: round(float64(len(pcm)) / (bridge.SampleRate * bridge.BitsPerSample / 8)),
	}, "", nil
}

// read is a clip's PCM, refused when it cannot be 16-bit device audio.
func read(ctx context.Context, clips harvest.Clips, ref string) ([]byte, error) {
	rc, err := clips.Open(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	pcm, err := io.ReadAll(io.LimitReader(rc, maxClipBytes+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", ref, err)
	case len(pcm) > maxClipBytes:
		return nil, fmt.Errorf("%s is over %d bytes, so it is not a clip", ref, maxClipBytes)
	case len(pcm)%2 != 0:
		return nil, fmt.Errorf("%s is %d bytes, so it is not 16-bit audio", ref, len(pcm))
	}
	return pcm, nil
}

// fileSafe keeps a satellite's name to what every filesystem takes.
func fileSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, s)
}

func round(s float64) float64 { return math.Round(s*1000) / 1000 }

func main() {
	negatives := flag.String("negatives", "", "the review UI's /export/wake-negatives.jsonl, or - for stdin")
	blobs := flag.String("blobs", os.Getenv("CHORUS_BLOB_DIR"), "the blob store's directory (CHORUS_BLOB_DIR)")
	out := flag.String("out", ".wake/corpus", "where to lay the corpus out")
	channel := flag.String("channel", channelProcessed, "which channel to train on: processed or second (SPEC §9.3)")
	validation := flag.Float64("validation", 0.1, "fraction of hours held out to choose the model's weights")
	testing := flag.Float64("testing", 0.1, "fraction of hours held out to score the model (task wake:evaluate)")
	flag.Parse()
	if err := run(*negatives, *blobs, *out, config{Channel: *channel, Validation: *validation, Testing: *testing}); err != nil {
		fmt.Fprintf(os.Stderr, "wakecorpus: %v\n", err)
		os.Exit(1)
	}
}

func run(negatives, blobs, out string, cfg config) error {
	if negatives == "" {
		return errors.New("-negatives: name the review UI's wake corpus export (Export, Wake corpus)")
	}
	if blobs == "" {
		return errors.New("-blobs: name the blob store's directory, or set CHORUS_BLOB_DIR")
	}
	if info, err := os.Stat(blobs); err != nil || !info.IsDir() {
		return fmt.Errorf("-blobs %s is not a directory", blobs)
	}
	store, err := blob.NewDir(blobs)
	if err != nil {
		return err
	}
	var export []byte
	if negatives == "-" {
		export, err = io.ReadAll(os.Stdin)
	} else {
		export, err = os.ReadFile(negatives)
	}
	if err != nil {
		return fmt.Errorf("-negatives: %w", err)
	}
	m, err := assemble(context.Background(), export, negatives, store, out, cfg)
	if err != nil {
		return err
	}
	var seconds float64
	for _, c := range m.Clips {
		seconds += c.Seconds
	}
	fmt.Printf("%d clips, %.1f minutes, in %s\n", len(m.Clips), seconds/60, out)
	whys := map[string]int{}
	for _, s := range m.Skipped {
		whys[s.Why]++
	}
	for _, why := range slices.Sorted(maps.Keys(whys)) {
		fmt.Printf("skipped %d: %s\n", whys[why], why)
	}
	return nil
}
