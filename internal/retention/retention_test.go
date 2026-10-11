package retention_test

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/retention"
)

// today is the Saturday the pruner runs; the household's conversations are
// dated back from it.
var today = time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

// clock is a virtual clock whose waits fire only when the test advances it.
type clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []wait
	// asked hears each wait begin: the pruner waits once a pass is done.
	asked chan struct{}
}

type wait struct {
	at time.Time
	ch chan time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, wait{c.now.Add(d), ch})
	select {
	case c.asked <- struct{}{}:
	default:
	}
	return ch
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	var live []wait
	for _, w := range c.timers {
		if w.at.After(c.now) {
			live = append(live, w)
			continue
		}
		w.ch <- w.at
	}
	c.timers = live
}

// house is one household's journal, audio and reviewer's verdicts.
type house struct {
	t       *testing.T
	store   *journal.MemStore
	blobs   *blob.Memory
	verdict *curation.MemStore
	clock   *clock
	guard   *guard
}

func newHouse(t *testing.T) *house {
	return &house{
		t: t, store: journal.NewMemStore(), blobs: blob.NewMemory(), verdict: curation.NewMemStore(),
		clock: &clock{now: today, asked: make(chan struct{}, 8)}, guard: &guard{declined: map[string]time.Time{}},
	}
}

// at writes records into a log as of t, keeping a blob for every ref.
func (h *house) at(t time.Time, conv string, records ...journal.Record) {
	h.t.Helper()
	j := journal.New(h.store, journal.FixedClock(t), versions)
	for _, r := range records {
		for _, ref := range []string{r.AudioRef, r.Fields["second_audio_ref"]} {
			if ref == "" || h.guard.declined[ref] != (time.Time{}) {
				continue
			}
			key, err := blob.KeyFor(ref)
			if err != nil {
				h.t.Fatal(err)
			}
			w, err := h.blobs.Create(context.Background(), key)
			if err != nil {
				h.t.Fatal(err)
			}
			if _, err := w.Write([]byte("pcm")); err != nil {
				h.t.Fatal(err)
			}
			if _, err := w.Commit(); err != nil {
				h.t.Fatal(err)
			}
		}
		if _, err := j.Append(context.Background(), conv, r); err != nil {
			h.t.Fatalf("append %s to %s: %v", r.Kind, conv, err)
		}
	}
}

func (h *house) pruner(p retention.Policy) *retention.Pruner {
	h.t.Helper()
	pr, err := retention.New(retention.Config{
		Journal: journal.New(h.store, h.clock, versions), Store: h.store, Blobs: h.blobs,
		Curation: h.verdict, NotKept: h.guard, Clock: h.clock, Timers: h.clock, Policy: p,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return pr
}

func (h *house) pass(p *retention.Pruner) retention.Report {
	h.t.Helper()
	rep, err := p.Pass(context.Background())
	if err != nil {
		h.t.Fatalf("pass: %v", err)
	}
	return rep
}

func (h *house) kept(ref string) bool {
	_, err := h.blobs.Open(context.Background(), ref)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		h.t.Fatal(err)
	}
	return err == nil
}

func (h *house) log(conv string) []journal.Event {
	h.t.Helper()
	events, err := h.store.Events(context.Background(), conv)
	if err != nil {
		h.t.Fatal(err)
	}
	return events
}

// droppedIn is each ref the log says is not kept, and why.
func (h *house) droppedIn(conv string) map[string]string {
	out := map[string]string{}
	for _, e := range h.log(conv) {
		if e.Kind == journal.KindAudioDropped {
			out[e.Fields["audio_ref"]] = e.Fields["reason"] + "/" + e.Fields["days"]
		}
	}
	return out
}

// guard stands in for the disk guard's record of what it did not write.
type guard struct {
	mu       sync.Mutex
	declined map[string]time.Time
}

func (g *guard) Declined() map[string]time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return maps.Clone(g.declined)
}

