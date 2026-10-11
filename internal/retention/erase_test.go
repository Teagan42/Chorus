package retention_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/retention"
	"github.com/teagan42/chorus/internal/session"
)

// summaries stands in for the memory store's summaries of each conversation.
type summaries struct{ forgot []string }

func (s *summaries) ForgetSummaries(_ context.Context, conversationID string) error {
	s.forgot = append(s.forgot, conversationID)
	return nil
}

func (h *house) eraser(sums *summaries) *retention.Eraser {
	e := &retention.Eraser{
		Journal: journal.New(h.store, h.clock, journal.Versions{}), Store: h.store, Blobs: h.blobs,
		Curation: h.verdict, NotKept: h.guard, Clock: h.clock,
	}
	if sums != nil {
		e.Summaries = sums
	}
	return e
}

// guestVisit is Alan's sister in the living room on Saturday evening: Alan
// wakes it, she asks for a pharmacy, and the session closes.
func guestVisit() []journal.Record {
	return []journal.Record{
		opened("living_room", "alan"),
		rec(journal.KindUtteranceTranscribed, "blob://mic/guest-ask", "text", "is there a pharmacy open tonight", "speaker_id", "", "second_audio_ref", "blob://mic/guest-ask.ch1"),
		rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop"),
		rec(journal.KindSpeechSpoken, "blob://tts/guest-pharmacy", "text", "The one on Mill Road is open until ten.", "frames_played", "24000"),
		closed("living_room"),
	}
}

// Alan asks the review UI to delete his sister's visit an hour after she
// left. It goes the way the journal horizon deletes a conversation: both
// mic channels and the voice, the reviewer's label on her turn, the
// summaries of it, and then the log. Every other conversation is untouched.
//
// verifies SPEC §8
func TestAReviewerDeletesTheGuestsVisitByRetentionsPath(t *testing.T) {
	h := newHouse(t)
	ctx := context.Background()
	const visit, other = "conv-1930-living_room", "conv-0705-kitchen"
	h.at(today.Add(-time.Hour), visit, guestVisit()...)
	h.at(today.Add(-2*time.Hour), other, weather("kitchen", "k0")...)
	if err := h.verdict.PutAnnotation(ctx, curation.Annotation{ConversationID: visit, Seq: 2, Labels: []curation.Label{curation.LabelWrongPerson}, AnnotatedAt: today}); err != nil {
		t.Fatal(err)
	}
	sums := &summaries{}

	erased, err := h.eraser(sums).DeleteConversation(ctx, visit)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if erased.Events != 5 || erased.Clips != 3 || !erased.Curated {
		t.Errorf("erased = %+v, want 5 events, 3 clips, curated", erased)
	}
	for _, ref := range []string{"blob://mic/guest-ask", "blob://mic/guest-ask.ch1", "blob://tts/guest-pharmacy"} {
		if h.kept(ref) {
			t.Errorf("%s outlived the deletion", ref)
		}
	}
	if got := h.log(visit); len(got) != 0 {
		t.Errorf("the visit still has %d events", len(got))
	}
	if as, _ := h.verdict.Annotations(ctx, visit); len(as) != 0 {
		t.Errorf("the label on her turn outlived the deletion: %v", as)
	}
	if !slices.Equal(sums.forgot, []string{visit}) {
		t.Errorf("summaries forgotten for %v, want the visit only", sums.forgot)
	}
	if len(h.log(other)) != 5 || !h.kept("blob://tts/k0-forecast") {
		t.Error("deleting the visit touched Teagan's weather")
	}
}

// A conversation someone is talking in, or one they could still walk back
// into, is not the review UI's to delete: chorusd is about to write to it
// (ADR-0022). Nor is a satellite's or the house's own log, which chorusd
// writes all day, nor a log that does not exist.
//
// verifies SPEC §4.5, §8
func TestDeletionRefusesALiveOrResumableLog(t *testing.T) {
	h := newHouse(t)
	ctx := context.Background()
	h.at(today.Add(-40*day), "conv-kitchen-live", weather("kitchen", "live")[:4]...)
	h.at(today.Add(-time.Minute), "conv-kitchen-live",
		rec(journal.KindUtteranceTranscribed, "blob://mic/live-now", "text", "and tomorrow?", "speaker_id", "teagan"))
	h.at(today.Add(-time.Minute), "conv-1930-living_room", guestVisit()...)
	h.at(today.Add(-time.Hour), "device:kitchen", rec(journal.KindWakeRejected, "blob://wake/cough", "reason", "no_speech"))
	e := h.eraser(nil)

	for id, want := range map[string]error{
		"conv-kitchen-live":     retention.ErrLive,
		"conv-1930-living_room": retention.ErrResumable,
		"device:kitchen":        retention.ErrLongLived,
		journal.HouseTimers:     retention.ErrLongLived,
		"conv-0000-attic":       retention.ErrNoLog,
	} {
		before := h.log(id)
		if _, err := e.DeleteConversation(ctx, id); !errors.Is(err, want) {
			t.Errorf("delete %s = %v, want %v", id, err, want)
		}
		if got := h.log(id); len(got) != len(before) {
			t.Errorf("a refused delete of %s still changed its log", id)
		}
	}
	if !h.kept("blob://mic/live-now") || !h.kept("blob://mic/guest-ask") {
		t.Error("a refused delete removed audio")
	}
}

