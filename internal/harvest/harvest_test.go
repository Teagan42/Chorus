package harvest_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/harvest"
	"github.com/teaganglenn/chorus/internal/journal"
)

func versions() journal.Versions {
	return journal.Versions{
		Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7",
		STT: "istupakov/parakeet-tdt-0.6b-v2-onnx", TTS: "kokoro/af_heart",
	}
}

func record(kind journal.Kind, audio string, fields ...string) journal.Record {
	r := journal.Record{Kind: kind, AudioRef: audio, Fields: map[string]string{}}
	for i := 0; i+1 < len(fields); i += 2 {
		r.Fields[fields[i]] = fields[i+1]
	}
	return r
}

func opened(satellite string) journal.Record {
	return record(journal.KindSessionOpened, "", "satellite", satellite, "speaker_id", "alice", "resumed", "false")
}

func closed(reason string) journal.Record {
	return record(journal.KindSessionClosed, "", "reason", reason, "satellite", "kitchen")
}

func heard(text, audio string) journal.Record {
	return record(journal.KindUtteranceTranscribed, audio, "text", text, "speaker_id", "alice")
}

func completed() journal.Record {
	return record(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")
}

func speak(id string) journal.Record {
	return record(journal.KindToolCalled, "", "tool", "speak", "call_id", id, "args_json", `{"mode":"queue","streamed":true}`)
}

func cancelled(id string) journal.Record {
	return record(journal.KindToolResult, "", "call_id", id, "outcome", "cancelled")
}

func spoken(text, audio string) journal.Record {
	return record(journal.KindSpeechSpoken, audio, "text", text, "frames_played", "16000")
}

func truncated(spoken, unspoken, audio string) journal.Record {
	return record(journal.KindSpeechTruncated, audio, "spoken_text", spoken, "unspoken_text", unspoken, "frames_played", "2080")
}

func discarded(text string) journal.Record {
	return record(journal.KindSpeechDiscarded, "", "unspoken_text", text, "reason", "barge_in")
}

func bargeIn(ms, audio string) journal.Record {
	return record(journal.KindBargeInDetected, audio, "tts_position_ms", ms)
}

// cutTurn is the rejected turn in the order the session writes it: the
// detection lands before the speech children record the cut (SPEC §4.4).
func cutTurn() []journal.Record {
	return []journal.Record{
		heard("play something by zeppelin", "blob://mic/1"),
		record(journal.KindToolCalled, "", "tool", "media_search", "call_id", "c1", "args_json", `{"query":"zeppelin"}`),
		speak("s1"),
		record(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok", "result_json", `{"hits":3}`),
		bargeIn("420", "blob://mic/2"),
		truncated("I found three", " albums by that artist", "blob://tts/s1"),
		cancelled("s1"),
		completed(),
	}
}

// correctedTurn is the turn after the correction, as the session writes it.
func correctedTurn() []journal.Record {
	return []journal.Record{
		heard("just the first one", "blob://mic/3"),
		speak("s2"),
		spoken("Playing Led Zeppelin one.", "blob://tts/s2"),
		record(journal.KindToolResult, "", "call_id", "s2", "outcome", "ok"),
		completed(),
	}
}

func concat(parts ...[]journal.Record) []journal.Record {
	var out []journal.Record
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// conversation appends records through a real journal, so fixtures carry the
// same stamps a session's log would.
func conversation(t *testing.T, v journal.Versions, records []journal.Record) *journal.MemStore {
	t.Helper()
	store := journal.NewMemStore()
	j := journal.New(store, journal.FixedClock(time.Unix(1_760_000_000, 0)), v)
	for _, r := range records {
		if _, err := j.Append(context.Background(), "conv-1", r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
	return store
}

func scan(t *testing.T, store journal.Store) harvest.Result {
	t.Helper()
	res, err := harvest.Scan(context.Background(), store, "conv-1")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return res
}

func onePair(t *testing.T, res harvest.Result) harvest.Pair {
	t.Helper()
	if len(res.Pairs) != 1 {
		t.Fatalf("got %d pairs, want 1: %+v", len(res.Pairs), res.Pairs)
	}
	return res.Pairs[0]
}

// verifies SPEC §9.1
func TestBargeInYieldsACandidateWithTheCorrectionAndWhatFollowed(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn(), []journal.Record{closed("model_ended")},
	))
	p := onePair(t, scan(t, store))

	want := harvest.Pair{
		ID: "conv-1/7", ConversationID: "conv-1", Source: harvest.SourceBargeIn,
		Prompt:          []harvest.Message{{Role: "user", Content: "play something by zeppelin", Name: "alice"}},
		Rejected:        "I found three",
		RejectedUnheard: " albums by that artist",
		Heard:           "just the first one", HeardSpeaker: "alice",
		AsSaid:     "Playing Led Zeppelin one.",
		Versions:   versions(),
		Attributed: true,
		Calls: []journal.Call{
			{ID: "c1", Tool: "media_search", Args: `{"query":"zeppelin"}`, Outcome: "ok", Result: `{"hits":3}`},
			{ID: "s1", Tool: "speak", Args: `{"mode":"queue","streamed":true}`, Outcome: "cancelled"},
		},
		HeardAt:           time.Unix(1_760_000_000, 0).UTC(),
		BargeInPositionMS: 420,
		CutFrames:         2080,
		Seq:               harvest.Seq{Prompt: 2, BargeIn: 6, Cut: 7, Correction: 10},
		Audio: harvest.Audio{
			Rejected: []string{"blob://tts/s1"}, BargeIn: "blob://mic/2",
			Correction: "blob://mic/3", AsSaid: []string{"blob://tts/s2"},
		},
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("pair mismatch\n got: %+v\nwant: %+v", p, want)
	}
	// The harvester never decides the chosen side: what was said after the
	// correction answers the correction, not the prompt (ADR-0026).
	if p.Chosen != "" || p.Curated {
		t.Errorf("chosen = %q curated = %v; a raw candidate has no chosen side", p.Chosen, p.Curated)
	}
}

// One barge-in empties the whole queue: the utterance that was playing is
// truncated and everything behind it is discarded. That is one rejected
// turn, not one pair per utterance, and the playing one leads whichever
// child recorded first (SPEC §4.2).
//
// verifies SPEC §9.1
func TestBargeInThatEmptiesTheQueueIsOnePair(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{
			opened("kitchen"),
			heard("tell me about that album", "blob://mic/1"),
			speak("s1"), spoken("One sec.", "blob://tts/s1"),
			record(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok"),
			speak("s2"), speak("s3"),
			bargeIn("420", "blob://mic/2"),
			// The dropped queue lands first: different children record the two (internal/session).
			discarded("and it came out in 1973"), cancelled("s3"),
			truncated("I found three", " albums by that artist", "blob://tts/s2"), cancelled("s2"),
			completed(),
		},
		correctedTurn(), []journal.Record{closed("model_ended")},
	))
	p := onePair(t, scan(t, store))

	if p.Rejected != "One sec. I found three" {
		t.Errorf("rejected = %q, want everything heard in the turn", p.Rejected)
	}
	if want := " albums by that artist and it came out in 1973"; p.RejectedUnheard != want {
		t.Errorf("unheard = %q, want %q (queue order, not log order)", p.RejectedUnheard, want)
	}
	if want := []string{"blob://tts/s1", "blob://tts/s2"}; !reflect.DeepEqual(p.Audio.Rejected, want) {
		t.Errorf("rejected audio = %v, want %v", p.Audio.Rejected, want)
	}
}

// Nothing heard of the cut turn: the queue was emptied before it started
// playing. The rejected side is then wholly unheard, and the separator that
// a truncation's tail carries itself has to be supplied.
//
// verifies SPEC §9.1
func TestWhollyDiscardedTurnHasAnEmptyHeardSide(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{
			opened("kitchen"),
			heard("tell me about that album", "blob://mic/1"),
			speak("s1"), spoken("One sec.", "blob://tts/s1"),
			record(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok"),
			speak("s2"),
			bargeIn("90", "blob://mic/2"),
			discarded("I found three albums"), cancelled("s2"),
			completed(),
		},
		correctedTurn(), []journal.Record{closed("model_ended")},
	))
	p := onePair(t, scan(t, store))

	if p.Rejected != "One sec." || p.RejectedUnheard != " I found three albums" {
		t.Errorf("rejected = %q + %q; the two must concatenate to the turn as generated", p.Rejected, p.RejectedUnheard)
	}
}

// verifies SPEC §9.1
func TestCutWithNoCorrectionIsCountedNotPaired(t *testing.T) {
	cases := map[string][]journal.Record{
		"session closed": concat(
			[]journal.Record{opened("kitchen")}, cutTurn(), []journal.Record{closed("silence_timeout")},
		),
		"closed then woken again": concat(
			[]journal.Record{opened("kitchen")}, cutTurn(),
			[]journal.Record{closed("silence_timeout")},
			[]journal.Record{record(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "alice", "resumed", "true")},
			correctedTurn(), []journal.Record{closed("model_ended")},
		),
		"another barge-in first": concat(
			[]journal.Record{opened("kitchen")}, cutTurn(),
			[]journal.Record{bargeIn("900", "blob://mic/9")},
			correctedTurn(), []journal.Record{closed("model_ended")},
		),
		"log ends mid-cut": concat([]journal.Record{opened("kitchen")}, cutTurn()),
	}
	for name, records := range cases {
		t.Run(name, func(t *testing.T) {
			res := scan(t, conversation(t, versions(), records))
			if len(res.Pairs) != 0 || res.Uncorrected != 1 {
				t.Errorf("got %d pairs, %d uncorrected; want 0 pairs, 1 uncorrected", len(res.Pairs), res.Uncorrected)
			}
		})
	}
}

// A truncation with no detection in its turn is a preempt or a migration,
// not a correction, so it harvests nothing.
//
// verifies SPEC §9.1
func TestTruncationWithoutABargeInIsNotACandidate(t *testing.T) {
	store := conversation(t, versions(), []journal.Record{
		opened("kitchen"),
		heard("tell me about that album", "blob://mic/1"),
		speak("s1"),
		truncated("I found three", " albums", "blob://tts/s1"), cancelled("s1"),
		record(journal.KindSpeechDiscarded, "", "unspoken_text", "and one more", "reason", "preempted"),
		completed(),
		heard("which ones", "blob://mic/2"),
		completed(),
		closed("model_ended"),
	})
	res := scan(t, store)
	if len(res.Pairs) != 0 || res.Uncorrected != 0 {
		t.Errorf("got %d pairs, %d uncorrected; want none", len(res.Pairs), res.Uncorrected)
	}
}

// The person walked to another room between the cut and the correction. The
// session closed and a resumed one opened, but the conversation is the same
// log, so the correction still answers the cut (SPEC §4.5).
//
// verifies SPEC §9.1, §4.5
func TestMigrationBetweenCutAndCorrectionKeepsThePair(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(),
		[]journal.Record{
			closed("migrated"),
			record(journal.KindSessionOpened, "", "satellite", "office", "speaker_id", "alice", "resumed", "true"),
		},
		correctedTurn(), []journal.Record{closed("model_ended")},
	))
	res := scan(t, store)
	p := onePair(t, res)
	if res.Uncorrected != 0 {
		t.Errorf("uncorrected = %d; a migration is not a close", res.Uncorrected)
	}
	if p.Heard != "just the first one" || p.AsSaid != "Playing Led Zeppelin one." {
		t.Errorf("pair = heard %q, as said %q; the correction on the new device must pair with the cut", p.Heard, p.AsSaid)
	}
}

