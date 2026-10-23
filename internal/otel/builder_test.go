package otel_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/otel"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// thursday is the household's day as chorusd journalled it, by log.
func thursday(t *testing.T) map[string][]journal.Event {
	t.Helper()
	ctx := context.Background()
	store, err := household.Journal(ctx)
	if err != nil {
		t.Fatalf("household journal: %v", err)
	}
	logs := map[string][]journal.Event{}
	for id := range household.Logs() {
		events, err := store.Events(ctx, id)
		if err != nil {
			t.Fatalf("events %s: %v", id, err)
		}
		logs[id] = events
	}
	return logs
}

// tree draws spans as their parents hold them, each child under its parent
// in order of start, with times as the household's clock read them.
func tree(spans []otel.Span) string {
	byParent := map[otel.SpanID][]otel.Span{}
	ids := map[otel.SpanID]bool{}
	for _, s := range spans {
		ids[s.ID] = true
	}
	for _, s := range spans {
		parent := s.Parent
		if !ids[parent] {
			parent = otel.SpanID{}
		}
		byParent[parent] = append(byParent[parent], s)
	}
	var b strings.Builder
	var draw func(parent otel.SpanID, depth int)
	draw = func(parent otel.SpanID, depth int) {
		kids := byParent[parent]
		slices.SortStableFunc(kids, func(x, y otel.Span) int {
			if c := x.Start.Compare(y.Start); c != 0 {
				return c
			}
			return strings.Compare(x.Name, y.Name)
		})
		for _, s := range kids {
			fmt.Fprintf(&b, "%s%s %s-%s", strings.Repeat("  ", depth), s.Name, clock(s.Start), clock(s.End))
			if s.Status.Failed {
				fmt.Fprintf(&b, " ERROR(%s)", s.Status.Message)
			}
			if len(s.Events) > 0 {
				var names []string
				for _, e := range s.Events {
					names = append(names, e.Name)
				}
				fmt.Fprintf(&b, " [%s]", strings.Join(names, " "))
			}
			b.WriteByte('\n')
			draw(s.ID, depth+1)
		}
	}
	draw(otel.SpanID{}, 0)
	return b.String()
}

// clock reads to the millisecond the fixture's float seconds were meant at.
func clock(t time.Time) string { return t.Round(time.Millisecond).Format("15:04:05.000") }

func named(t *testing.T, spans []otel.Span, name string, n int) otel.Span {
	t.Helper()
	var hits []otel.Span
	for _, s := range spans {
		if s.Name == name {
			hits = append(hits, s)
		}
	}
	if len(hits) <= n {
		t.Fatalf("no %s #%d among %d spans:\n%s", name, n, len(spans), tree(spans))
	}
	return hits[n]
}

func attr(t *testing.T, s otel.Span, key string) any {
	t.Helper()
	v, ok := s.Attr(key)
	if !ok {
		t.Fatalf("%s has no %s: %v", s.Name, key, s.Attrs)
	}
	return v
}

func diff(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n%s\nwant:\n%s", what, got, want)
	}
}