func (g *guard) Settle(ref string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.declined, ref)
}

var versions = journal.Versions{Model: "qwen3:14b", Prompt: "sys@3", ToolSchema: "tools@7"}

func rec(kind journal.Kind, audio string, fields ...string) journal.Record {
	r := journal.Record{Kind: kind, AudioRef: audio, Fields: map[string]string{}}
	for i := 0; i+1 < len(fields); i += 2 {
		r.Fields[fields[i]] = fields[i+1]
	}
	return r
}

func opened(sat, person string) journal.Record {
	return rec(journal.KindSessionOpened, "", "satellite", sat, "speaker_id", person)
}

func closed(sat string) journal.Record {
	return rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", sat)
}

// weather is Teagan asking the kitchen about the weather, its clips named
// for the day.
func weather(sat, tag string) []journal.Record {
	return []journal.Record{
		opened(sat, "teagan"),
		rec(journal.KindUtteranceTranscribed, "blob://mic/"+tag+"-ask", "text", "what's the weather today", "speaker_id", "teagan", "second_audio_ref", "blob://mic/"+tag+"-ask.ch1"),
		rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop"),
		rec(journal.KindSpeechSpoken, "blob://tts/"+tag+"-forecast", "text", "Cloudy, rain from three.", "frames_played", "24000"),
		closed(sat),
	}
}

// bargeIn is Alice cutting the kitchen off mid-list and asking for the
// first album instead: the pair a reviewer accepts.
func bargeIn(tag string) []journal.Record {
	return []journal.Record{
		opened("kitchen", "alice"),
		rec(journal.KindUtteranceTranscribed, "blob://mic/"+tag+"-ask", "text", "play something by zeppelin", "speaker_id", "alice"),
		rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop"),
		rec(journal.KindBargeInDetected, "blob://mic/"+tag+"-first", "tts_position_ms", "800"),
		rec(journal.KindSpeechTruncated, "blob://tts/"+tag+"-list", "spoken_text", "I found three", "unspoken_text", " albums.", "frames_played", "12800", "reason", "barge_in"),
		rec(journal.KindUtteranceTranscribed, "blob://mic/"+tag+"-first", "text", "just the first one", "speaker_id", "alice"),
		rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop"),
		closed("kitchen"),
	}
}

func kitchenKeeps(audio, journalH time.Duration) retention.Policy {
	return retention.Policy{Satellites: map[string]retention.Horizons{"kitchen": {Audio: audio, Journal: journalH}}}
}

// The kitchen keeps 30 days of audio. Teagan's weather from 40 days ago
// loses its clips, both mic channels and the voice, each recorded as
// pruned in its own log; last week's keeps them. The office keeps
// everything, so its 40-day-old clips stay. A second pass changes nothing.
//
// verifies SPEC §8
func TestTheKitchenKeepsThirtyDaysOfAudio(t *testing.T) {
	h := newHouse(t)
	h.at(today.Add(-40*day), "conv-kitchen-old", weather("kitchen", "k40")...)
	h.at(today.Add(-7*day), "conv-kitchen-week", weather("kitchen", "k7")...)
	h.at(today.Add(-40*day), "conv-office-old", weather("office", "o40")...)
	p := h.pruner(kitchenKeeps(30*day, 0))

	if rep := h.pass(p); rep.AudioPruned != 3 || rep.LogsDeleted != 0 {
		t.Errorf("report = %+v, want 3 clips pruned and no log deleted", rep)
	}
	for _, ref := range []string{"blob://mic/k40-ask", "blob://mic/k40-ask.ch1", "blob://tts/k40-forecast"} {
		if h.kept(ref) {
			t.Errorf("%s kept past the kitchen's 30 days", ref)
		}
	}
	want := map[string]string{
		"blob://mic/k40-ask": "retention/30", "blob://mic/k40-ask.ch1": "retention/30", "blob://tts/k40-forecast": "retention/30",
	}
	if got := h.droppedIn("conv-kitchen-old"); !maps.Equal(got, want) {
		t.Errorf("recorded = %v, want %v", got, want)
	}
	for _, ref := range []string{"blob://mic/k7-ask", "blob://tts/k7-forecast", "blob://mic/o40-ask", "blob://tts/o40-forecast"} {
		if !h.kept(ref) {
			t.Errorf("%s pruned inside its horizon", ref)
		}
	}
	if got := h.droppedIn("conv-office-old"); len(got) != 0 {
		t.Errorf("the office recorded %v, keeping everything", got)
	}
	if _, err := journal.Replay(context.Background(), h.store, "conv-kitchen-old", journal.Overrides{}); err != nil {
		t.Errorf("the pruned log does not replay: %v", err)
	}

	if rep := h.pass(p); rep != (retention.Report{}) {
		t.Errorf("second pass = %+v, want nothing", rep)
	}
	if n := len(h.droppedIn("conv-kitchen-old")); n != 3 {
		t.Errorf("recorded %d drops after two passes, want 3", n)
	}
}