// verifies SPEC §9.1
func TestRejectedTurnWithoutACompletionIsUnattributed(t *testing.T) {
	cut := cutTurn()
	cut = cut[:len(cut)-1] // The engine died before TurnEnd; no completion was recorded.
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cut, correctedTurn(), []journal.Record{closed("model_ended")},
	))
	p := onePair(t, scan(t, store))

	if p.Attributed || p.Versions != (journal.Versions{}) {
		t.Errorf("attributed = %v versions = %+v; a turn with no completion cannot be attributed", p.Attributed, p.Versions)
	}
}

// Attribution is to the model, prompt and tool schema that produced the
// rejected turn. Which ear heard the correction and which voice was cut are
// provenance for review, not preconditions: a journal stamped without them
// still yields a trainable pair (SPEC §8).
//
// verifies SPEC §9.1
func TestRejectedTurnIsAttributedWithoutAnSTTOrTTSVersion(t *testing.T) {
	v := versions()
	v.STT, v.TTS = "", ""
	store := conversation(t, v, concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn(), []journal.Record{closed("model_ended")},
	))
	p := onePair(t, scan(t, store))

	if !p.Attributed || p.Versions != v {
		t.Errorf("attributed = %v versions = %+v; the ear and voice do not gate attribution", p.Attributed, p.Versions)
	}
}

