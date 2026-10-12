package harvest

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/bridge"
)

// Clips is where an archive reads the audio its rows name. blob.Store is one.
type Clips interface {
	Open(ctx context.Context, ref string) (io.ReadCloser, error)
}

// maxClipBytes bounds one clip, as the review UI's audio handler does: past
// ten minutes of device PCM a blob is corrupt or not audio.
const maxClipBytes = 32 << 20

// manifest is an archive's first word on itself: the format, the split it
// was cut with, and what each file holds.
type manifest struct {
	SchemaVersion      int            `json:"schema_version"`
	ExportedAt         string         `json:"exported_at"`
	ValidationFraction float64        `json:"validation_fraction"`
	Split              string         `json:"split"`
	Dedupe             string         `json:"dedupe"`
	Audio              string         `json:"audio"`
	Rows               map[string]int `json:"rows"`
	Clips              int            `json:"clips"`
	Gone               []string       `json:"gone"`
}

// The archive's layout. Each JSONL file is present, empty or not, so a
// loader can name them all without checking.
const (
	archiveDPOTrain      = "dpo/train.jsonl"
	archiveDPOValidation = "dpo/validation.jsonl"
	archiveSFTTrain      = "sft/train.jsonl"
	archiveSFTValidation = "sft/validation.jsonl"
	archiveManifest      = "manifest.json"
)

// ClipPath is where a clip is inside an archive: its blob key as a WAV.
func ClipPath(ref string) (string, error) {
	key, err := blob.KeyFor(ref)
	if err != nil {
		return "", err
	}
	return "audio/" + key + ".wav", nil
}

// WriteArchive writes the dataset as one zip: the rows split into train and
// validation files, every clip they name as a WAV under audio/, and a
// manifest. A row's meta.audio.paths says where each of its kept clips is;
// a clip retention dropped, or missing from the store, is listed in its
// meta.audio.gone and has no path (ADR-0065, ADR-0071).
func (d Dataset) WriteArchive(ctx context.Context, w io.Writer, clips Clips, at time.Time) error {
	z := zip.NewWriter(w)
	paths := map[string]string{}
	gone := d.gone()
	for _, ref := range d.refs() {
		if gone[ref] {
			continue
		}
		path, err := ClipPath(ref)
		if err != nil {
			return fmt.Errorf("archive %s: %w", ref, err)
		}
		ok, err := writeClip(ctx, z, clips, ref, path, at)
		if err != nil {
			return err
		}
		if ok {
			paths[ref] = path
		} else {
			gone[ref] = true
		}
	}

	m := manifest{
		SchemaVersion:      SchemaVersion,
		ExportedAt:         stamp(at),
		ValidationFraction: d.validation,
		Split:              "sha256(conversation_id)[:8] as a big-endian fraction of 2^64; below validation_fraction is validation",
		Dedupe:             "one row per conversation, turn, context, rejected and chosen (speech and calls, whitespace collapsed); kept from barge-in, then annotation, then replay",
		Audio:              "WAV, 16 kHz, 16-bit signed, mono",
		Rows:               map[string]int{},
		Clips:              len(paths),
		Gone:               slices.Sorted(maps.Keys(gone)),
	}
	dpo := slices.Clone(d.dpo)
	for i := range dpo {
		dpo[i].Meta.Audio = located(dpo[i].Meta.Audio, paths, gone)
	}
	sft := slices.Clone(d.sft)
	for i := range sft {
		sft[i].Meta.Audio = locatedSFT(sft[i].Meta.Audio, paths, gone)
	}
	files := []struct {
		name  string
		write func(io.Writer) error
	}{
		{archiveDPOTrain, func(w io.Writer) error { return Dataset{dpo: dpo}.WriteDPO(w, SplitTrain) }},
		{archiveDPOValidation, func(w io.Writer) error { return Dataset{dpo: dpo}.WriteDPO(w, SplitValidation) }},
		{archiveSFTTrain, func(w io.Writer) error { return Dataset{sft: sft}.WriteSFT(w, SplitTrain) }},
		{archiveSFTValidation, func(w io.Writer) error { return Dataset{sft: sft}.WriteSFT(w, SplitValidation) }},
	}
	for _, f := range files {
		cw, err := z.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: at})
		if err != nil {
			return fmt.Errorf("archive %s: %w", f.name, err)
		}
		n := &lineCounter{w: cw}
		if err := f.write(n); err != nil {
			return fmt.Errorf("archive %s: %w", f.name, err)
		}
		m.Rows[f.name] = n.lines
	}
	mw, err := z.CreateHeader(&zip.FileHeader{Name: archiveManifest, Method: zip.Deflate, Modified: at})
	if err != nil {
		return fmt.Errorf("archive %s: %w", archiveManifest, err)
	}
	enc := json.NewEncoder(mw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("archive %s: %w", archiveManifest, err)
	}
	if err := z.Close(); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return nil
}