// Alice's barge-in from 40 days ago was accepted into the dataset, so its
// clips outlive the kitchen's 30 days. A discarded one does not. With
// prune_curated on, the accepted one goes too.
//
// verifies SPEC §8, §9.1
func TestACuratedBargeInKeepsItsClip(t *testing.T) {
	h := newHouse(t)
	ctx := context.Background()
	h.at(today.Add(-40*day), "conv-kitchen-accepted", bargeIn("acc")...)
	h.at(today.Add(-40*day), "conv-kitchen-discarded", bargeIn("dis")...)
	for conv, status := range map[string]curation.Status{"conv-kitchen-accepted": curation.StatusAccepted, "conv-kitchen-discarded": curation.StatusDiscarded} {
		if err := h.verdict.Put(ctx, curation.Decision{PairID: conv + "/5", ConversationID: conv, Status: status, Chosen: "Playing Led Zeppelin one.", DecidedAt: today.Add(-39 * day)}); err != nil {
			t.Fatal(err)
		}
	}
	h.pass(h.pruner(kitchenKeeps(30*day, 0)))

	for _, ref := range []string{"blob://mic/acc-ask", "blob://mic/acc-first", "blob://tts/acc-list"} {
		if !h.kept(ref) {
			t.Errorf("curated clip %s was pruned", ref)
		}
	}
	if got := h.droppedIn("conv-kitchen-accepted"); len(got) != 0 {
		t.Errorf("curated log recorded %v", got)
	}
	if h.kept("blob://tts/dis-list") {
		t.Error("the discarded barge-in kept its clip")
	}

	all := kitchenKeeps(30*day, 0)
	all.PruneCurated = true
	h.pass(h.pruner(all))
	if h.kept("blob://tts/acc-list") {
		t.Error("prune_curated kept the curated clip")
	}
}

// Teagan has kept one conversation going with the kitchen for weeks, and
// is talking to it now. Its oldest clips are past the horizon, but its
// session is live: nothing in its log is touched (ADR-0022).
//
// verifies SPEC §4.5, §8
func TestALiveSessionIsNeverPruned(t *testing.T) {
	h := newHouse(t)
	h.at(today.Add(-40*day), "conv-kitchen-live", weather("kitchen", "live")[:4]...)
	h.at(today.Add(-time.Minute), "conv-kitchen-live",
		rec(journal.KindUtteranceTranscribed, "blob://mic/live-now", "text", "and tomorrow?", "speaker_id", "teagan"))
	h.guard.declined["blob://mic/live-now"] = today
	before := h.log("conv-kitchen-live")

	rep := h.pass(h.pruner(kitchenKeeps(30*day, 31*day)))
	if rep != (retention.Report{}) {
		t.Errorf("report = %+v, want nothing for a live session", rep)
	}
	if after := h.log("conv-kitchen-live"); len(after) != len(before) {
		t.Errorf("the live log went from %d to %d events", len(before), len(after))
	}
	if !h.kept("blob://mic/live-ask") {
		t.Error("the live session's clip was pruned")
	}
}

