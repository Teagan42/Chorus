package satellite_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/teagan42/chorus/internal/echo"
	"github.com/teagan42/chorus/internal/echo/echotest"
	"github.com/teagan42/chorus/internal/satellite"
)

// voiced renders each delta as the assistant actually said it on the
// household's evening: Kokoro's audio, cut where the text is.
type voiced map[string][]byte

func (v voiced) Synthesize(_ context.Context, text string) ([]byte, error) {
	pcm, ok := v[text]
	if !ok {
		return nil, fmt.Errorf("the household never heard %q", text)
	}
	return pcm, nil
}

// The living room's evening: "Finding some jazz.", then the playlist, in
// deltas the satellite keeps whole, the first ending where voice.json says
// Alan cut it.
func livingRoom(t *testing.T) (voiced, []byte, []byte) {
	t.Helper()
	ack := echotest.Clip(t, "tts/jazz-ack")
	playlist := echotest.Clip(t, "tts/jazz-playlist")
	const heard, spotify = 17600, 28800 // frames: voice.json's heard_frames, then a guess
	return voiced{
		"Finding some jazz.":        ack,
		"Playing Late Night Jazz":   playlist[:2*heard],
		" from Spotify,":            playlist[2*heard : 2*spotify],
		" starting with Take Five.": playlist[2*spotify:],
	}, ack, playlist
}

var playlistDeltas = []string{"Playing Late Night Jazz", " from Spotify,", " starting with Take Five."}

// What the satellite sends is what the echo stage compares the mic against,
// on the same count: after "Finding some jazz." has played, the playlist's
// own echo, arriving 140 ms behind the DAC, is found 140 ms behind the DAC.
// A reference placed from the connection's start rather than this
// utterance's base would find nothing, or find it two seconds out.
//
// verifies SPEC §3.2.1, §4.3
func TestTheReferenceIsWhatTheDACPlayedWhereItPlayedIt(t *testing.T) {
	voices, ack, playlist := livingRoom(t)
	ref := echo.NewReference(0)
	r := newRig(t, func(c *satellite.Config) { c.Synth, c.Reference = voices, ref })

	first, _ := r.open(t, "call-ack")
	if err := first.Write("Finding some jazz."); err != nil {
		t.Fatal(err)
	}
	r.dev.AwaitTTS(t, len(ack))
	done := closeAsync(first)
	r.dev.AwaitFinish(t, 1)
	r.playAll(t)
	await(t, done)

	second, _ := r.open(t, "call-playlist")
	for _, d := range playlistDeltas {
		if err := second.Write(d); err != nil {
			t.Fatal(err)
		}
	}
	r.dev.AwaitTTS(t, len(ack)+len(playlist))
	base := r.sat.SpeechBase()
	r.play(t, 2*echotest.Rate)

	uplink := echotest.Kitchen.Echo(playlist)
	v := echo.Judge{}.Recent(ref, uplink[:2*2*echotest.Rate], base+2*echotest.Rate)
	if !v.Echo {
		t.Fatalf("the playlist's own echo: %+v, want an echo", v)
	}
	if lag := v.Lag * 1000 / echotest.Rate; lag < 130 || lag > 150 {
		t.Errorf("found %d ms behind the DAC, want the room's 140", lag)
	}
	done = closeAsync(second)
	r.dev.AwaitFinish(t, 2)
	r.playAll(t)
	await(t, done)
}

// Alan cut the playlist off at "Playing Late Night Jazz". The stop discarded
// the rest on the device, so the reference drops it too: audio arriving now
// shaped like " from Spotify" is not the device's voice, because the room
// never heard the device say it. What was heard is still compared.
//
// verifies SPEC §3.2.1, §4.3, §4.4
func TestACutTakesTheUnheardTailOutOfTheReference(t *testing.T) {
	voices, _, playlist := livingRoom(t)
	ref := echo.NewReference(0)
	r := newRig(t, func(c *satellite.Config) { c.Synth, c.Reference = voices, ref })

	st, cancel := r.open(t, "call-playlist")
	for _, d := range playlistDeltas {
		if err := st.Write(d); err != nil {
			t.Fatal(err)
		}
	}
	r.dev.AwaitTTS(t, len(playlist))
	const heard = 17600
	r.play(t, heard)
	cancel()
	if pb := await(t, closeAsync(st)); pb.Frames != heard {
		t.Fatalf("cut at %d frames, want %d", pb.Frames, heard)
	}

	uplink := echotest.Kitchen.Echo(playlist)
	if v := (echo.Judge{}).Recent(ref, uplink[:2*heard], heard); !v.Echo {
		t.Errorf("what Alan heard: %+v, want an echo", v)
	}
	tail := uplink[2*(heard+echotest.Rate/2) : 2*(heard+echotest.Rate+echotest.Rate/2)]
	if v := (echo.Judge{}).Recent(ref, tail, heard+echotest.Rate+echotest.Rate/2); v.Echo {
		t.Errorf("what the stop discarded: %+v, want no echo", v)
	}
}
