package harvest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
)

func dropped(ref, reason string) journal.Record {
	r := record(journal.KindAudioDropped, "", "audio_ref", ref, "reason", reason)
	if reason == "retention" {
		r.Fields["days"] = "30"
	}
	return r
}

// dayAfter is a month on from the kitchen log's Thursday, once its 30 days ran out.
var dayAfter = time.Date(2025, time.November, 9, 3, 0, 0, 0, time.UTC)

// The kitchen's barge-in from a month ago lost its clip of the cut speech to
// retention, and the disk was full when the correction was said. The pair
// still harvests, its refs where they were so each clip lines up with its
// text, and says which of them are gone: a loader skips those instead of
// failing to open them.
//
// verifies SPEC §8, §9.1
func TestAPairSaysWhichOfItsClipsAreGone(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn(),
		[]journal.Record{
			closed("model_ended"),
			dropped("blob://tts/s1", "retention"),
			dropped("blob://mic/3", "disk_low"),
		},
	))
	res := scan(t, store)
	if len(res.Pairs) != 1 {
		t.Fatalf("got %d pairs, want the barge-in", len(res.Pairs))
	}
	a := res.Pairs[0].Audio
	if !slices.Equal(a.Rejected, []string{"blob://tts/s1"}) || a.Correction != "blob://mic/3" {
		t.Errorf("audio = %+v; a gone clip keeps its place beside its text", a)
	}
	if want := []string{"blob://mic/3", "blob://tts/s1"}; !slices.Equal(a.Gone, want) {
		t.Errorf("gone = %v, want %v", a.Gone, want)
	}

	var buf bytes.Buffer
	if err := harvest.Export(&buf, res.Pairs); err != nil {
		t.Fatal(err)
	}
	var row struct {
		Meta struct {
			Audio struct {
				Gone []string `json:"gone"`
			} `json:"audio"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(buf.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if want := []string{"blob://mic/3", "blob://tts/s1"}; !slices.Equal(row.Meta.Audio.Gone, want) {
		t.Errorf("exported gone = %v, want %v", row.Meta.Audio.Gone, want)
	}
}

// A pair whose clips are all kept exports no gone list at all, so a row from
// before retention reads exactly as it did.
//
// verifies SPEC §9.1
func TestAPairWithEveryClipKeptExportsNoGoneList(t *testing.T) {
	store := conversation(t, versions(), concat([]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn()))
	var buf bytes.Buffer
	if err := harvest.Export(&buf, scan(t, store).Pairs); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte(`"gone"`)) {
		t.Errorf("export names gone clips when none are: %s", buf.Bytes())
	}
}

// The dishwasher's rejected wake outlived the kitchen's 30 days of audio. It
// is still a row of the wake corpus, saying its audio is gone, so a trainer
// drops it rather than opening a ref with nothing behind it.
//
// verifies SPEC §8, §9.3
func TestANegativeWhoseAudioIsGoneSaysSo(t *testing.T) {
	store := kitchenLog(t)
	j := journal.New(store, journal.FixedClock(dayAfter), versions())
	for _, ref := range []string{"blob://wake/kitchen-dishwasher", "blob://wake/kitchen-dishwasher-second"} {
		if _, err := j.Append(context.Background(), "device:kitchen", dropped(ref, "retention")); err != nil {
			t.Fatal(err)
		}
	}
	ns, err := harvest.Negatives(context.Background(), store, "device:kitchen")
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 {
		t.Fatalf("got %d negatives, want the dishwasher and the guest", len(ns))
	}
	if !ns[0].AudioGone || ns[1].AudioGone {
		t.Errorf("audio gone = %v, %v; want only the dishwasher's", ns[0].AudioGone, ns[1].AudioGone)
	}

	var buf bytes.Buffer
	if err := harvest.ExportNegatives(&buf, ns); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	var dish, guest map[string]any
	if err := json.Unmarshal(lines[0], &dish); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &guest); err != nil {
		t.Fatal(err)
	}
	if dish["audio_gone"] != true {
		t.Errorf("dishwasher row = %v; want audio_gone true", dish)
	}
	if _, ok := guest["audio_gone"]; ok {
		t.Errorf("guest row = %v; a kept clip says nothing of being gone", guest)
	}
}