// Teagan cuts the forecast off to ask about an umbrella. The conversation is
// one trace: the kitchen's session, a turn per utterance from when she
// stopped speaking, the model's ask under it, and each call the ask made.
// The cut answer carries the barge-in and the truncation where it landed.
//
// verifies SPEC §8, §4.4, §11
func TestTheWeatherBargeInIsOneTrace(t *testing.T) {
	spans := otel.Spans(thursday(t)[household.ConvWeather], otel.Options{})

	diff(t, "the weather conversation", tree(spans), `chorus.session 07:05:00.000-07:05:12.000
  chorus.turn 07:05:00.150-07:05:02.450
    chat qwen3-32b@1 07:05:00.400-07:05:02.450
      execute_tool ha_get_state 07:05:00.500-07:05:00.600
      execute_tool speak 07:05:00.650-07:05:02.350 [speech_started barge_in_detected speech_truncated]
  chorus.turn 07:05:03.350-07:05:05.550
    chat qwen3-32b@1 07:05:03.600-07:05:05.550
      execute_tool speak 07:05:03.750-07:05:05.450 [speech_started speech_spoken]
`)

	trace := spans[0].Trace
	for _, s := range spans {
		if s.Trace != trace {
			t.Errorf("%s is in trace %s, want the conversation's %s", s.Name, s.Trace, trace)
		}
		if attr(t, s, "chorus.conversation_id") != household.ConvWeather {
			t.Errorf("%s names conversation %v", s.Name, attr(t, s, "chorus.conversation_id"))
		}
	}

	session := named(t, spans, "chorus.session", 0)
	for k, want := range map[string]any{
		"chorus.satellite": "kitchen", "chorus.speaker_id": "teagan",
		"chorus.resumed": false, "chorus.close_reason": "model_ended",
	} {
		if got := attr(t, session, k); got != want {
			t.Errorf("session %s = %v, want %v", k, got, want)
		}
	}

	turn := named(t, spans, "chorus.turn", 0)
	if attr(t, turn, "user.id") != "teagan" || attr(t, turn, "chorus.first_audio.wait_ms") != int64(600) {
		t.Errorf("first turn = %v, want Teagan's, heard after 600 ms", turn.Attrs)
	}

	ask := named(t, spans, "chat qwen3-32b@1", 0)
	if attr(t, ask, "gen_ai.request.model") != "qwen3-32b@1" || attr(t, ask, "chorus.prompt_version") != "sys@3" {
		t.Errorf("the ask = %v, want the model and prompt in effect", ask.Attrs)
	}
	if got := attr(t, ask, "gen_ai.response.finish_reasons"); !reflect.DeepEqual(got, []string{"stop"}) {
		t.Errorf("finish reasons %v, want [stop]", got)
	}
	if ask.Kind != otel.KindClient {
		t.Errorf("the ask is kind %d, want a client call", ask.Kind)
	}

	cut := named(t, spans, "execute_tool speak", 0)
	if attr(t, cut, "chorus.speech.cut_reason") != "barge_in" || attr(t, cut, "chorus.tool.outcome") != "cancelled" {
		t.Errorf("the cut answer = %v, want cut by a barge-in and cancelled", cut.Attrs)
	}
	if cut.Status.Failed {
		t.Errorf("a barge-in failed the speech: %+v", cut.Status)
	}
	barge := cut.Events[1]
	if !barge.At.Equal(household.Day().Add(7*time.Hour+5*time.Minute+2250*time.Millisecond)) || barge.Attrs[0] != otel.Int("chorus.tts_position_ms", 1500) {
		t.Errorf("barge-in = %+v, want 1500 ms into the forecast at 07:05:02.250", barge)
	}
}

// Alice's album follows her from the kitchen to the living room: one trace,
// two sessions, the second resumed (SPEC §4.5). The slow search's spoken
// acknowledgement is a call of the same ask (ADR-0039).
//
// verifies SPEC §4.5, §8
func TestAConversationThatMovesRoomsIsOneTraceOfTwoSessions(t *testing.T) {
	spans := otel.Spans(thursday(t)[household.ConvZeppelin], otel.Options{})

	diff(t, "the zeppelin conversation", tree(spans), `chorus.session 08:53:20.000-08:58:40.000
  chorus.turn 08:53:20.050-08:53:22.750
    chat qwen3-32b@1 08:53:20.300-08:53:22.750
      execute_tool media_search 08:53:20.400-08:53:21.600
      execute_tool speak 08:53:20.400-08:53:21.500 [speech_started speech_spoken]
      execute_tool speak 08:53:21.650-08:53:22.650 [barge_in_detected speech_truncated]
  chorus.turn 08:53:22.950-08:53:25.250
    chat qwen3-32b@1 08:53:23.200-08:53:25.250
      execute_tool ha_call_service 08:53:23.300-08:53:23.400
      execute_tool speak 08:53:23.450-08:53:25.150 [speech_started speech_spoken]
chorus.session 08:58:40.200-08:59:30.000
  chorus.turn 08:58:40.600-08:58:41.700
    chat qwen3-32b@1 08:58:40.600-08:58:41.700
      execute_tool ha_call_service 08:58:41.300-08:58:41.600
`)
	kitchen, living := named(t, spans, "chorus.session", 0), named(t, spans, "chorus.session", 1)
	if kitchen.Trace != living.Trace {
		t.Error("the living room's session is in another trace: the conversation did not follow her")
	}
	if attr(t, kitchen, "chorus.close_reason") != "migrated" || attr(t, living, "chorus.resumed") != true || attr(t, living, "chorus.satellite") != "living_room" {
		t.Errorf("kitchen %v, living room %v: want a migration and a resume", kitchen.Attrs, living.Attrs)
	}
}

