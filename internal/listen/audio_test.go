package listen_test

import "encoding/binary"

// chunkBytes is one 32 ms uplink chunk, the firmware's own send size
// (SPEC §3.2).
const chunkBytes = 1024

// voice is n bytes of a constant sample. A test's "voice" is an amplitude:
// the scripted embedder maps the peak sample to a speaker, so two amplitudes
// are two people without any model in the loop.
func voice(amplitude int16, n int) []byte {
	out := make([]byte, n)
	for i := 0; i+1 < n; i += 2 {
		binary.LittleEndian.PutUint16(out[i:], uint16(amplitude))
	}
	return out
}

// quiet is n bytes of digital silence.
func quiet(n int) []byte { return make([]byte, n) }
