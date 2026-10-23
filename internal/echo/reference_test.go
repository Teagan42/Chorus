package echo

import (
	"encoding/binary"
	"testing"
)

// frames is n frames of one sample value, the way the satellite hands a
// synthesised delta over: 16-bit little-endian mono.
func frames(n int, v int16) []byte {
	b := make([]byte, 2*n)
	for i := range n {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

func at(r *Reference, frame int64) float64 { return r.span(frame, 1)[0] }

// The forecast's two deltas land back to back on the DAC's count, where the
// satellite placed them: the second starts where the first ended.
//
// verifies SPEC §3.2.1
func TestDeltasArePlacedOnTheDACsCount(t *testing.T) {
	r := NewReference(0)
	r.Write(16000, frames(8000, 1000)) // "Today will be cloudy in the morning,"
	r.Write(24000, frames(8000, 2000)) // " with rain from three"
	for _, c := range []struct {
		frame int64
		want  float64
	}{
		{15999, 0},
		{16000, 1000.0 / 32768},
		{23999, 1000.0 / 32768},
		{24000, 2000.0 / 32768},
		{31999, 2000.0 / 32768},
		{32000, 0},
	} {
		if got := at(r, c.frame); got != c.want {
			t.Errorf("frame %d = %v, want %v", c.frame, got, c.want)
		}
	}
}

// Teagan cut the forecast off a second in. The device discarded everything
// after the cut, so the room never heard it, and the answer to "do I need an
// umbrella" starts where the cut landed: what is held there is the new
// answer, never the old one's unheard tail.
//
// verifies SPEC §3.2.1, §4.4
func TestACutDropsWhatTheRoomNeverHeard(t *testing.T) {
	r := NewReference(0)
	r.Write(0, frames(64000, 1000)) // four seconds of forecast, sent ahead
	r.Cut(16000)                    // heard one
	if got := at(r, 20000); got != 0 {
		t.Errorf("frame 20000 after the cut = %v, want silence", got)
	}
	r.Write(16500, frames(24000, 3000)) // "Yes, rain from three."
	if got := at(r, 16200); got != 0 {
		t.Errorf("the gap before the next answer = %v, want silence", got)
	}
	if got := at(r, 20000); got != 3000.0/32768 {
		t.Errorf("frame 20000 = %v, want the new answer", got)
	}
	if got := at(r, 15999); got != 1000.0/32768 {
		t.Errorf("frame 15999 = %v, want the forecast that was heard", got)
	}
}

// A second answer written behind the end of the first -- the next utterance
// after a barge-in, whose base is the cut -- replaces what overlaps it.
//
// verifies SPEC §3.2.1
func TestAWriteBehindTheEndReplacesWhatFollows(t *testing.T) {
	r := NewReference(0)
	r.Write(0, frames(16000, 1000))
	r.Write(8000, frames(4000, 2000))
	if got := at(r, 9000); got != 2000.0/32768 {
		t.Errorf("frame 9000 = %v, want the newer answer", got)
	}
	if got := at(r, 13000); got != 0 {
		t.Errorf("frame 13000 = %v, want nothing past the newer answer", got)
	}
}

// A long evening is not kept: only the newest keep frames, which is more
// than any comparison reaches back.
//
// verifies SPEC §3.2.1
func TestOnlyTheNewestAudioIsKept(t *testing.T) {
	r := NewReference(16000)
	r.Write(0, frames(16000, 1000))
	r.Write(16000, frames(16000, 2000))
	if got := at(r, 8000); got != 0 {
		t.Errorf("frame 8000 = %v, want it dropped", got)
	}
	if got := at(r, 20000); got != 2000.0/32768 {
		t.Errorf("frame 20000 = %v, want it kept", got)
	}
	if n := len(r.pcm); n != 16000 {
		t.Errorf("holding %d frames, want 16000", n)
	}
	// An answer an hour later starts over rather than padding the hour.
	r.Write(16000*3600, frames(100, 3000))
	if n := len(r.pcm); n != 100 {
		t.Errorf("holding %d frames after an hour's gap, want 100", n)
	}
}

// A satellite with nowhere to keep its audio keeps none, and says nothing.
//
// verifies SPEC §3.2.1
func TestANilReferenceTakesNothing(t *testing.T) {
	var r *Reference
	r.Write(0, frames(160, 1000))
	r.Cut(0)
	if v := (Judge{}).Recent(r, frames(16000, 1000), 16000); v.Judged {
		t.Errorf("a nil reference judged %+v", v)
	}
}
