package pcm_test

import (
	"bytes"
	"encoding/binary"
	"slices"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/provider/pcm"
)

// The header is the only place an endpoint learns the rate. One wrong field
// and ffmpeg either refuses the file or resamples speech to the wrong pitch,
// and neither surfaces as an error here.
func TestTheHeaderDeclaresTheDeviceFormat(t *testing.T) {
	samples := []byte{1, 0, 2, 0, 3, 0}
	got := pcm.WAV(samples)

	if len(got) != pcm.WAVHeaderSize+len(samples) {
		t.Fatalf("wav is %d bytes, want %d", len(got), pcm.WAVHeaderSize+len(samples))
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
	case u32(40) != len(samples):
		t.Errorf("data size is %d, want %d", u32(40), len(samples))
	}
	if !bytes.Equal(got[pcm.WAVHeaderSize:], samples) {
		t.Error("the samples were altered")
	}
}

// wavAt builds a WAV the way a server might: any rate, channels and format,
// optional chunks before the data, and whatever data size it declares.
func wavAt(rate, channels, format, bits int, extra []byte, declared uint32, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(0))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(format))
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*channels*bits/8))
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels*bits/8))
	_ = binary.Write(&b, binary.LittleEndian, uint16(bits))
	b.Write(extra)
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, declared)
	b.Write(data)
	return b.Bytes()
}

func TestParseWAVReadsWhatServersSend(t *testing.T) {
	data := pcm.Encode([]int16{1, -2, 3, -4})
	// A LIST chunk with an odd size, so the pad byte is exercised too.
	list := append([]byte("LIST\x03\x00\x00\x00abc"), 0)

	for name, b := range map[string][]byte{
		"canonical":          wavAt(22050, 2, 1, 16, nil, uint32(len(data)), data),
		"chunk before data":  wavAt(22050, 2, 1, 16, list, uint32(len(data)), data),
		"streamed, size max": wavAt(22050, 2, 1, 16, nil, 0xFFFFFFFF, data),
		"streamed, size 0":   wavAt(22050, 2, 1, 16, nil, 0, data),
		"extensible":         wavAt(22050, 2, 0xFFFE, 16, nil, uint32(len(data)), data),
	} {
		a, err := pcm.ParseWAV(b)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if a.Rate != 22050 || a.Channels != 2 || !slices.Equal(a.Samples, []int16{1, -2, 3, -4}) {
			t.Errorf("%s: got %+v", name, a)
		}
	}
}

// Anything but 16-bit integer samples is refused by name, rather than read
// as noise at full scale.
func TestParseWAVRefusesWhatItCannotPlay(t *testing.T) {
	data := make([]byte, 8)
	for name, tc := range map[string]struct {
		b    []byte
		want string
	}{
		"float":    {wavAt(24000, 1, 3, 32, nil, 8, data), "integer PCM"},
		"24-bit":   {wavAt(24000, 1, 1, 24, nil, 6, data[:6]), "24-bit"},
		"not riff": {[]byte("ID3 this is an mp3"), "RIFF"},
		"no data":  {wavAt(24000, 1, 1, 16, nil, 0, nil)[:36], "no data"},
		"torn":     {wavAt(24000, 1, 1, 16, nil, 3, data[:3]), "whole 16-bit"},
	} {
		_, err := pcm.ParseWAV(tc.b)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, tc.want)
		}
	}
}

func TestDownmixAveragesChannels(t *testing.T) {
	if got := pcm.Downmix([]int16{100, 300, -100, -300, 7}, 2); !slices.Equal(got, []int16{200, -200}) {
		t.Errorf("downmix = %v, want [200 -200]", got)
	}
	mono := []int16{1, 2, 3}
	if got := pcm.Downmix(mono, 1); !slices.Equal(got, mono) {
		t.Errorf("mono changed: %v", got)
	}
}
