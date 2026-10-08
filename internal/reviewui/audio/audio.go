// Package audio serves journal blobs to a browser. Blobs are raw PCM in the
// device format (16 kHz s16le mono, ADR-0007); an <audio> element cannot
// play that, so the handler frames each one as a WAV on the way out. The
// store stays raw: the container is a property of this reader, not the data.
package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/teaganglenn/chorus/internal/blob"
	"github.com/teaganglenn/chorus/internal/bridge"
)

// maxBlobBytes bounds one response. Ten minutes of device-format PCM is
// ~19 MB; a blob past this is corrupt or not audio, and streaming it into a
// header that must state its length first would lie about one or the other.
const maxBlobBytes = 32 << 20

// headerLen is the fixed PCM WAV preamble this package writes.
const headerLen = 44

// Handler serves GET ?ref=blob://... as audio/wav from the store. Optional
// from and to bound the slice in device frames: the journal records the cut
// as a frame count, and a reviewer replaying what was heard needs playback
// to stop where the DAC did, with the unheard tail servable on its own.
func Handler(store blob.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, err := frameBound(r, "from", 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		to, err := frameBound(r, "to", maxBlobBytes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if to < from {
			http.Error(w, fmt.Sprintf("empty range: from=%d to=%d", from, to), http.StatusBadRequest)
			return
		}
		ref := r.URL.Query().Get("ref")
		rc, err := store.Open(r.Context(), ref)
		switch {
		case err == nil:
		case errors.Is(err, fs.ErrNotExist):
			http.NotFound(w, r)
			return
		default:
			// Open validates the ref before touching the filesystem, so any
			// other failure is the caller's reference, not the store.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer func() { _ = rc.Close() }()

		pcm, err := io.ReadAll(io.LimitReader(rc, maxBlobBytes+1))
		if err != nil {
			http.Error(w, fmt.Sprintf("read %s: %v", ref, err), http.StatusInternalServerError)
			return
		}
		if len(pcm) > maxBlobBytes {
			http.Error(w, fmt.Sprintf("%s exceeds %d bytes", ref, maxBlobBytes), http.StatusInsufficientStorage)
			return
		}
		pcm = pcm[min(from*bytesPerFrame, len(pcm)):min(to*bytesPerFrame, len(pcm))]

		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Length", strconv.Itoa(headerLen+len(pcm)))
		// The journal is append-only, so a ref's bytes never change.
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		if _, err := w.Write(header(len(pcm))); err != nil {
			return
		}
		_, _ = w.Write(pcm)
	})
}

// bytesPerFrame is the size of one mono device-format sample.
const bytesPerFrame = bridge.BitsPerSample / 8

// frameBound reads one frame-offset query parameter, absent meaning def.
func frameBound(r *http.Request, name string, def int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s: not a frame count: %q", name, v)
	}
	return n, nil
}

// header is the 44-byte PCM WAV preamble for n bytes of device-format audio.
func header(n int) []byte {
	const channels = 1
	h := make([]byte, 0, headerLen)
	le32 := func(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }
	le16 := func(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }

	h = append(h, "RIFF"...)
	h = append(h, le32(uint32(headerLen-8+n))...)
	h = append(h, "WAVE"...)
	h = append(h, "fmt "...)
	h = append(h, le32(16)...) // fmt chunk size
	h = append(h, le16(1)...)  // PCM
	h = append(h, le16(channels)...)
	h = append(h, le32(bridge.SampleRate)...)
	h = append(h, le32(bridge.SampleRate*bytesPerFrame)...) // byte rate
	h = append(h, le16(bytesPerFrame)...)                   // block align
	h = append(h, le16(bridge.BitsPerSample)...)
	h = append(h, "data"...)
	h = append(h, le32(uint32(n))...)
	return h
}

// DurationMS is how long a blob of n PCM bytes plays, for timeline layout.
func DurationMS(n int) int {
	return n * 1000 / (bridge.SampleRate * bridge.BitsPerSample / 8)
}