// A log the daemon left open when it crashed a month ago is no live
// session: sessions do not survive a restart (SPEC §14).
//
// verifies SPEC §8
func TestALogLeftOpenByACrashIsNotLive(t *testing.T) {
	h := newHouse(t)
	h.at(today.Add(-40*day), "conv-kitchen-crashed", weather("kitchen", "crash")[:4]...)
	if rep := h.pass(h.pruner(kitchenKeeps(30*day, 0))); rep.AudioPruned != 3 {
		t.Errorf("report = %+v, want its 3 clips pruned", rep)
	}
}

// The kitchen keeps a year of conversations. One that ended 400 days ago
// is deleted whole, with its audio and the reviewer's discard of it; one
// that ended 300 days ago stays, though it began 400 days ago; an accepted
// one from 400 days ago stays. Audio pruned long after a conversation
// ended does not put off its deletion.
//
// verifies SPEC §8, §9.2
func TestTheJournalHorizonDeletesWholeEndedConversations(t *testing.T) {
	h := newHouse(t)
	ctx := context.Background()
	h.at(today.Add(-400*day), "conv-kitchen-gone", weather("kitchen", "gone")...)
	h.at(today.Add(-400*day+30*day), "conv-kitchen-gone", rec(journal.KindAudioDropped, "", "audio_ref", "blob://mic/gone-ask", "reason", "retention", "days", "30"))
	h.at(today.Add(-400*day), "conv-kitchen-long", weather("kitchen", "long")[:4]...)
	h.at(today.Add(-300*day), "conv-kitchen-long", closed("kitchen"))
	h.at(today.Add(-400*day), "conv-kitchen-kept", bargeIn("kept")...)
	put := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	put(h.verdict.Put(ctx, curation.Decision{PairID: "conv-kitchen-gone/3", ConversationID: "conv-kitchen-gone", Status: curation.StatusDiscarded, Reason: "noise", DecidedAt: today.Add(-399 * day)}))
	put(h.verdict.Put(ctx, curation.Decision{PairID: "conv-kitchen-kept/5", ConversationID: "conv-kitchen-kept", Status: curation.StatusAccepted, Chosen: "Playing Led Zeppelin one.", DecidedAt: today.Add(-399 * day)}))

	rep := h.pass(h.pruner(kitchenKeeps(0, 365*day)))
	if rep.LogsDeleted != 1 {
		t.Errorf("report = %+v, want one log deleted", rep)
	}
	if n := len(h.log("conv-kitchen-gone")); n != 0 {
		t.Errorf("the gone conversation keeps %d events", n)
	}
	if h.kept("blob://tts/gone-forecast") || h.kept("blob://mic/gone-ask.ch1") {
		t.Error("the gone conversation's audio was kept")
	}
	if ds, _ := h.verdict.ForConversation(ctx, "conv-kitchen-gone"); len(ds) != 0 {
		t.Errorf("the gone conversation keeps verdicts %v", ds)
	}
	ids, _ := h.store.Conversations(ctx)
	if want := []string{"conv-kitchen-long", "conv-kitchen-kept"}; !slices.Equal(ids, want) {
		t.Errorf("logs = %v, want %v", ids, want)
	}
	if !h.kept("blob://tts/kept-list") {
		t.Error("the curated conversation lost its audio")
	}
}

