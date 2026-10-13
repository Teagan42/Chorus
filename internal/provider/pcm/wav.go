package pcm

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/teagan42/chorus/internal/bridge"
)

// WAVHeaderSize is the canonical 44-byte RIFF/WAVE header: one fmt chunk, one
// data chunk, nothing else.
const WAVHeaderSize = 44

const (
	wavFormatPCM        = 1
	wavFormatExtensible = 0xFFFE
	wavChannels         = 1
)

// WAV wraps device PCM in a header declaring the device's own format, so an
// endpoint that decodes by container resamples nothing and the samples reach
// the model as captured. Raw PCM has no rate to read, and ffmpeg-backed
// endpoints refuse it as undecodable.
func WAV(pcm []byte) []byte {
	const bytesPerFrame = bridge.BitsPerSample / 8
	out := make([]byte, WAVHeaderSize+len(pcm))
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(WAVHeaderSize-8+len(pcm)))
	copy(out[8:], "WAVE")
	copy(out[12:], "fmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], wavFormatPCM)
	binary.LittleEndian.PutUint16(out[22:], wavChannels)
	binary.LittleEndian.PutUint32(out[24:], bridge.SampleRate)
	binary.LittleEndian.PutUint32(out[28:], bridge.SampleRate*wavChannels*bytesPerFrame)
	binary.LittleEndian.PutUint16(out[32:], wavChannels*bytesPerFrame)
	binary.LittleEndian.PutUint16(out[34:], bridge.BitsPerSample)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(pcm)))
	copy(out[WAVHeaderSize:], pcm)
	return out
}

// Audio is decoded WAV: interleaved samples and the format they are in.
type Audio struct {
	Samples  []int16
	Rate     int
	Channels int
}

// ParseWAV reads a 16-bit PCM WAV, walking its chunks rather than assuming
// the canonical layout: servers add LIST and fact chunks.
//
// A data chunk that declares more than the buffer holds, or zero with
// samples after it, is read to the end of the buffer. Streaming writers
// cannot know the length when the header goes out, and Kokoro-FastAPI
// declares 0xFFFFFFFF even on a complete response (ADR-0023); the samples
// after the header are the same either way.
func ParseWAV(b []byte) (Audio, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return Audio{}, errors.New("wav: not a RIFF/WAVE file")
	}
	var (
		a       Audio
		haveFmt bool
		bits    int
	)
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4:]))
		body := off + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(b) {
				return Audio{}, errors.New("wav: fmt chunk is short")
			}
			format := binary.LittleEndian.Uint16(b[body:])
			a.Channels = int(binary.LittleEndian.Uint16(b[body+2:]))
			a.Rate = int(binary.LittleEndian.Uint32(b[body+4:]))
			bits = int(binary.LittleEndian.Uint16(b[body+14:]))
			if format != wavFormatPCM && format != wavFormatExtensible {
				return Audio{}, fmt.Errorf("wav: format %d is not integer PCM", format)
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return Audio{}, errors.New("wav: data before fmt")
			}
			if bits != 16 {
				return Audio{}, fmt.Errorf("wav: %d-bit samples, want 16", bits)
			}
			if a.Channels < 1 || a.Rate < 1 {
				return Audio{}, fmt.Errorf("wav: %d channels at %d Hz", a.Channels, a.Rate)
			}
			end := body + size
			if size == 0 || end > len(b) || end < body {
				end = len(b)
			}
			samples, err := Decode(b[body:end])
			if err != nil {
				return Audio{}, fmt.Errorf("wav: %w", err)
			}
			a.Samples = samples
			return a, nil
		}
		// Chunks are word-aligned: an odd size carries a pad byte.
		next := body + size + size%2
		if next <= off {
			break
		}
		off = next
	}
	return Audio{}, errors.New("wav: no data chunk")
}
