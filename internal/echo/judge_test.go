package echo_test

import (
	"testing"

	"github.com/teagan42/chorus/internal/echo"
	"github.com/teagan42/chorus/internal/echo/echotest"
)

// The jazz playlist plays from frame base on this satellite's DAC: two
// seconds of the connection went before it.
const base = 2 * echotest.Rate

func playing(t *testing.T, clip string) (*echo.Reference, []byte) {
	t.Helper()
	played := echotest.Clip(t, clip)
	ref := echo.NewReference(0)
	ref.Write(base, played)
	return ref, played
}

// judge is the newest audio of uplink up to frame end of the playback, as the
// listener would hold it: the DAC reported base+end as the last of it came in.
func judge(ref *echo.Reference, uplink []byte, end int) echo.Verdict {
	return echo.Judge{}.Recent(ref, uplink[:2*end], uint64(base+end))
}

// "Playing Late Night Jazz from Spotify" comes back through the living
// room's microphone, 140 ms late, ten dB down and brought back up by the
// AGC. It is the device's own voice, found at the room's delay.
//
// verifies SPEC §4.3
func TestTheDevicesOwnVoiceIsAnEcho(t *testing.T) {
	ref, played := playing(t, "tts/jazz-playlist")
	uplink := echotest.Kitchen.Echo(played)
	for _, end := range []int{echotest.Rate, 2 * echotest.Rate, 3 * echotest.Rate} {
		v := judge(ref, uplink, end)
		if !v.Echo {
			t.Errorf("%d ms in: %+v, want an echo", 1000*end/echotest.Rate, v)
		}
		if lag := v.Lag * 1000 / echotest.Rate; lag < 130 || lag > 150 {
			t.Errorf("%d ms in: found at %d ms, want the room's 140", 1000*end/echotest.Rate, lag)
		}
	}
}

// Alan says "no, wait" over the playlist he asked for, half a second in,
// with the device's voice still leaking behind him. That is a person, and
// the gate must hear him.
//
// verifies SPEC §4.3
func TestAPersonTalkingOverTheEchoIsNotOne(t *testing.T) {
	ref, played := playing(t, "tts/jazz-playlist")
	alan := echotest.Clip(t, "mic/jazz-bargein")
	at := echotest.Rate / 2
	uplink := echotest.Kitchen.Over(played, alan, at)
	v := judge(ref, uplink, at+len(alan)/2)
	if !v.Judged || v.Echo {
		t.Errorf("Alan over the echo: %+v, want judged and no echo", v)
	}
}

// Teagan cuts the forecast off on a satellite whose canceller leaves nothing
// behind: only her voice is on the uplink. It is compared -- the device was
// playing -- and is nothing like what it played.
//
// verifies SPEC §4.3
func TestAPersonWithNoEchoBehindThemIsNotOne(t *testing.T) {
	ref, played := playing(t, "tts/weather-forecast")
	teagan := echotest.Clip(t, "mic/weather-bargein")
	silent := echotest.Kitchen
	silent.Leak = -120
	at := echotest.Rate
	v := judge(ref, silent.Over(played, teagan, at), at+len(teagan)/2)
	if !v.Judged || v.Echo || v.Score > 0.4 {
		t.Errorf("Teagan alone: %+v, want judged, no echo, and nowhere near", v)
	}
}

// Nothing to compare is no verdict, so the gate's echo stage passes: a
// reference that was never written, playback that ended long before the
// audio arrived, and a "yes" too short to line up with anything by more than
// chance.
//
// verifies SPEC §4.3
func TestWithNothingToCompareThereIsNoVerdict(t *testing.T) {
	_, played := playing(t, "tts/door-confirm")
	teagan := echotest.Clip(t, "mic/door-ask")
	if v := (echo.Judge{}).Recent(nil, teagan, base); v.Judged {
		t.Errorf("no reference: %+v", v)
	}

	ref, _ := playing(t, "tts/door-confirm")
	long := uint64(base + len(played)/2 + 10*echotest.Rate)
	if v := (echo.Judge{}).Recent(ref, teagan, long); v.Judged {
		t.Errorf("ten seconds after the answer ended: %+v", v)
	}

	yes := echotest.Clip(t, "mic/door-yes")
	v := (echo.Judge{}).Recent(ref, yes[:2*echotest.Rate/5], uint64(base+echotest.Rate))
	if v.Judged {
		t.Errorf("a 200 ms yes: %+v", v)
	}
}

// What a stop kept from the room is not an echo of anything. The forecast
// was cut a second in; audio shaped like its unheard tail arriving now is
// not the device's voice, because the device never said it.
//
// verifies SPEC §4.3, §4.4
func TestWhatAStopDiscardedIsNotAnEcho(t *testing.T) {
	ref, played := playing(t, "tts/weather-forecast")
	ref.Cut(base + echotest.Rate)
	uplink := echotest.Kitchen.Echo(played)
	// The two seconds after the cut, arriving as if the device had played on.
	v := echo.Judge{}.Recent(ref, uplink[2*2*echotest.Rate:2*3*echotest.Rate], uint64(base+3*echotest.Rate))
	if v.Echo {
		t.Errorf("the unheard tail: %+v, want no echo", v)
	}
}

// The threshold is the satellite's to set (devices.yaml echo_threshold):
// above any score turns the stage off and still reports what it found.
//
// verifies SPEC §4.3
func TestTheThresholdIsTheSatellites(t *testing.T) {
	ref, played := playing(t, "tts/jazz-playlist")
	uplink := echotest.Kitchen.Echo(played)
	v := echo.Judge{Threshold: 1.01}.Recent(ref, uplink[:2*2*echotest.Rate], base+2*echotest.Rate)
	if v.Echo || !v.Judged || v.Score < echo.DefaultThreshold {
		t.Errorf("threshold over 1: %+v, want judged, scored, and no echo", v)
	}
}

// Judged before the embedder on every candidate while the device speaks, so
// it has to be cheap next to the ~300 ms the gate has (SPEC §4.3).
func BenchmarkRecent(b *testing.B) {
	played := echotest.Clip(b, "tts/weather-forecast")
	ref := echo.NewReference(0)
	ref.Write(base, played)
	uplink := echotest.Kitchen.Echo(played)[:2*2*echotest.Rate]
	for b.Loop() {
		echo.Judge{}.Recent(ref, uplink, base+2*echotest.Rate)
	}
}