// A conversation across the kitchen and the office is kept as long as the
// longer of their horizons, and forever when either keeps forever.
//
// verifies SPEC §4.5, §8
func TestAMigratedConversationIsKeptByItsLongestHorizon(t *testing.T) {
	h := newHouse(t)
	walk := append(weather("kitchen", "walk")[:4],
		rec(journal.KindSessionClosed, "", "reason", "migrated", "satellite", "kitchen"),
		opened("office", "teagan"), closed("office"))
	h.at(today.Add(-100*day), "conv-walk", walk...)

	short := retention.Policy{Satellites: map[string]retention.Horizons{"kitchen": {Journal: 30 * day}, "office": {Journal: 90 * day}}}
	if rep := h.pass(h.pruner(short)); rep.LogsDeleted != 1 {
		t.Errorf("past both horizons: report = %+v, want it deleted", rep)
	}

	h.at(today.Add(-100*day), "conv-walk-2", walk...)
	forever := retention.Policy{Satellites: map[string]retention.Horizons{"kitchen": {Journal: 30 * day}}}
	if rep := h.pass(h.pruner(forever)); rep.LogsDeleted != 0 {
		t.Errorf("the office keeps forever: report = %+v, want it kept", rep)
	}
}

// The kitchen's own log past its year: old presence and wakes go, with the
// dishwasher's clips and the reviewer's discard; the guest's wake a
// reviewer confirmed as a hard negative stays with its clip, and so does
// the log's last event.
//
// verifies SPEC §8, §9.3
func TestTheDeviceLogIsTrimmedButKeepsConfirmedWakes(t *testing.T) {
	h := newHouse(t)
	ctx := context.Background()
	old := today.Add(-400 * day)
	h.at(old, "device:kitchen",
		rec(journal.KindPresenceChanged, "", "state", "present", "sensor", "room_presence"),
		rec(journal.KindWakeRejected, "blob://wake/dishwasher", "reason", "no_speech", "second_audio_ref", "blob://wake/dishwasher.ch1"),
		rec(journal.KindWakeRejected, "blob://wake/cough", "reason", "no_speech"),
	)
	h.at(today.Add(-2*day), "device:kitchen", rec(journal.KindPresenceChanged, "", "state", "absent", "sensor", "room_presence"))
	for seq, status := range map[uint64]curation.WakeStatus{2: curation.WakeDiscarded, 3: curation.WakeConfirmed} {
		if err := h.verdict.PutWakeVerdict(ctx, curation.WakeVerdict{ConversationID: "device:kitchen", Seq: seq, Status: status, JudgedAt: old}); err != nil {
			t.Fatal(err)
		}
	}

	rep := h.pass(h.pruner(kitchenKeeps(0, 365*day)))
	if rep.EventsDeleted != 2 {
		t.Errorf("report = %+v, want the presence and the dishwasher deleted", rep)
	}
	var seqs []uint64
	for _, e := range h.log("device:kitchen") {
		seqs = append(seqs, e.Seq)
	}
	if !slices.Equal(seqs, []uint64{3, 4}) {
		t.Errorf("device log seqs = %v, want the confirmed cough and the last presence", seqs)
	}
	if h.kept("blob://wake/dishwasher") || h.kept("blob://wake/dishwasher.ch1") {
		t.Error("the trimmed wake kept its clips")
	}
	if !h.kept("blob://wake/cough") {
		t.Error("the confirmed hard negative lost its clip")
	}
	vs, _ := h.verdict.WakeVerdicts(ctx, "device:kitchen")
	if _, ok := vs[2]; ok || len(vs) != 1 {
		t.Errorf("verdicts = %v, want only the confirmed one", vs)
	}
}