// The garage sensor times out: the call fails as timed out, and when its
// detached work reports after the session ended, the late result is its own
// span under the call it outlived (ADR-0060).
//
// verifies SPEC §7, §8
func TestATimedOutCallsLateResultIsASpanUnderIt(t *testing.T) {
	log := thursday(t)[household.ConvGarage]
	last := log[len(log)-1]
	log = append(log, journal.Event{
		ConversationID: household.ConvGarage, Seq: last.Seq + 1, At: last.At.Add(time.Second),
		Kind: journal.KindToolResult, Versions: household.Versions(),
		Fields: map[string]string{"call_id": "c1", "outcome": "detached", "result_json": `{"state":"closed"}`},
	})
	spans := otel.Spans(log, otel.Options{})

	diff(t, "the garage conversation", tree(spans), `chorus.session 09:30:00.000-09:30:13.000
  chorus.turn 09:30:00.050-09:30:08.500
    chat qwen3-32b@1 09:30:00.300-09:30:08.500
      execute_tool ha_get_state 09:30:01.000-09:30:06.000 ERROR(timed_out)
        detached ha_get_state 09:30:06.000-09:30:14.000
      execute_tool speak 09:30:06.200-09:30:08.400 [speech_started speech_spoken]
`)
	late := named(t, spans, "detached ha_get_state", 0)
	if attr(t, late, "chorus.tool.outcome") != "detached" || late.Status.Failed {
		t.Errorf("late result = %v %+v, want detached and not failed", late.Attrs, late.Status)
	}
}

// Alan's jazz: the television refused at the speaker gate, his ask cut
// before the model finished, a guess in the pause thrown away, and the one
// at the end kept from 640 ms before his utterance closed (ADR-0095).
//
// verifies SPEC §11, §4.3, §8
func TestTheJazzConversationShowsTheGuessesAndTheCut(t *testing.T) {
	spans := otel.Spans(thursday(t)[household.ConvJazz], otel.Options{})

	diff(t, "the jazz conversation", tree(spans), `chorus.session 18:40:00.000-18:41:00.000
  chorus.turn 18:40:00.050-18:40:02.350
    chat qwen3-32b@1 18:40:00.300-18:40:02.350
      execute_tool ha_call_service 18:40:00.400-18:40:00.500
      execute_tool speak 18:40:00.550-18:40:02.250 [speech_started barge_in_rejected speech_spoken]
  chorus.turn 18:40:04.250-18:40:07.200 [speech_discarded]
    chat qwen3-32b@1 18:40:04.500-18:40:07.200
      execute_tool media_search 18:40:04.600-18:40:05.850
      execute_tool speak 18:40:04.600-18:40:05.800 [speech_started speech_spoken]
      execute_tool speak 18:40:05.900-18:40:07.200 [barge_in_detected speech_truncated]
  chorus.turn 18:40:07.660-18:40:12.050
    chat qwen3-32b@1 18:40:07.660-18:40:12.050
      execute_tool media_search 18:40:08.350-18:40:10.100
      execute_tool speak 18:40:08.350-18:40:10.050 [speech_started speech_spoken]
      execute_tool ha_call_service 18:40:10.150-18:40:10.200
      execute_tool speak 18:40:10.250-18:40:11.950 [speech_spoken]
  chorus.speculation 18:40:07.670-18:40:07.850
`)

	tv := named(t, spans, "execute_tool speak", 0).Events[1]
	if tv.Attrs[0] != otel.String("chorus.stage", "speaker_id") {
		t.Errorf("the television's barge-in = %+v, want refused at the speaker stage", tv)
	}
	cut := named(t, spans, "chat qwen3-32b@1", 1)
	if attr(t, cut, "chorus.cut") != true {
		t.Errorf("the ask Alan cut = %v, want it marked cut with no completion", cut.Attrs)
	}
	guess := named(t, spans, "chorus.speculation", 0)
	if attr(t, guess, "chorus.speculative") != true || attr(t, guess, "chorus.discard_reason") != "resumed" || attr(t, guess, "chorus.speaker_id") != "alan" {
		t.Errorf("the thrown-away guess = %v", guess.Attrs)
	}
	kept := named(t, spans, "chat qwen3-32b@1", 2)
	if attr(t, kept, "chorus.asked_early_ms") != int64(640) {
		t.Errorf("the kept guess = %v, want asked 640 ms early", kept.Attrs)
	}
	if turn := named(t, spans, "chorus.turn", 1); attr(t, turn, "user.id") != "alan" {
		t.Errorf("the jazz turn is %v's, want Alan's", attr(t, turn, "user.id"))
	}
}