// verifies SPEC §9.1
func TestAnsweringTurnThatIsCutTooIsFlagged(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(),
		[]journal.Record{
			heard("just the first one", "blob://mic/3"),
			speak("s2"),
			bargeIn("300", "blob://mic/4"),
			truncated("Playing Led", " Zeppelin one.", "blob://tts/s2"), cancelled("s2"),
			completed(),
			heard("no, the second", "blob://mic/5"),
			speak("s3"), spoken("Playing Led Zeppelin two.", "blob://tts/s3"),
			record(journal.KindToolResult, "", "call_id", "s3", "outcome", "ok"),
			completed(),
			closed("model_ended"),
		},
	))
	res := scan(t, store)
	if len(res.Pairs) != 2 {
		t.Fatalf("got %d pairs, want 2: each cut pairs with the correction after it", len(res.Pairs))
	}
	first, second := res.Pairs[0], res.Pairs[1]
	if first.AsSaid != "Playing Led" || !first.AsSaidCut {
		t.Errorf("first = as said %q cut %v; want the heard part, flagged", first.AsSaid, first.AsSaidCut)
	}
	if second.Rejected != "Playing Led" || second.Heard != "no, the second" || second.AsSaidCut {
		t.Errorf("second = %+v; want the cut answer as its rejected side", second)
	}
}