// The house keeps a year. The oven timer that went off 400 days ago is
// deleted with its start; the one from last week stays, and the house log
// still replays.
//
// verifies SPEC §8
func TestTheHouseLogDropsEndedTimersPastTheHorizon(t *testing.T) {
	h := newHouse(t)
	started := func(id string, at time.Time) journal.Record {
		return rec(journal.KindTimerStarted, "", "timer_id", id, "seconds", "720", "fires_at", at.Add(12*time.Minute).Format(time.RFC3339Nano),
			"satellite", "kitchen", "conversation_id", "conv-1", "call_id", "c1", "label", "oven")
	}
	finished := func(id string) journal.Record {
		return rec(journal.KindTimerFinished, "", "timer_id", id, "outcome", "announced", "conversation_id", "conv-2")
	}
	old, week := today.Add(-400*day), today.Add(-7*day)
	h.at(old, journal.HouseTimers, started("t_old", old))
	h.at(old.Add(12*time.Minute), journal.HouseTimers, finished("t_old"))
	h.at(week, journal.HouseTimers, started("t_week", week))
	h.at(week.Add(12*time.Minute), journal.HouseTimers, finished("t_week"))

	rep := h.pass(h.pruner(retention.Policy{House: retention.Horizons{Journal: 365 * day}}))
	if rep.EventsDeleted != 2 {
		t.Errorf("report = %+v, want the old timer's two events", rep)
	}
	st, err := journal.Replay(context.Background(), h.store, journal.HouseTimers, journal.Overrides{})
	if err != nil {
		t.Fatalf("the trimmed house log does not replay: %v", err)
	}
	if len(st.Timers) != 1 || st.Timers[0].ID != "t_week" {
		t.Errorf("timers = %+v, want last week's", st.Timers)
	}
}

// The disk was full when the kitchen heard Teagan, so the guard declined
// the clip. Once her conversation has ended the log says the audio was not
// kept, and the guard lets the ref go.
//
// verifies SPEC §8
func TestADeclinedClipIsJournalledOnceItsConversationEnds(t *testing.T) {
	h := newHouse(t)
	h.guard.declined["blob://mic/full-ask"] = today
	h.guard.declined["blob://mic/full-ask.ch1"] = today
	h.at(today.Add(-time.Hour), "conv-kitchen-full", weather("kitchen", "full")...)

	if rep := h.pass(h.pruner(retention.Policy{})); rep.AudioNotKept != 2 {
		t.Errorf("report = %+v, want both declined channels journalled", rep)
	}
	want := map[string]string{"blob://mic/full-ask": "disk_low/", "blob://mic/full-ask.ch1": "disk_low/"}
	if got := h.droppedIn("conv-kitchen-full"); !maps.Equal(got, want) {
		t.Errorf("recorded = %v, want %v", got, want)
	}
	if got := h.guard.Declined(); len(got) != 0 {
		t.Errorf("still declined: %v", got)
	}
}

// A household that set no retention keeps everything, and a pass reads
// nothing at all.
//
// verifies SPEC §8
func TestNoRetentionPrunesNothing(t *testing.T) {
	h := newHouse(t)
	h.at(today.Add(-4000*day), "conv-kitchen-ancient", weather("kitchen", "ancient")...)
	if rep := h.pass(h.pruner(retention.Policy{})); rep != (retention.Report{}) {
		t.Errorf("report = %+v, want nothing", rep)
	}
	if !h.kept("blob://mic/ancient-ask") || len(h.log("conv-kitchen-ancient")) == 0 {
		t.Error("something was pruned with no retention set")
	}
}

// chorusd prunes once at startup, then every hour: Teagan's weather comes
// due overnight and is gone by the next pass after.
//
// verifies SPEC §8
func TestRunPrunesAtStartAndEveryHour(t *testing.T) {
	h := newHouse(t)
	h.at(today.Add(-40*day), "conv-kitchen-old", weather("kitchen", "old")...)
	h.at(today.Add(-30*day+30*time.Minute), "conv-kitchen-due", weather("kitchen", "due")...)
	p := h.pruner(kitchenKeeps(30*day, 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	<-h.clock.asked
	if h.kept("blob://mic/old-ask") {
		t.Error("the startup pass kept a clip past the horizon")
	}
	if !h.kept("blob://mic/due-ask") {
		t.Fatal("the startup pass pruned a clip half an hour early")
	}
	h.clock.set(today.Add(time.Hour))
	<-h.clock.asked
	if h.kept("blob://mic/due-ask") {
		t.Error("the hourly pass kept a clip that came due")
	}
}