// The front door: the held unlock and the yes are moments on the calls they
// held and ran, what Teagan's turn was told it remembers is a moment on the
// turn, and the summary written after the close follows the session.
//
// verifies SPEC §6, §5, §8
func TestTheFrontDoorShowsTheYesTheMemoryAndTheSummary(t *testing.T) {
	spans := otel.Spans(thursday(t)[household.ConvDoor], otel.Options{})

	diff(t, "the door conversation", tree(spans), `chorus.session 18:58:00.000-18:58:14.000
  chorus.turn 18:58:00.050-18:58:03.350 [memory_recalled]
    chat qwen3-32b@1 18:58:00.300-18:58:03.350
      execute_tool ha_call_service 18:58:01.100-18:58:01.100 [confirmation_requested]
      execute_tool speak 18:58:01.650-18:58:03.250 [speech_started speech_spoken]
  chorus.turn 18:58:03.750-18:58:07.150
    chat qwen3-32b@1 18:58:04.000-18:58:07.150
      execute_tool ha_call_service 18:58:04.100-18:58:04.200 [confirmation_given]
      execute_tool speak 18:58:04.250-18:58:07.050 [speech_started speech_spoken]
  chorus.summary 18:58:14.000-18:58:15.300
`)
	held := named(t, spans, "execute_tool ha_call_service", 0)
	if attr(t, held, "chorus.tool.outcome") != "confirmation_required" || held.Status.Failed {
		t.Errorf("the held unlock = %v %+v, want held, not failed", held.Attrs, held.Status)
	}
	summary := named(t, spans, "chorus.summary", 0)
	if got := attr(t, summary, "chorus.people"); !reflect.DeepEqual(got, []string{"teagan"}) {
		t.Errorf("summary kept for %v, want teagan", got)
	}
}

// The oven timer goes off to an empty kitchen: a session nobody woke, its
// speech a call of the session itself, since no turn asked for it.
//
// verifies SPEC §4, §8
func TestAnAnnouncementIsASessionWithNoTurn(t *testing.T) {
	spans := otel.Spans(thursday(t)[household.ConvOven], otel.Options{})

	diff(t, "the oven announcement", tree(spans), `chorus.session 12:22:06.550-12:22:08.150 [announcement_made]
  execute_tool speak 12:22:06.550-12:22:08.050 [speech_spoken]
`)
	session := named(t, spans, "chorus.session", 0)
	if attr(t, session, "chorus.announced") != true || attr(t, session, "chorus.close_reason") != "announced" {
		t.Errorf("the oven session = %v", session.Attrs)
	}
	said := session.Events[0]
	if !slices.Contains(said.Attrs, otel.String("chorus.requested_by", "alan")) || !slices.Contains(said.Attrs, otel.String("chorus.from_conversation", household.ConvTimer)) {
		t.Errorf("the announcement = %+v, want Alan's timer from his conversation", said.Attrs)
	}
}

// wordKeys are the attributes that carry what was said, heard, generated or
// passed to a tool.
var wordKeys = []string{
	"chorus.transcript", "chorus.speech.text", "chorus.speech.unspoken_text",
	"gen_ai.tool.call.arguments", "gen_ai.tool.call.result", "chorus.completion_json",
	"chorus.summary", "chorus.memories_json", "chorus.summaries_json",
}

func everyAttr(spans []otel.Span) []otel.Attr {
	var all []otel.Attr
	for _, s := range spans {
		all = append(all, s.Attrs...)
		for _, e := range s.Events {
			all = append(all, e.Attrs...)
		}
	}
	return all
}