// refs is every clip the dataset's rows name, sorted, once each.
func (d Dataset) refs() []string {
	seen := map[string]bool{}
	for _, r := range d.dpo {
		for _, ref := range r.Meta.Audio.refs() {
			seen[ref] = true
		}
	}
	for _, r := range d.sft {
		for _, ref := range append([]string{r.Meta.Audio.Ask}, r.Meta.Audio.Said...) {
			seen[ref] = true
		}
	}
	delete(seen, "")
	return slices.Sorted(maps.Keys(seen))
}

// gone is every clip a row's log says is no longer kept.
func (d Dataset) gone() map[string]bool {
	out := map[string]bool{}
	for _, r := range d.dpo {
		for _, ref := range r.Meta.Audio.Gone {
			out[ref] = true
		}
	}
	for _, r := range d.sft {
		for _, ref := range r.Meta.Audio.Gone {
			out[ref] = true
		}
	}
	return out
}

// writeClip copies one clip into the archive as a WAV, or reports it gone.
// Anything but a missing blob fails the archive: a short corpus that looks
// whole is worse than none.
func writeClip(ctx context.Context, z *zip.Writer, clips Clips, ref, path string, at time.Time) (bool, error) {
	rc, err := clips.Open(ctx, ref)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("archive %s: %w", ref, err)
	}
	defer func() { _ = rc.Close() }()
	pcm, err := io.ReadAll(io.LimitReader(rc, maxClipBytes+1))
	if err != nil {
		return false, fmt.Errorf("archive %s: %w", ref, err)
	}
	if len(pcm) > maxClipBytes {
		return false, fmt.Errorf("archive %s: over %d bytes", ref, maxClipBytes)
	}
	// PCM barely deflates; storing it keeps the archive fast to write.
	cw, err := z.CreateHeader(&zip.FileHeader{Name: path, Method: zip.Store, Modified: at})
	if err != nil {
		return false, fmt.Errorf("archive %s: %w", path, err)
	}
	if _, err := cw.Write(WAVHeader(len(pcm))); err != nil {
		return false, fmt.Errorf("archive %s: %w", path, err)
	}
	if _, err := cw.Write(pcm); err != nil {
		return false, fmt.Errorf("archive %s: %w", path, err)
	}
	return true, nil
}

// located is a row's audio with each kept clip's path, and every clip that
// is not kept, by the log's word or the store's, listed gone.
func located(a Audio, paths map[string]string, gone map[string]bool) Audio {
	a.Paths = pathsOf(a.refs(), paths)
	a.Gone = goneOf(a.refs(), union(set(a.Gone), gone))
	return a
}

func locatedSFT(a sftAudio, paths map[string]string, gone map[string]bool) sftAudio {
	refs := append([]string{a.Ask}, a.Said...)
	a.Paths = pathsOf(refs, paths)
	a.Gone = goneOf(refs, union(set(a.Gone), gone))
	return a
}

func pathsOf(refs []string, paths map[string]string) map[string]string {
	out := map[string]string{}
	for _, ref := range refs {
		if p, ok := paths[ref]; ok {
			out[ref] = p
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func union(a, b map[string]bool) map[string]bool {
	out := maps.Clone(a)
	maps.Copy(out, b)
	return out
}

// lineCounter counts the rows written through it.
type lineCounter struct {
	w     io.Writer
	lines int
}

func (c *lineCounter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			c.lines++
		}
	}
	return c.w.Write(p)
}

// WAVHeader is the 44-byte PCM WAV preamble for n bytes of device audio
// (ADR-0007): the store keeps raw PCM, and a trainer's loader wants a file.
func WAVHeader(n int) []byte {
	const bytesPerFrame = bridge.BitsPerSample / 8
	h := make([]byte, 0, 44)
	h = append(h, "RIFF"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(36+n))
	h = append(h, "WAVEfmt "...)
	h = binary.LittleEndian.AppendUint32(h, 16) // fmt chunk size
	h = binary.LittleEndian.AppendUint16(h, 1)  // PCM
	h = binary.LittleEndian.AppendUint16(h, 1)  // mono
	h = binary.LittleEndian.AppendUint32(h, bridge.SampleRate)
	h = binary.LittleEndian.AppendUint32(h, bridge.SampleRate*bytesPerFrame)
	h = binary.LittleEndian.AppendUint16(h, bytesPerFrame)
	h = binary.LittleEndian.AppendUint16(h, bridge.BitsPerSample)
	h = append(h, "data"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(n))
	return h
}
