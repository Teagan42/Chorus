package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/blob"
	"github.com/teaganglenn/chorus/internal/curation"
	"github.com/teaganglenn/chorus/internal/harvest"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/triage"
)

// The household's Thursday, 9 October 2025, as chorusd would have journaled
// it: three people, three satellites, every signal Triage knows, and one
// conversation from the night before so Browse has a yesterday. The browser
// tests walk a reviewer through this day; TestHouseholdDay pins what it
// derives so a fixture edit cannot quietly hollow them out.

// householdDay is midnight of the day under review.
var householdDay = time.Date(2025, time.October, 9, 0, 0, 0, 0, time.UTC)

// householdNow is when the reviewer sits down: that evening.
var householdNow = householdDay.Add(22*time.Hour + 30*time.Minute)

// The conversations, named the way the tests talk about them.
const (
	convWeather = "conv-0705-kitchen"     // Teagan cuts the forecast off: barge-in
	convZeppel  = "conv-0853-kitchen"     // Alice's album, then she walks to the living room
	convGarage  = "conv-0930-office"      // the garage sensor times out: failure
	convTimer   = "conv-1210-kitchen"     // Alan asks for the oven timer twice: repeated
	convJazz    = "conv-1840-living_room" // Alice dims, Alan asks for jazz and cuts it: flip, unattributed barge-in
	convList    = "conv-2104-office"      // nothing wrong at all
	convLock    = "conv-2230-kitchen"     // the night before
)

// The harvested pairs, by the seq of each cut.
const (
	pairWeather = convWeather + "/7"
	pairZeppel  = convZeppel + "/7"
	pairJazz    = convJazz + "/14"
)

// The chosen sides a reviewer writes once they have heard each cut.
const (
	fixWeather = "Cloudy this morning, rain from three and a high of fourteen, so take an umbrella."
	fixZeppel  = "I found three albums by Led Zeppelin. Playing the first, Led Zeppelin one."
)

type logLine struct {
	at  time.Duration // since householdDay
	rec journal.Record
}

func rec(kind journal.Kind, audio string, fields ...string) journal.Record {
	r := journal.Record{Kind: kind, AudioRef: audio, Fields: map[string]string{}}
	for i := 0; i+1 < len(fields); i += 2 {
		r.Fields[fields[i]] = fields[i+1]
	}
	return r
}

func at(h, m int, s float64) time.Duration {
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s*float64(time.Second))
}

const speakArgs = `{"mode":"queue","streamed":true}`