// A trace backend is one more place a home's speech would be kept. Unless
// the house asks for the words, no span of the whole day carries one: who,
// where, which tool and how it went, and nothing anyone said (ADR-0101).
//
// verifies SPEC §8
func TestTheWordsStayHomeUnlessAskedFor(t *testing.T) {
	var quiet, worded []otel.Span
	for _, log := range thursday(t) {
		quiet = append(quiet, otel.Spans(log, otel.Options{})...)
		worded = append(worded, otel.Spans(log, otel.Options{Words: true})...)
	}
	said := []string{"umbrella", "zeppelin", "Zeppelin", "garage door sensor", "oat milk", "front_door", "Book club", "rainy"}
	for _, a := range everyAttr(quiet) {
		if slices.Contains(wordKeys, a.Key) {
			t.Errorf("words on a span with words off: %s = %v", a.Key, a.Value)
		}
		for _, w := range said {
			if s, ok := a.Value.(string); ok && strings.Contains(s, w) {
				t.Errorf("%s = %q says %q with words off", a.Key, s, w)
			}
		}
	}

	found := map[string]bool{}
	for _, a := range everyAttr(worded) {
		if s, ok := a.Value.(string); ok {
			found[a.Key+"="+s] = true
		}
	}
	for _, want := range []string{
		"chorus.transcript=do I need an umbrella",
		"chorus.speech.text=Today will be cloudy in the morning,",
		"chorus.speech.unspoken_text= with rain from three and a high of fourteen.",
		`gen_ai.tool.call.arguments={"entity_id":"weather.home"}`,
		`gen_ai.tool.call.result={"state":"rainy","attributes":{"temperature":14}}`,
		"chorus.summary=Teagan let book club in: the front door was unlocked after a yes.",
	} {
		if !found[want] {
			t.Errorf("with words on, no span carries %s", want)
		}
	}
}

// Reading the day twice yields the same spans, ids and all; and the day fed
// to one builder as it happened, every conversation interleaved, yields
// what each log read alone does. A live export and a later read of the log
// cannot disagree (ADR-0007, ADR-0101).
//
// verifies SPEC §8, §15
func TestTheSpansAreAFunctionOfTheLog(t *testing.T) {
	logs := thursday(t)
	var each, all []otel.Span
	var day []journal.Event
	for _, id := range slices.Sorted(func(yield func(string) bool) {
		for id := range logs {
			if !yield(id) {
				return
			}
		}
	}) {
		first, again := otel.Spans(logs[id], otel.Options{}), otel.Spans(logs[id], otel.Options{})
		if !reflect.DeepEqual(first, again) {
			t.Errorf("%s read twice gave different spans", id)
		}
		each = append(each, first...)
		day = append(day, logs[id]...)
	}
	slices.SortStableFunc(day, func(x, y journal.Event) int { return x.At.Compare(y.At) })
	b := otel.NewBuilder(otel.Options{})
	for _, e := range day {
		all = append(all, b.Add(e)...)
	}
	all = append(all, b.Unfinished()...)

	key := func(s otel.Span) string { return s.Trace.String() + s.ID.String() }
	byKey := map[string]otel.Span{}
	for _, s := range each {
		byKey[key(s)] = s
	}
	if len(all) != len(each) {
		t.Errorf("live built %d spans, the logs read alone %d", len(all), len(each))
	}
	for _, s := range all {
		if want, ok := byKey[key(s)]; !ok || !reflect.DeepEqual(s, want) {
			t.Errorf("live span %s %s differs from the log's:\n%+v\nwant\n%+v", s.Name, s.ID, s, want)
		}
	}

	ids := map[otel.SpanID]bool{}
	for _, s := range each {
		if ids[s.ID] {
			t.Errorf("span id %s used twice", s.ID)
		}
		ids[s.ID] = true
	}
	for _, s := range each {
		if !s.Parent.IsZero() && !ids[s.Parent] {
			t.Errorf("%s names a parent %s no span is", s.Name, s.Parent)
		}
		if s.End.Before(s.Start) {
			t.Errorf("%s ends before it starts", s.Name)
		}
		if strings.HasPrefix(s.Name, "chorus.unfinished") {
			t.Errorf("a finished day left %s open", s.Name)
		}
	}
	for _, a := range everyAttr(each) {
		if a.Key == "chorus.unfinished" {
			t.Error("a finished day left a span unfinished")
		}
	}
}

