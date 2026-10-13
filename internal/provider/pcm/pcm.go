// Package pcm is the audio plumbing the speech providers share: device-format
// WAV for the endpoints that want a container, WAV and raw sample decoding
// for the ones that answer in one, and a resampler from whatever rate a voice
// renders at to the device's (SPEC §3.2).
//
// Kokoro keeps its own exact 3:2 kernels (ADR-0023); this is for the
// providers whose rate is only known once the audio arrives.
package pcm

import (
	"encoding/binary"
	"fmt"

	"github.com/teagan42/chorus/internal/bridge"
)

// Decode reads little-endian 16-bit samples. An odd length is a truncated
// response, not a sample to round off: every later sample would be a byte
// out of phase and the utterance would come out as noise.
func Decode(b []byte) ([]int16, error) {
	if len(b)%2 != 0 {
		return nil, fmt.Errorf("%d bytes is not whole 16-bit samples", len(b))
	}
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out, nil
}

// Encode writes samples little-endian, the device's byte order.
func Encode(samples []int16) []byte {
	out := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(s))
	}
	return out
}

// Downmix averages interleaved channels to mono. One channel is returned as
// it is; a trailing partial frame is dropped, since it has no partner to
// average with.
func Downmix(samples []int16, channels int) []int16 {
	if channels <= 1 {
		return samples
	}
	out := make([]int16, len(samples)/channels)
	for i := range out {
		var acc int
		for c := range channels {
			acc += int(samples[i*channels+c])
		}
		out[i] = int16(acc / channels)
	}
	return out
}

// ToDevice turns samples at any rate and channel count into device PCM:
// 16 kHz, signed 16-bit, mono.
func ToDevice(samples []int16, rate, channels int) ([]byte, error) {
	r, err := NewResampler(rate)
	if err != nil {
		return nil, err
	}
	return Encode(r.Run(Downmix(samples, channels))), nil
}

// DeviceRate is the rate everything here converts to.
const DeviceRate = bridge.SampleRate
