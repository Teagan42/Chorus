package speaches

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The header is the only place the endpoint learns the rate. One wrong field
// and ffmpeg either refuses the file or resamples speech to the wrong pitch,
// and neither surfaces as an error here.
func TestTheHeaderDeclaresTheDeviceFormat(t *testing.T) {
	pcm := []byte{1, 0, 2, 0, 3, 0}
	got := wav(pcm)

	if len(got) != wavHeaderSize+len(pcm) {
		t.Fatalf("wav is %d bytes, want %d", len(got), wavHeaderSize+len(pcm))
	}
	for off, want := range map[int]string{0: "RIFF", 8: "WAVE", 12: "fmt ", 36: "data"} {
		if string(got[off:off+4]) != want {
			t.Errorf("offset %d is %q, want %q", off, got[off:off+4], want)
		}
	}
	u16 := func(off int) int { return int(binary.LittleEndian.Uint16(got[off:])) }
	u32 := func(off int) int { return int(binary.LittleEndian.Uint32(got[off:])) }
	switch {
	case u32(4) != len(got)-8:
		t.Errorf("riff size is %d, want %d", u32(4), len(got)-8)
	case u32(16) != 16:
		t.Errorf("fmt chunk is %d bytes, want 16", u32(16))
	case u16(20) != 1:
		t.Errorf("format tag is %d, want 1 (pcm)", u16(20))
	case u16(22) != 1:
		t.Errorf("channels is %d, want mono", u16(22))
	case u32(24) != 16000:
		t.Errorf("rate is %d, want 16000", u32(24))
	case u32(28) != 32000:
		t.Errorf("byte rate is %d, want 32000", u32(28))
	case u16(32) != 2:
		t.Errorf("block align is %d, want 2", u16(32))
	case u16(34) != 16:
		t.Errorf("bits is %d, want 16", u16(34))
	case u32(40) != len(pcm):
		t.Errorf("data size is %d, want %d", u32(40), len(pcm))
	}
	if !bytes.Equal(got[wavHeaderSize:], pcm) {
		t.Error("the samples were altered")
	}
}