// chorusd stopped mid-answer: reading the log ends what it left open where
// its last event was, and says so, rather than inventing an end.
//
// verifies SPEC §8
func TestALogThatStopsMidAnswerEndsUnfinished(t *testing.T) {
	log := thursday(t)[household.ConvList]
	// Through the speak call's first frame, then nothing.
	spans := otel.Spans(log[:6], otel.Options{})

	diff(t, "the list conversation cut short", tree(spans), `chorus.session 21:04:00.000-21:04:00.650
  chorus.turn 21:04:00.050-21:04:00.650
    chat qwen3-32b@1 21:04:00.300-21:04:00.650
      execute_tool ha_call_service 21:04:00.400-21:04:00.500
      execute_tool speak 21:04:00.550-21:04:00.650 [speech_started]
`)
	for _, name := range []string{"chorus.session", "chorus.turn", "chat qwen3-32b@1", "execute_tool speak"} {
		if attr(t, named(t, spans, name, 0), "chorus.unfinished") != true {
			t.Errorf("%s is not marked unfinished", name)
		}
	}
	if _, ok := named(t, spans, "execute_tool ha_call_service", 0).Attr("chorus.unfinished"); ok {
		t.Error("the call that answered is marked unfinished")
	}
}

// porchLight is Teagan asking for the porch light on an evening the model
// went quiet and the voice failed too: the canned apology is said from audio
// rendered ahead of time (ADR-0051).
func porchLight() []journal.Event {
	at := time.Date(2025, time.October, 10, 19, 2, 0, 0, time.UTC)
	conv := "conv-1902-kitchen"
	var log []journal.Event
	add := func(ms int, kind journal.Kind, fields ...string) {
		f := map[string]string{}
		for i := 0; i < len(fields); i += 2 {
			f[fields[i]] = fields[i+1]
		}
		log = append(log, journal.Event{
			ConversationID: conv, Seq: uint64(len(log) + 1), At: at.Add(time.Duration(ms) * time.Millisecond),
			Kind: kind, Fields: f, Versions: household.Versions(),
		})
	}
	add(0, journal.KindSessionOpened, "satellite", "kitchen", "speaker_id", "teagan", "room", "kitchen", "wake_word", "hey_eddie")
	add(300, journal.KindUtteranceTranscribed, "text", "turn on the porch light", "speaker_id", "teagan", "speaker_match", "identified", "turn_end", "finished")
	add(90300, journal.KindModelFailed, "reason", "timed_out", "error", "no tokens for 90s", "canned_call_id", "canned_1")
	add(90300, journal.KindToolCalled, "tool", "speak", "call_id", "canned_1", "args_json", `{"text":"Sorry, I can't think right now."}`)
	add(90400, journal.KindSpeechFailed, "call_id", "canned_1", "reason", "tts_unavailable", "error", "kokoro: connection refused")
	add(90500, journal.KindToolResult, "call_id", "canned_1", "outcome", "error")
	add(97000, journal.KindSessionClosed, "reason", "error", "satellite", "kitchen")
	return log
}

// A model that went quiet and a voice that failed are failures in the
// trace, each naming why, and so is the session that ended in error.
//
// verifies SPEC §7, §8
func TestFailuresAreErrorsNamingWhy(t *testing.T) {
	spans := otel.Spans(porchLight(), otel.Options{})

	diff(t, "the porch light", tree(spans), `chorus.session 19:02:00.000-19:03:37.000 ERROR(error)
  chorus.turn 19:02:00.300-19:03:30.500
    chat qwen3-32b@1 19:02:00.300-19:03:30.300 ERROR(timed_out: no tokens for 90s)
    execute_tool speak 19:03:30.300-19:03:30.500 ERROR(tts_unavailable) [speech_failed]
`)
	ask := named(t, spans, "chat qwen3-32b@1", 0)
	if attr(t, ask, "error.type") != "timed_out" || attr(t, ask, "chorus.canned_call_id") != "canned_1" {
		t.Errorf("the failed ask = %v", ask.Attrs)
	}
	session := named(t, spans, "chorus.session", 0)
	if attr(t, session, "chorus.room") != "kitchen" || attr(t, session, "chorus.wake_word") != "hey_eddie" {
		t.Errorf("the session = %v, want the room and the wake word", session.Attrs)
	}
}
