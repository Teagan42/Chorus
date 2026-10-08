package speaches

import (
	"encoding/binary"

	"github.com/teaganglenn/chorus/internal/bridge"
)

// wavHeaderSize is the canonical 44-byte RIFF/WAVE header: one fmt chunk, one
// data chunk, nothing else. The endpoint decodes with ffmpeg, which wants a
// container; raw PCM has no rate to read and is refused as undecodable.
const wavHeaderSize = 44

const (
	wavFormatPCM = 1
	wavChannels  = 1
)

// wav wraps device PCM in a header declaring the device's own format, so the
// endpoint resamples nothing and the samples reach the model as captured.
func wav(pcm []byte) []byte {
	const bytesPerFrame = bridge.BitsPerSample / 8
	out := make([]byte, wavHeaderSize+len(pcm))
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(wavHeaderSize-8+len(pcm)))
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
	copy(out[wavHeaderSize:], pcm)
	return out
}