// The prompt carries the turns before the rejected one, bounded, and only
// what the user heard of them: unheard text never reaches a prompt the model
// is trained on (SPEC §4.4).
//
// verifies SPEC §9.1, §4.4
func TestPromptIsBoundedAndCarriesOnlyHeardText(t *testing.T) {
	var prior []journal.Record
	for i := range harvest.ContextTurns + 2 {
		n := string(rune('a' + i))
		prior = append(prior,
			heard("question "+n, "blob://mic/q"+n),
			speak("s"+n),
			truncated("answer "+n, " unheard "+n, "blob://tts/s"+n), cancelled("s"+n),
			completed(),
		)
	}
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, prior, cutTurn(), correctedTurn(), []journal.Record{closed("model_ended")},
	))
	p := onePair(t, scan(t, store))

	want := []harvest.Message{
		{Role: "user", Content: "question c", Name: "alice"},
		{Role: "assistant", Content: "answer c"},
		{Role: "user", Content: "question d", Name: "alice"},
		{Role: "assistant", Content: "answer d"},
		{Role: "user", Content: "question e", Name: "alice"},
		{Role: "assistant", Content: "answer e"},
		{Role: "user", Content: "question f", Name: "alice"},
		{Role: "assistant", Content: "answer f"},
		{Role: "user", Content: "play something by zeppelin", Name: "alice"},
	}
	if !reflect.DeepEqual(p.Prompt, want) {
		t.Errorf("prompt mismatch\n got: %+v\nwant: %+v", p.Prompt, want)
	}
	for _, m := range p.Prompt {
		if strings.Contains(m.Content, "unheard") {
			t.Errorf("prompt carries unheard text: %q", m.Content)
		}
	}
}

// verifies SPEC §9.1
func TestHarvestIsDeterministic(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn(), []journal.Record{closed("model_ended")},
	))
	first := scan(t, store)
	second := scan(t, store)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("harvest is not deterministic\n%+v\n%+v", first, second)
	}
}

// verifies SPEC §9.1
func TestHarvestRejectsAnEmptyConversation(t *testing.T) {
	_, err := harvest.Harvest(context.Background(), journal.NewMemStore(), "nope")
	if err == nil {
		t.Fatal("harvesting an unknown conversation succeeded")
	}
}

// A kind the walker does not fold would be dropped silently, and a dropped
// kind is how a candidate quietly loses its context.
//
// verifies SPEC §8
func TestScanFoldsEveryGeneratedKind(t *testing.T) {
	store := journal.NewMemStore()
	// Straight into the store: only the kind matters, not each kind's fields.
	for i, k := range journal.AllKinds {
		e := journal.Event{
			Seq: uint64(i + 1), ConversationID: "conv-1", Kind: k,
			Fields: map[string]string{"tts_position_ms": "1", "reason": "barge_in", "memories_json": "[]"},
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("append %s: %v", k, err)
		}
	}
	if _, err := harvest.Scan(context.Background(), store, "conv-1"); err != nil {
		t.Errorf("scan: %v", err)
	}
}

// verifies SPEC §9.1
func TestScanWrapsStoreErrors(t *testing.T) {
	boom := errors.New("boom")
	_, err := harvest.Scan(context.Background(), failingStore{boom}, "conv-1")
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "conv-1") {
		t.Errorf("err = %v; want the store error wrapped with the conversation", err)
	}
}

type failingStore struct{ err error }