// householdLogs is every conversation's log, keyed by conversation id.
func householdLogs() map[string][]logLine {
	return map[string][]logLine{
		convWeather: {
			{at(7, 5, 0), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "teagan", "resumed", "false")},
			{at(7, 5, 0.4), rec(journal.KindUtteranceTranscribed, "blob://mic/weather-ask", "text", "what's the weather today", "speaker_id", "teagan")},
			{at(7, 5, 1.5), rec(journal.KindToolCalled, "", "tool", "ha_get_state", "call_id", "c1", "args_json", `{"entity_id":"weather.home"}`)},
			{at(7, 5, 1.8), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok", "result_json", `{"state":"rainy","attributes":{"temperature":14}}`)},
			{at(7, 5, 2.4), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(7, 5, 3.9), rec(journal.KindBargeInDetected, "blob://mic/weather-bargein", "tts_position_ms", "1500")},
			{at(7, 5, 4.0), rec(journal.KindSpeechTruncated, "blob://tts/weather-forecast", "spoken_text", "Today will be cloudy in the morning,", "unspoken_text", " with rain from three and a high of fourteen.", "frames_played", "24000")},
			{at(7, 5, 4.0), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "cancelled")},
			{at(7, 5, 4.1), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(7, 5, 5.2), rec(journal.KindUtteranceTranscribed, "blob://mic/weather-umbrella", "text", "do I need an umbrella", "speaker_id", "teagan")},
			{at(7, 5, 6.0), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", speakArgs)},
			{at(7, 5, 7.5), rec(journal.KindSpeechSpoken, "blob://tts/weather-yes", "text", "Yes, rain from three.", "frames_played", "24000")},
			{at(7, 5, 7.6), rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "ok")},
			{at(7, 5, 7.7), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(7, 5, 12), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "kitchen")},
		},
		convZeppel: {
			{at(8, 53, 20), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "alice", "resumed", "false")},
			{at(8, 53, 20.3), rec(journal.KindUtteranceTranscribed, "blob://mic/zeppelin-ask", "text", "play something by zeppelin", "speaker_id", "alice")},
			{at(8, 53, 21.2), rec(journal.KindToolCalled, "", "tool", "media_search", "call_id", "c1", "args_json", `{"query":"Led Zeppelin","media_type":"album","limit":5}`)},
			{at(8, 53, 21.9), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok", "result_json", `{"results":["Led Zeppelin","Led Zeppelin II","Led Zeppelin IV"]}`)},
			{at(8, 53, 22.3), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(8, 53, 22.8), rec(journal.KindBargeInDetected, "blob://mic/zeppelin-bargein", "tts_position_ms", "420")},
			{at(8, 53, 22.9), rec(journal.KindSpeechTruncated, "blob://tts/zeppelin-list", "spoken_text", "I found three", "unspoken_text", " albums by that artist: Led Zeppelin, Led Zeppelin II and Led Zeppelin IV.", "frames_played", "6720")},
			{at(8, 53, 22.9), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "cancelled")},
			{at(8, 53, 23.0), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(8, 53, 24.1), rec(journal.KindUtteranceTranscribed, "blob://mic/zeppelin-first", "text", "just the first one", "speaker_id", "alice")},
			{at(8, 53, 25.0), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c2", "args_json", `{"domain":"media_player","service":"play_media","entity_id":"media_player.kitchen","data":{"media_content_id":"album:led-zeppelin-i"}}`)},
			{at(8, 53, 25.4), rec(journal.KindToolResult, "", "call_id", "c2", "outcome", "ok")},
			{at(8, 53, 25.6), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", speakArgs)},
			{at(8, 53, 27.1), rec(journal.KindSpeechSpoken, "blob://tts/zeppelin-playing", "text", "Playing Led Zeppelin one.", "frames_played", "24000")},
			{at(8, 53, 27.2), rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "ok")},
			{at(8, 53, 27.3), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			// She carries the music to the living room; the conversation follows her.
			{at(8, 58, 40), rec(journal.KindSessionClosed, "", "reason", "migrated", "satellite", "kitchen")},
			{at(8, 58, 40.2), rec(journal.KindSessionOpened, "", "satellite", "living_room", "speaker_id", "alice", "resumed", "true")},
			{at(8, 58, 40.6), rec(journal.KindUtteranceTranscribed, "blob://mic/zeppelin-louder", "text", "turn it up a bit", "speaker_id", "alice")},
			{at(8, 58, 41.3), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c3", "args_json", `{"domain":"media_player","service":"volume_up","entity_id":"media_player.living_room"}`)},
			{at(8, 58, 41.6), rec(journal.KindToolResult, "", "call_id", "c3", "outcome", "ok")},
			{at(8, 58, 41.7), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(8, 59, 30), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "living_room")},
		},
		convGarage: {
			{at(9, 30, 0), rec(journal.KindSessionOpened, "", "satellite", "office", "speaker_id", "teagan", "resumed", "false")},
			{at(9, 30, 0.3), rec(journal.KindUtteranceTranscribed, "blob://mic/garage-ask", "text", "is the garage door closed", "speaker_id", "teagan")},
			{at(9, 30, 1.0), rec(journal.KindToolCalled, "", "tool", "ha_get_state", "call_id", "c1", "args_json", `{"entity_id":"cover.garage_door"}`)},
			{at(9, 30, 6.0), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "timed_out")},
			{at(9, 30, 6.2), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(9, 30, 8.2), rec(journal.KindSpeechSpoken, "blob://tts/garage-sorry", "text", "I couldn't reach the garage door sensor.", "frames_played", "32000")},
			{at(9, 30, 8.3), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(9, 30, 8.4), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(9, 30, 13), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "office")},
		},
		convTimer: {
			{at(12, 10, 0), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "alan", "resumed", "false")},
			{at(12, 10, 0.2), rec(journal.KindUtteranceTranscribed, "blob://mic/timer-oven", "text", "set a timer for the oven", "speaker_id", "alan")},
			{at(12, 10, 2.6), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(12, 10, 4.4), rec(journal.KindSpeechSpoken, "blob://tts/timer-howlong", "text", "Sure, how long?", "frames_played", "16000")},
			{at(12, 10, 4.5), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(12, 10, 4.6), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(12, 10, 6.4), rec(journal.KindUtteranceTranscribed, "blob://mic/timer-twelve", "text", "set a timer for twelve minutes", "speaker_id", "alan")},
			{at(12, 10, 6.9), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c1", "args_json", `{"domain":"timer","service":"start","entity_id":"timer.oven","data":{"duration":"00:12:00"}}`)},
			{at(12, 10, 7.2), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok")},
			{at(12, 10, 7.4), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", speakArgs)},
			{at(12, 10, 9.0), rec(journal.KindSpeechSpoken, "blob://tts/timer-started", "text", "Twelve minute oven timer started.", "frames_played", "24000")},
			{at(12, 10, 9.1), rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "ok")},
			{at(12, 10, 9.2), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(12, 10, 14), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "kitchen")},
		},
		convJazz: {
			{at(18, 40, 0), rec(journal.KindSessionOpened, "", "satellite", "living_room", "speaker_id", "alice", "resumed", "false")},
			{at(18, 40, 0.3), rec(journal.KindUtteranceTranscribed, "blob://mic/jazz-dim", "text", "dim the living room lights", "speaker_id", "alice")},
			{at(18, 40, 1.1), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c1", "args_json", `{"domain":"light","service":"turn_on","entity_id":"light.living_room","data":{"brightness_pct":30}}`)},
			{at(18, 40, 1.4), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok")},
			{at(18, 40, 1.6), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(18, 40, 3.1), rec(journal.KindSpeechSpoken, "blob://tts/jazz-dimmed", "text", "Dimmed to thirty percent.", "frames_played", "24000")},
			{at(18, 40, 3.2), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(18, 40, 3.3), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			// Alan, from the sofa: a second voice in Alice's conversation.
			{at(18, 40, 4.5), rec(journal.KindUtteranceTranscribed, "blob://mic/jazz-ask", "text", "and put on some jazz", "speaker_id", "alan")},
			{at(18, 40, 5.4), rec(journal.KindToolCalled, "", "tool", "media_search", "call_id", "c2", "args_json", `{"query":"jazz","media_type":"playlist","limit":3}`)},
			{at(18, 40, 6.0), rec(journal.KindToolResult, "", "call_id", "c2", "outcome", "ok", "result_json", `{"results":["Late Night Jazz","Jazz Classics","Coffee Table Jazz"]}`)},
			{at(18, 40, 6.2), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", speakArgs)},
			// He cuts it while the model is still streaming: no completion, so
			// the pair cannot be attributed to a model.
			{at(18, 40, 7.1), rec(journal.KindBargeInDetected, "blob://mic/jazz-bargein", "tts_position_ms", "900")},
			{at(18, 40, 7.2), rec(journal.KindSpeechTruncated, "blob://tts/jazz-playlist", "spoken_text", "Playing Late Night Jazz", "unspoken_text", " from Spotify, starting with Take Five.", "frames_played", "14400")},
			{at(18, 40, 7.2), rec(journal.KindSpeechDiscarded, "", "unspoken_text", "Say skip to hear the next one.", "reason", "barge_in")},
			{at(18, 40, 7.2), rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "cancelled")},
			{at(18, 40, 8.0), rec(journal.KindUtteranceTranscribed, "blob://mic/jazz-quieter", "text", "something quieter", "speaker_id", "alan")},
			{at(18, 40, 8.9), rec(journal.KindToolCalled, "", "tool", "media_search", "call_id", "c3", "args_json", `{"query":"quiet jazz piano","media_type":"playlist","limit":3}`)},
			{at(18, 40, 9.4), rec(journal.KindToolResult, "", "call_id", "c3", "outcome", "ok")},
			{at(18, 40, 9.6), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c4", "args_json", `{"domain":"media_player","service":"play_media","entity_id":"media_player.living_room","data":{"media_content_id":"playlist:quiet-jazz-piano"}}`)},
			{at(18, 40, 9.9), rec(journal.KindToolResult, "", "call_id", "c4", "outcome", "ok")},
			{at(18, 40, 10.1), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s3", "args_json", speakArgs)},
			{at(18, 40, 11.6), rec(journal.KindSpeechSpoken, "blob://tts/jazz-quiet", "text", "Playing Quiet Jazz Piano.", "frames_played", "24000")},
			{at(18, 40, 11.7), rec(journal.KindToolResult, "", "call_id", "s3", "outcome", "ok")},
			{at(18, 40, 11.8), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(18, 41, 0), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "living_room")},
		},
		convList: {
			{at(21, 4, 0), rec(journal.KindSessionOpened, "", "satellite", "office", "speaker_id", "teagan", "resumed", "false")},
			{at(21, 4, 0.3), rec(journal.KindUtteranceTranscribed, "blob://mic/list-oatmilk", "text", "add oat milk to the shopping list", "speaker_id", "teagan")},
			{at(21, 4, 1.2), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c1", "args_json", `{"domain":"todo","service":"add_item","entity_id":"todo.shopping_list","data":{"item":"oat milk"}}`)},
			{at(21, 4, 1.5), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok")},
			{at(21, 4, 1.7), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(21, 4, 2.7), rec(journal.KindSpeechSpoken, "blob://tts/list-added", "text", "Added oat milk.", "frames_played", "16000")},
			{at(21, 4, 2.8), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(21, 4, 2.9), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(21, 4, 8), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "office")},
		},
		convLock: {
			{at(-2, 30, 0), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "teagan", "resumed", "false")},
			{at(-2, 30, 0.3), rec(journal.KindUtteranceTranscribed, "blob://mic/lock-ask", "text", "lock the front door", "speaker_id", "teagan")},
			{at(-2, 30, 1.0), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c1", "args_json", `{"domain":"lock","service":"lock","entity_id":"lock.front_door"}`)},
			{at(-2, 30, 1.6), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok")},
			{at(-2, 30, 1.8), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(-2, 30, 2.8), rec(journal.KindSpeechSpoken, "blob://tts/lock-done", "text", "Front door locked.", "frames_played", "16000")},
			{at(-2, 30, 2.9), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(-2, 30, 3.0), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(-2, 30, 8), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "kitchen")},
		},
		// Wakes the second stage threw out: the dishwasher, and a podcast.
		"device:kitchen": {
			{at(14, 2, 0), rec(journal.KindWakeRejected, "blob://wake/kitchen-dishwasher", "reason", "no_speech")},
		},
		"device:office": {
			{at(21, 15, 0), rec(journal.KindWakeRejected, "blob://wake/office-podcast", "reason", "no_speech")},
		},
	}
}

// householdClips is how long each recording lasts, in seconds of device
// audio (16 kHz s16le mono).
var householdClips = map[string]float64{
	"mic/weather-ask": 1.2, "mic/weather-bargein": 0.6, "mic/weather-umbrella": 1.1,
	"tts/weather-forecast": 4, "tts/weather-yes": 1.5,
	"mic/zeppelin-ask": 1.4, "mic/zeppelin-bargein": 0.5, "mic/zeppelin-first": 0.9, "mic/zeppelin-louder": 0.8,
	"tts/zeppelin-list": 5, "tts/zeppelin-playing": 1.5,
	"mic/garage-ask": 1.3, "tts/garage-sorry": 2,
	"mic/timer-oven": 1.2, "mic/timer-twelve": 1.6, "tts/timer-howlong": 1, "tts/timer-started": 1.5,
	"mic/jazz-dim": 1.4, "mic/jazz-ask": 1.1, "mic/jazz-bargein": 0.7, "mic/jazz-quieter": 0.9,
	"tts/jazz-dimmed": 1.5, "tts/jazz-playlist": 3.5, "tts/jazz-quiet": 1.5,
	"mic/list-oatmilk": 1.6, "tts/list-added": 1,
	"mic/lock-ask": 1, "tts/lock-done": 1,
	"wake/kitchen-dishwasher": 0.8, "wake/office-podcast": 1.2,
}

// householdJournal writes the day through a real journal, one append at a
// time, at the moment each event happened.
func householdJournal(t *testing.T) *journal.MemStore {
	t.Helper()
	store := journal.NewMemStore()
	clk := &stepClock{}
	j := journal.New(store, clk, journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"})
	for id, lines := range householdLogs() {
		for _, l := range lines {
			clk.now = householdDay.Add(l.at)
			if _, err := j.Append(context.Background(), id, l.rec); err != nil {
				t.Fatalf("%s: append %s: %v", id, l.rec.Kind, err)
			}
		}
	}
	return store
}

func householdBlobs(t *testing.T) *blob.Memory {
	t.Helper()
	m := blob.NewMemory()
	for key, seconds := range householdClips {
		w, err := m.Create(context.Background(), key)
		if err != nil {
			t.Fatalf("create %s: %v", key, err)
		}
		if _, err := w.Write(make([]byte, int(seconds*32000))); err != nil {
			t.Fatalf("write %s: %v", key, err)
		}
		if _, err := w.Commit(); err != nil {
			t.Fatalf("commit %s: %v", key, err)
		}
	}
	return m
}

// newHouseholdServer serves the day with no verdicts yet.
func newHouseholdServer(t *testing.T) (*server, curation.Store) {
	t.Helper()
	decisions := curation.NewMemStore()
	s := newServer(householdJournal(t), decisions, householdBlobs(t), func() time.Time { return householdNow })
	return s, decisions
}

// TestHouseholdDay pins what the browser tests count on finding in the day.
func TestHouseholdDay(t *testing.T) {
	store := householdJournal(t)
	ctx := context.Background()

	var pairs []string
	attributed := map[string]bool{}
	signals := map[triage.Kind][]string{}
	for id := range householdLogs() {
		if _, err := journal.Replay(ctx, store, id, journal.Overrides{}); err != nil {
			t.Errorf("%s does not replay: %v", id, err)
		}
		if id[:7] == "device:" {
			continue
		}
		hs, err := harvest.Harvest(ctx, store, id)
		if err != nil {
			t.Fatalf("harvest %s: %v", id, err)
		}
		for _, p := range hs {
			pairs = append(pairs, p.ID)
			attributed[p.ID] = p.Attributed
		}
		sigs, err := triage.Scan(ctx, store, id)
		if err != nil {
			t.Fatalf("triage %s: %v", id, err)
		}
		for _, s := range sigs {
			signals[s.Kind] = append(signals[s.Kind], s.ConversationID)
		}
	}
	if len(pairs) != 3 || !attributed[pairWeather] || !attributed[pairZeppel] || attributed[pairJazz] {
		t.Errorf("pairs = %v (attributed %v), want weather and zeppelin attributed, jazz not", pairs, attributed)
	}
	want := map[triage.Kind][]string{
		triage.KindBargeIn:     {convWeather, convZeppel, convJazz},
		triage.KindFailure:     {convGarage},
		triage.KindRepeated:    {convTimer},
		triage.KindSpeakerFlip: {convJazz},
	}
	for k, convs := range want {
		got := map[string]bool{}
		for _, c := range signals[k] {
			got[c] = true
		}
		if len(signals[k]) != len(convs) {
			t.Errorf("%s signals = %v, want %v", k, signals[k], convs)
		}
		for _, c := range convs {
			if !got[c] {
				t.Errorf("%s signals = %v, missing %s", k, signals[k], c)
			}
		}
	}
	for ref := range householdClips {
		found := false
		for _, lines := range householdLogs() {
			for _, l := range lines {
				found = found || l.rec.AudioRef == "blob://"+ref
			}
		}
		if !found {
			t.Errorf("clip %s is cited by no event", ref)
		}
	}
}

// The evening's curation through the handlers alone: the counterpart of the
// browser journey, so a failure there can be told apart from one here.
//
// verifies SPEC §9.1
func TestHouseholdCurationExportsOnlyTheFixedAttributedPairs(t *testing.T) {
	s, _ := newHouseholdServer(t)
	for _, step := range []struct {
		id, action string
		form       url.Values
	}{
		{pairWeather, "save", url.Values{"chosen": {fixWeather}}},
		{pairWeather, "accept", nil},
		{pairZeppel, "accept-anyway", nil},
		{pairZeppel, "save", url.Values{"chosen": {fixZeppel}}},
		{pairJazz, "save", url.Values{"chosen": {"I found three jazz playlists. Playing Late Night Jazz."}}},
		{pairJazz, "accept", nil},
	} {
		if code, h := post(t, s, "/pairs/"+step.id+"/"+step.action, step.form); code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", step.action, step.id, code, h)
		}
	}
	body := strings.TrimSpace(get(t, s, "/export/dpo.jsonl"))
	lines := strings.Split(body, "\n")
	if len(lines) != 2 {
		t.Fatalf("export has %d rows, want weather and zeppelin (jazz is unattributed):\n%s", len(lines), body)
	}
	for i, want := range []string{fixZeppel, fixWeather} {
		if !strings.Contains(lines[i], `"content":"`+want+`"`) {
			t.Errorf("row %d does not carry the fix %q:\n%s", i, want, lines[i])
		}
	}
	if h := get(t, s, "/export"); !strings.Contains(h, "Held · unattributed") || !strings.Contains(h, "Download 2 rows (JSONL)") {
		t.Error("the export page does not count the held jazz pair beside the two rows")
	}
}
