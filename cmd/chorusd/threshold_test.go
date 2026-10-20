package main

import (
	"context"
	"encoding/binary"
	"log/slog"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/listen"
)

// voiceAt is one sample at amp: enough for the fake embedder to look up.
func voiceAt(amp int16) []byte {
	pcm := make([]byte, 2)
	binary.LittleEndian.PutUint16(pcm, uint16(amp))
	return pcm
}

// A satellite that names its own speaker_accept is matched at that level;
// the rest keep the house's. Alan on a far-field satellite scores 0.6
// against his own voiceprint -- under the 0.70 default, so a guest there,
// and himself where the satellite says 0.5 (ADR-0084, ADR-0098).
//
// verifies SPEC §5
func TestASatelliteMatchesAtItsOwnAcceptLevel(t *testing.T) {
	emb := newEmbedder()
	ids := identity.New(emb)
	if err := ids.Enroll("alan", "Alan", [][]float32{axis(0), axis(0), axis(0)}); err != nil {
		t.Fatal(err)
	}
	house, err := identity.NewResolver(emb, ids, identity.Thresholds{})
	if err != nil {
		t.Fatal(err)
	}
	const far = int16(9000)
	emb.by[far] = []float32{0.6, 0.8, 0, 0}

	inv := inventory()
	inv.Satellites[1].SpeakerAccept = 0.5 // office, the Voice PE
	var asked []float64
	d := deps{providers: providers{
		speakers: house,
		speakersAt: func(accept float64) (listen.Speakers, error) {
			asked = append(asked, accept)
			return identity.NewResolver(emb, ids, identity.Thresholds{Accept: accept})
		},
	}}
	dm, err := newDaemon(inv, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != 0.5 {
		t.Errorf("built resolvers at %v, want one at 0.5 for the office alone", asked)
	}
	for sat, want := range map[string]string{"kitchen": "", "office": "alan"} {
		out, err := dm.speakersFor(sat).Resolve(context.Background(), voiceAt(far))
		if err != nil {
			t.Fatal(err)
		}
		if out.PersonID != want {
			t.Errorf("%s: matched %q (%s), want %q", sat, out.PersonID, out.Reason, want)
		}
	}
	if dm.speakersFor("kitchen") != listen.Speakers(house) {
		t.Error("a satellite naming no level did not get the house's resolver")
	}
}

// Without speaker identification the setting does nothing, and says so
// rather than reading as if it took.
func TestSpeakerAcceptWithoutSpeakerIDIsSaid(t *testing.T) {
	inv := inventory()
	inv.Satellites[0].SpeakerAccept = 0.6
	var logs strings.Builder
	dm, err := newDaemon(inv, deps{Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if dm.speakersFor("kitchen") != nil {
		t.Error("a resolver appeared with nothing to identify speakers")
	}
	if !strings.Contains(logs.String(), "speaker_accept has no effect") {
		t.Errorf("not said:\n%s", logs.String())
	}
}