func (f failingStore) Append(context.Context, journal.Event) error { return f.err }
func (f failingStore) Events(context.Context, string) ([]journal.Event, error) {
	return nil, f.err
}
func (f failingStore) LastSeq(context.Context, string) (uint64, error) { return 0, f.err }

// The detection snapshot lags the DAC by the stop's flight time, so the
// truncation's frames_played is the confirmed cut, and it is relative to the
// cut clip's own audio. A cut that only discarded queued clips played none
// of them, so it carries no frames.
//
// verifies SPEC §9.2
func TestPairCarriesTheDACConfirmedCutFrames(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn(),
	))
	if p := onePair(t, scan(t, store)); p.CutFrames != 2080 {
		t.Errorf("CutFrames = %d, want the truncation's frames_played", p.CutFrames)
	}

	discardOnly := []journal.Record{
		opened("kitchen"),
		heard("play something by zeppelin", "blob://mic/1"),
		speak("s1"),
		bargeIn("0", "blob://mic/2"),
		discarded("I found three albums by that artist"),
		cancelled("s1"),
		completed(),
		heard("just the first one", "blob://mic/3"),
		spoken("Playing Led Zeppelin one.", "blob://tts/s2"),
		completed(),
	}
	store = conversation(t, versions(), discardOnly)
	if p := onePair(t, scan(t, store)); p.CutFrames != 0 {
		t.Errorf("CutFrames = %d, want 0 when nothing of the cut clip played", p.CutFrames)
	}
}

// A live satellite journals when each turn's answer starts playing. The
// start says when, not what, so the pair is the one the log without starts
// yields: same sides, same audio, same cut.
//
// verifies SPEC §9.1, §11
func TestTheFirstPlayedFrameDoesNotChangeThePair(t *testing.T) {
	started := func(id, ms string) journal.Record {
		return record(journal.KindSpeechStarted, "", "call_id", id, "wait_ms", ms)
	}
	plain := onePair(t, scan(t, conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn(),
	))))

	cut, corrected := cutTurn(), correctedTurn()
	live := concat(
		[]journal.Record{opened("kitchen")},
		cut[:3], []journal.Record{started("s1", "1460")}, cut[3:],
		corrected[:2], []journal.Record{started("s2", "880")}, corrected[2:],
	)
	got := onePair(t, scan(t, conversation(t, versions(), live)))

	if got.Rejected != plain.Rejected || got.RejectedUnheard != plain.RejectedUnheard ||
		got.Heard != plain.Heard || got.AsSaid != plain.AsSaid || got.CutFrames != plain.CutFrames ||
		!reflect.DeepEqual(got.Audio, plain.Audio) || !reflect.DeepEqual(got.Prompt, plain.Prompt) {
		t.Errorf("starts changed the pair\n got: %+v\nwant: %+v", got, plain)
	}
}

// The oven goes off in the kitchen while Alice waits on a search, and is said
// ahead of the answer she then cuts off. The oven's words were nobody's
// choice, so the rejected side is the answer alone, and the oven's speak
// call is not among the turn's calls (ADR-0045).
//
// verifies SPEC §9.1
func TestAnAnnouncementInTheMiddleOfACutTurnIsNoPartOfThePair(t *testing.T) {
	oven := []journal.Record{
		record(journal.KindAnnouncementMade, "", "text", "The oven timer is done.", "call_id", "an_0a7e", "source", "timer", "timer_id", "t_0a7e11c3"),
		record(journal.KindToolCalled, "", "tool", "speak", "call_id", "an_0a7e", "args_json", `{"text":"The oven timer is done.","mode":"queue"}`),
		record(journal.KindSpeechSpoken, "blob://tts/an_0a7e", "text", "The oven timer is done.", "frames_played", "24000", "call_id", "an_0a7e"),
		record(journal.KindToolResult, "", "call_id", "an_0a7e", "outcome", "ok"),
	}
	cut := cutTurn()
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cut[:2], oven, cut[2:], correctedTurn(), []journal.Record{closed("model_ended")},
	))
	p := onePair(t, scan(t, store))
	if p.Rejected != "I found three" {
		t.Errorf("rejected = %q, want the answer without the oven", p.Rejected)
	}
	for _, c := range p.Calls {
		if c.ID == "an_0a7e" {
			t.Errorf("the oven's speak call is in the pair: %+v", p.Calls)
		}
	}
	if len(p.Audio.Rejected) != 1 || p.Audio.Rejected[0] != "blob://tts/s1" {
		t.Errorf("rejected audio = %v", p.Audio.Rejected)
	}
}