// The resume window is the session's: a conversation the same person may
// still walk back into is exactly the one chorusd may open again.
//
// verifies SPEC §4.5
func TestTheResumeWindowIsTheSessionsMigrationWindow(t *testing.T) {
	if retention.DefaultResume != session.MigrationWindow {
		t.Errorf("DefaultResume = %v, session.MigrationWindow = %v", retention.DefaultResume, session.MigrationWindow)
	}
}

// Alan keeps the visit but drops what his sister said: both channels of
// her ask go, each recorded as redacted where retention records its own,
// and the voice's answer still plays. Dropping it again finds nothing to
// drop, and the log still replays.
//
// verifies SPEC §8
func TestAReviewerDropsOneTurnsAudioAsRedacted(t *testing.T) {
	h := newHouse(t)
	ctx := context.Background()
	const visit = "conv-1930-living_room"
	h.at(today.Add(-time.Hour), visit, guestVisit()...)
	e := h.eraser(nil)

	refs, err := e.DropAudio(ctx, visit, 2)
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if want := []string{"blob://mic/guest-ask", "blob://mic/guest-ask.ch1"}; !slices.Equal(refs, want) {
		t.Errorf("dropped %v, want %v", refs, want)
	}
	if h.kept("blob://mic/guest-ask") || h.kept("blob://mic/guest-ask.ch1") {
		t.Error("her ask is still kept")
	}
	if !h.kept("blob://tts/guest-pharmacy") {
		t.Error("dropping her ask removed the answer")
	}
	want := map[string]string{"blob://mic/guest-ask": "redacted/", "blob://mic/guest-ask.ch1": "redacted/"}
	if got := h.droppedIn(visit); !maps.Equal(got, want) {
		t.Errorf("recorded = %v, want %v", got, want)
	}
	if _, err := e.DropAudio(ctx, visit, 2); !errors.Is(err, retention.ErrNoAudio) {
		t.Errorf("a second drop = %v, want ErrNoAudio", err)
	}
	if _, err := e.DropAudio(ctx, visit, 3); !errors.Is(err, retention.ErrNoAudio) {
		t.Errorf("dropping a completion's audio = %v, want ErrNoAudio", err)
	}
	if _, err := e.DropAudio(ctx, visit, 99); !errors.Is(err, retention.ErrNoEvent) {
		t.Errorf("dropping #99 = %v, want ErrNoEvent", err)
	}
	if _, err := journal.Replay(ctx, h.store, visit, journal.Overrides{}); err != nil {
		t.Errorf("the redacted log does not replay: %v", err)
	}
}

// A clip in a live conversation, a resumable one or a satellite's own log
// stays: the record would race chorusd's next write to that log.
//
// verifies SPEC §4.5, §8
func TestDroppingAudioRefusesALiveOrLongLivedLog(t *testing.T) {
	h := newHouse(t)
	ctx := context.Background()
	h.at(today.Add(-time.Minute), "conv-kitchen-live", weather("kitchen", "live")[:4]...)
	h.at(today.Add(-time.Minute), "conv-1930-living_room", guestVisit()...)
	h.at(today.Add(-time.Hour), "device:kitchen", rec(journal.KindWakeRejected, "blob://wake/cough", "reason", "no_speech"))
	e := h.eraser(nil)
	for id, want := range map[string]error{
		"conv-kitchen-live":     retention.ErrLive,
		"conv-1930-living_room": retention.ErrResumable,
		"device:kitchen":        retention.ErrLongLived,
		"conv-0000-attic":       retention.ErrNoLog,
	} {
		if _, err := e.DropAudio(ctx, id, 1); !errors.Is(err, want) {
			t.Errorf("drop in %s = %v, want %v", id, err, want)
		}
	}
	for _, ref := range []string{"blob://mic/live-ask", "blob://mic/guest-ask", "blob://wake/cough"} {
		if !h.kept(ref) {
			t.Errorf("a refused drop removed %s", ref)
		}
	}
}

// The review UI asks before it offers a deletion, and is told what the
// deletion itself would say.
//
// verifies SPEC §4.5, §8
func TestRefusesSaysWhatADeletionWouldRefuse(t *testing.T) {
	h := newHouse(t)
	h.at(today.Add(-time.Minute), "conv-1930-living_room", guestVisit()...)
	h.at(today.Add(-time.Hour), "conv-0705-kitchen", weather("kitchen", "k0")...)
	e := h.eraser(nil)
	for id, want := range map[string]error{
		"conv-1930-living_room": retention.ErrResumable,
		"conv-0705-kitchen":     nil,
		"device:kitchen":        retention.ErrLongLived,
		"conv-0000-attic":       retention.ErrNoLog,
	} {
		if err := e.Refuses(id, h.log(id)); !errors.Is(err, want) {
			t.Errorf("Refuses(%s) = %v, want %v", id, err, want)
		}
	}
}
