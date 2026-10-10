// Package household is one made-up household's Thursday, 9 October 2025, as
// chorusd would have journaled it: three people, three satellites, every
// signal Triage knows, a timer going off and one called off, a television the
// barge-in gate refused, a door that waits for a yes, and one conversation
// from the night before. The review UI's browser tests walk a reviewer through this day, and
// the hosted demo serves it, so both show the same household.
//
// The audio is synthetic: voice.json says who says what and for how long,
// and internal/tools/householdvoice speaks it with Kokoro into audio/.
package household

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/journal"
)

// The conversations, named for what happens in them.
const (
	ConvWeather  = "conv-0705-kitchen"     // Teagan cuts the forecast off: barge-in
	ConvZeppelin = "conv-0853-kitchen"     // Alice's album, then she walks to the living room
	ConvGarage   = "conv-0930-office"      // the garage sensor times out: failure, slow
	ConvTimer    = "conv-1210-kitchen"     // Alan asks for the oven timer twice: repeated
	ConvOven     = "conv-1222-kitchen"     // the oven timer goes off: an announcement
	ConvPasta    = "conv-1748-kitchen"     // Teagan sets a pasta timer, then calls it off
	ConvJazz     = "conv-1840-living_room" // Alice dims, Alan asks for jazz and cuts it: flip, unattributed barge-in
	ConvDoor     = "conv-1858-kitchen"     // the front door waits for Teagan's yes: recall, nonce, summary, slow
	ConvList     = "conv-2104-office"      // nothing wrong at all
	ConvLock     = "conv-2230-kitchen"     // the night before
)

// DoorNonce is what the held unlock was handed to ask with.
const DoorNonce = "cf_4c1e9a07"

// BookClub is what Teagan's turn was told it remembers when book club came.
func BookClub() []journal.Memory {
	return []journal.Memory{
		{ID: "m_3b8f0a21", Person: "teagan", Fact: "Book club meets here on Thursdays at seven.", Shareable: true},
		{ID: "m_c41d9e07", Person: "alice", Fact: "Leave the porch light on when guests are coming.", Shareable: true},
	}
}

// LastNight is the conversation Teagan's turn was told of: the door locked
// the night before, as its summary said.
func LastNight() []journal.Summary {
	return []journal.Summary{{ConversationID: ConvLock, At: Day().Add(at(-2, 30, 8.1)), Text: "Teagan locked the front door for the night."}}
}

// OvenTimer is the timer Alan set for the oven.
const OvenTimer = "t_0a7e11c3"

// PastaTimer is the timer Teagan cancelled before the water boiled.
const PastaTimer = "t_9d2b4f60"

// The harvested pairs, by the seq of each cut.
const (
	PairWeather  = ConvWeather + "/8"
	PairZeppelin = ConvZeppelin + "/8"
	PairJazz     = ConvJazz + "/17"
)

// Day is midnight of the day under review.
func Day() time.Time { return time.Date(2025, time.October, 9, 0, 0, 0, 0, time.UTC) }

// ReviewedAt is when the reviewer sits down: that evening.
func ReviewedAt() time.Time { return Day().Add(22*time.Hour + 30*time.Minute) }

// Versions is what every turn of the day ran under: Parakeet heard it and
// Kokoro spoke it, named as chorusd names its providers (ADR-0032).
func Versions() journal.Versions {
	return journal.Versions{
		Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7",
		STT: "istupakov/parakeet-tdt-0.6b-v2-onnx", TTS: "kokoro/af_heart",
	}
}

// Line is one event of a log, At after midnight of Day.
type Line struct {
	At  time.Duration
	Rec journal.Record
}

func rec(kind journal.Kind, audio string, fields ...string) journal.Record {
	r := journal.Record{Kind: kind, AudioRef: audio, Fields: map[string]string{}}
	for i := 0; i+1 < len(fields); i += 2 {
		r.Fields[fields[i]] = fields[i+1]
	}
	return r
}

// presence is the radar's report as chorusd journals it (ADR-0050).
func presence(state string) journal.Record {
	return rec(journal.KindPresenceChanged, "", "state", state, "sensor", "room_presence")
}

func at(h, m int, s float64) time.Duration {
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s*float64(time.Second))
}

// Trailing is how long before its transcript each ask stopped: Smart Turn's
// pause and the decode, which the person waits through too (ADR-0035).
const Trailing = 250 * time.Millisecond

// started is a turn's first played frame, wait ms after its ask stopped.
func started(call string, wait int) journal.Record {
	return rec(journal.KindSpeechStarted, "", "call_id", call, "wait_ms", strconv.Itoa(wait))
}

const speakArgs = `{"mode":"queue","streamed":true}`

const doorArgs = `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`

// Logs is every conversation's log, keyed by conversation id.
func Logs() map[string][]Line {
	return map[string][]Line{
		ConvWeather: {
			{at(7, 5, 0), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "teagan", "resumed", "false")},
			{at(7, 5, 0.4), rec(journal.KindUtteranceTranscribed, "blob://mic/weather-ask", "text", "what's the weather today", "speaker_id", "teagan")},
			{at(7, 5, 0.5), rec(journal.KindToolCalled, "", "tool", "ha_get_state", "call_id", "c1", "args_json", `{"entity_id":"weather.home"}`)},
			{at(7, 5, 0.6), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok", "result_json", `{"state":"rainy","attributes":{"temperature":14}}`)},
			{at(7, 5, 0.65), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(7, 5, 0.75), started("s1", 600)},
			{at(7, 5, 2.25), rec(journal.KindBargeInDetected, "blob://mic/weather-bargein", "tts_position_ms", "1500")},
			{at(7, 5, 2.35), rec(journal.KindSpeechTruncated, "blob://tts/weather-forecast", "spoken_text", "Today will be cloudy in the morning,", "unspoken_text", " with rain from three and a high of fourteen.", "frames_played", "24000", "call_id", "s1", "reason", "barge_in")},
			{at(7, 5, 2.35), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "cancelled")},
			{at(7, 5, 2.45), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(7, 5, 3.6), rec(journal.KindUtteranceTranscribed, "blob://mic/weather-umbrella", "text", "do I need an umbrella", "speaker_id", "teagan")},
			{at(7, 5, 3.75), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", speakArgs)},
			{at(7, 5, 3.85), started("s2", 500)},
			{at(7, 5, 5.35), rec(journal.KindSpeechSpoken, "blob://tts/weather-yes", "text", "Yes, rain from three.", "frames_played", "24000", "call_id", "s2")},
			{at(7, 5, 5.45), rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "ok")},
			{at(7, 5, 5.55), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(7, 5, 12), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "kitchen")},
		},
		ConvZeppelin: {
			{at(8, 53, 20), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "alice", "resumed", "false")},
			{at(8, 53, 20.3), rec(journal.KindUtteranceTranscribed, "blob://mic/zeppelin-ask", "text", "play something by zeppelin", "speaker_id", "alice")},
			{at(8, 53, 20.4), rec(journal.KindToolCalled, "", "tool", "media_search", "call_id", "c1", "args_json", `{"query":"Led Zeppelin","limit":5}`)},
			{at(8, 53, 20.55), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok", "result_json", `{"results":["Led Zeppelin","Led Zeppelin II","Led Zeppelin IV"]}`)},
			{at(8, 53, 20.6), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(8, 53, 20.7), started("s1", 650)},
			{at(8, 53, 21.5), rec(journal.KindBargeInDetected, "blob://mic/zeppelin-bargein", "tts_position_ms", "800")},
			{at(8, 53, 21.6), rec(journal.KindSpeechTruncated, "blob://tts/zeppelin-list", "spoken_text", "I found three", "unspoken_text", " albums by that artist: Led Zeppelin, Led Zeppelin II and Led Zeppelin IV.", "frames_played", "12800", "call_id", "s1", "reason", "barge_in")},
			{at(8, 53, 21.6), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "cancelled")},
			{at(8, 53, 21.7), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(8, 53, 23.2), rec(journal.KindUtteranceTranscribed, "blob://mic/zeppelin-first", "text", "just the first one", "speaker_id", "alice")},
			{at(8, 53, 23.3), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c2", "args_json", `{"domain":"media_player","service":"play_media","entity_id":"media_player.kitchen","data":{"media_content_id":"album:led-zeppelin-i"}}`)},
			{at(8, 53, 23.4), rec(journal.KindToolResult, "", "call_id", "c2", "outcome", "ok")},
			{at(8, 53, 23.45), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", speakArgs)},
			{at(8, 53, 23.55), started("s2", 600)},
			{at(8, 53, 25.05), rec(journal.KindSpeechSpoken, "blob://tts/zeppelin-playing", "text", "Playing Led Zeppelin one.", "frames_played", "24000", "call_id", "s2")},
			{at(8, 53, 25.15), rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "ok")},
			{at(8, 53, 25.25), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			// She carries the music to the living room; the conversation follows her.
			{at(8, 58, 40), rec(journal.KindSessionClosed, "", "reason", "migrated", "satellite", "kitchen")},
			{at(8, 58, 40.2), rec(journal.KindSessionOpened, "", "satellite", "living_room", "speaker_id", "alice", "resumed", "true")},
			{at(8, 58, 40.6), rec(journal.KindUtteranceTranscribed, "blob://mic/zeppelin-louder", "text", "turn it up a bit", "speaker_id", "alice")},
			{at(8, 58, 41.3), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c3", "args_json", `{"domain":"media_player","service":"volume_up","entity_id":"media_player.living_room"}`)},
			{at(8, 58, 41.6), rec(journal.KindToolResult, "", "call_id", "c3", "outcome", "ok")},
			{at(8, 58, 41.7), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(8, 59, 30), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "living_room")},
		},
		ConvGarage: {
			{at(9, 30, 0), rec(journal.KindSessionOpened, "", "satellite", "office", "speaker_id", "teagan", "resumed", "false")},
			{at(9, 30, 0.3), rec(journal.KindUtteranceTranscribed, "blob://mic/garage-ask", "text", "is the garage door closed", "speaker_id", "teagan")},
			{at(9, 30, 1.0), rec(journal.KindToolCalled, "", "tool", "ha_get_state", "call_id", "c1", "args_json", `{"entity_id":"cover.garage_door"}`)},
			{at(9, 30, 6.0), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "timed_out")},
			{at(9, 30, 6.2), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			// Teagan waited out the sensor's timeout in silence.
			{at(9, 30, 6.3), started("s1", 6250)},
			{at(9, 30, 8.3), rec(journal.KindSpeechSpoken, "blob://tts/garage-sorry", "text", "I couldn't reach the garage door sensor.", "frames_played", "32000", "call_id", "s1")},
			{at(9, 30, 8.4), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(9, 30, 8.5), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(9, 30, 13), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "office")},
		},
		ConvTimer: {
			{at(12, 10, 0), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "alan", "resumed", "false")},
			{at(12, 10, 0.2), rec(journal.KindUtteranceTranscribed, "blob://mic/timer-oven", "text", "set a timer for the oven", "speaker_id", "alan")},
			{at(12, 10, 0.35), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(12, 10, 0.45), started("s1", 500)},
			{at(12, 10, 1.45), rec(journal.KindSpeechSpoken, "blob://tts/timer-howlong", "text", "Sure, how long?", "frames_played", "16000", "call_id", "s1")},
			{at(12, 10, 1.55), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(12, 10, 1.65), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(12, 10, 6.4), rec(journal.KindUtteranceTranscribed, "blob://mic/timer-twelve", "text", "set a timer for twelve minutes", "speaker_id", "alan")},
			{at(12, 10, 6.5), rec(journal.KindToolCalled, "", "tool", "timer_start", "call_id", "c1", "args_json", `{"seconds":720,"label":"oven"}`)},
			{at(12, 10, 6.6), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok", "result_json", `{"timer_id":"`+OvenTimer+`","label":"oven","satellite":"kitchen","seconds_left":720,"says":"The oven timer is done."}`)},
			{at(12, 10, 6.65), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", speakArgs)},
			{at(12, 10, 6.75), started("s2", 600)},
			{at(12, 10, 8.25), rec(journal.KindSpeechSpoken, "blob://tts/timer-started", "text", "Twelve minute oven timer started.", "frames_played", "24000", "call_id", "s2")},
			{at(12, 10, 8.35), rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "ok")},
			{at(12, 10, 8.45), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(12, 10, 14), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "kitchen")},
		},
		// Twelve minutes on, with nobody talking to it, the kitchen says so.
		// An announcement answers no ask, so it records no first frame.
		ConvOven: {
			{at(12, 22, 6.55), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "announced", "true", "resumed", "false")},
			{at(12, 22, 6.55), rec(journal.KindAnnouncementMade, "", "text", "The oven timer is done.", "call_id", "an_5c19e2d0", "source", "timer", "timer_id", OvenTimer, "requested_by", "alan", "from_satellite", "kitchen", "from_conversation", ConvTimer, "start_conversation", "false")},
			{at(12, 22, 6.55), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "an_5c19e2d0", "args_json", `{"text":"The oven timer is done.","mode":"queue"}`)},
			{at(12, 22, 8.05), rec(journal.KindSpeechSpoken, "blob://tts/oven-done", "text", "The oven timer is done.", "frames_played", "24000", "call_id", "an_5c19e2d0")},
			{at(12, 22, 8.05), rec(journal.KindToolResult, "", "call_id", "an_5c19e2d0", "outcome", "ok")},
			{at(12, 22, 8.15), rec(journal.KindSessionClosed, "", "reason", "announced", "satellite", "kitchen")},
		},
		// Timers belong to the house, not to the conversation that set them.
		journal.HouseTimers: {
			{at(12, 10, 6.55), rec(journal.KindTimerStarted, "", "timer_id", OvenTimer, "seconds", "720", "fires_at", Day().Add(at(12, 22, 6.55)).Format(time.RFC3339Nano), "label", "oven", "satellite", "kitchen", "person", "alan", "conversation_id", ConvTimer, "call_id", "c1")},
			{at(12, 22, 8.15), rec(journal.KindTimerFinished, "", "timer_id", OvenTimer, "outcome", "announced", "conversation_id", ConvOven)},
			{at(17, 48, 0.45), rec(journal.KindTimerStarted, "", "timer_id", PastaTimer, "seconds", "600", "fires_at", Day().Add(at(17, 58, 0.45)).Format(time.RFC3339Nano), "label", "pasta", "satellite", "kitchen", "person", "teagan", "conversation_id", ConvPasta, "call_id", "c1")},
			{at(17, 48, 6.25), rec(journal.KindTimerCancelled, "", "timer_id", PastaTimer, "conversation_id", ConvPasta, "call_id", "c2")},
		},
		// Dinner: the water is not boiling yet, so the timer is called off.
		ConvPasta: {
			{at(17, 48, 0), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "teagan", "resumed", "false")},
			{at(17, 48, 0.3), rec(journal.KindUtteranceTranscribed, "blob://mic/pasta-ask", "text", "set a pasta timer for ten minutes", "speaker_id", "teagan")},
			{at(17, 48, 0.4), rec(journal.KindToolCalled, "", "tool", "timer_start", "call_id", "c1", "args_json", `{"seconds":600,"label":"pasta"}`)},
			{at(17, 48, 0.5), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok", "result_json", `{"timer_id":"`+PastaTimer+`","label":"pasta","satellite":"kitchen","seconds_left":600,"says":"The pasta timer is done."}`)},
			{at(17, 48, 0.55), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(17, 48, 0.65), started("s1", 600)},
			{at(17, 48, 2.45), rec(journal.KindSpeechSpoken, "blob://tts/pasta-started", "text", "Ten minute pasta timer started.", "frames_played", "28800", "call_id", "s1")},
			{at(17, 48, 2.55), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(17, 48, 2.65), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(17, 48, 6.1), rec(journal.KindUtteranceTranscribed, "blob://mic/pasta-cancel", "text", "actually cancel it, the water isn't boiling yet", "speaker_id", "teagan")},
			{at(17, 48, 6.2), rec(journal.KindToolCalled, "", "tool", "timer_cancel", "call_id", "c2", "args_json", `{"timer_id":"`+PastaTimer+`"}`)},
			{at(17, 48, 6.3), rec(journal.KindToolResult, "", "call_id", "c2", "outcome", "ok", "result_json", `{"cancelled":"`+PastaTimer+`","label":"pasta"}`)},
			{at(17, 48, 6.35), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", speakArgs)},
			{at(17, 48, 6.45), started("s2", 600)},
			{at(17, 48, 7.75), rec(journal.KindSpeechSpoken, "blob://tts/pasta-cancelled", "text", "Pasta timer cancelled.", "frames_played", "20800", "call_id", "s2")},
			{at(17, 48, 7.85), rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "ok")},
			{at(17, 48, 7.95), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(17, 48, 13), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "kitchen")},
		},
		ConvJazz: {
			{at(18, 40, 0), rec(journal.KindSessionOpened, "", "satellite", "living_room", "speaker_id", "alice", "resumed", "false")},
			{at(18, 40, 0.3), rec(journal.KindUtteranceTranscribed, "blob://mic/jazz-dim", "text", "dim the living room lights", "speaker_id", "alice")},
			{at(18, 40, 0.4), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c1", "args_json", `{"domain":"light","service":"turn_on","entity_id":"light.living_room","data":{"brightness_pct":30}}`)},
			{at(18, 40, 0.5), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok")},
			{at(18, 40, 0.55), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(18, 40, 0.65), started("s1", 600)},
			// The television talks over the answer; the gate knows no such voice.
			{at(18, 40, 1.4), rec(journal.KindBargeInRejected, "blob://mic/jazz-tv", "stage", "speaker_id")},
			{at(18, 40, 2.15), rec(journal.KindSpeechSpoken, "blob://tts/jazz-dimmed", "text", "Dimmed to thirty percent.", "frames_played", "24000", "call_id", "s1")},
			{at(18, 40, 2.25), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(18, 40, 2.35), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			// Alan, from the sofa: a second voice in Alice's conversation.
			{at(18, 40, 4.5), rec(journal.KindUtteranceTranscribed, "blob://mic/jazz-ask", "text", "and put on some jazz", "speaker_id", "alan")},
			{at(18, 40, 4.6), rec(journal.KindToolCalled, "", "tool", "media_search", "call_id", "c2", "args_json", `{"query":"jazz","limit":3}`)},
			{at(18, 40, 4.75), rec(journal.KindToolResult, "", "call_id", "c2", "outcome", "ok", "result_json", `{"results":["Late Night Jazz","Jazz Classics","Coffee Table Jazz"]}`)},
			{at(18, 40, 4.8), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", speakArgs)},
			{at(18, 40, 4.9), started("s2", 650)},
			// He cuts it while the model is still streaming: no completion, so
			// the pair cannot be attributed to a model.
			{at(18, 40, 6.0), rec(journal.KindBargeInDetected, "blob://mic/jazz-bargein", "tts_position_ms", "1100")},
			{at(18, 40, 6.1), rec(journal.KindSpeechTruncated, "blob://tts/jazz-playlist", "spoken_text", "Playing Late Night Jazz", "unspoken_text", " from Spotify, starting with Take Five.", "frames_played", "17600", "call_id", "s2", "reason", "barge_in")},
			{at(18, 40, 6.1), rec(journal.KindSpeechDiscarded, "", "unspoken_text", "Say skip to hear the next one.", "reason", "barge_in")},
			{at(18, 40, 6.1), rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "cancelled")},
			{at(18, 40, 7.3), rec(journal.KindUtteranceTranscribed, "blob://mic/jazz-quieter", "text", "something quieter", "speaker_id", "alan")},
			{at(18, 40, 7.35), rec(journal.KindToolCalled, "", "tool", "media_search", "call_id", "c3", "args_json", `{"query":"quiet jazz piano","limit":3}`)},
			{at(18, 40, 7.45), rec(journal.KindToolResult, "", "call_id", "c3", "outcome", "ok")},
			{at(18, 40, 7.5), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c4", "args_json", `{"domain":"media_player","service":"play_media","entity_id":"media_player.living_room","data":{"media_content_id":"playlist:quiet-jazz-piano"}}`)},
			{at(18, 40, 7.55), rec(journal.KindToolResult, "", "call_id", "c4", "outcome", "ok")},
			{at(18, 40, 7.6), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s3", "args_json", speakArgs)},
			{at(18, 40, 7.7), started("s3", 650)},
			{at(18, 40, 9.2), rec(journal.KindSpeechSpoken, "blob://tts/jazz-quiet", "text", "Playing Quiet Jazz Piano.", "frames_played", "24000", "call_id", "s3")},
			{at(18, 40, 9.3), rec(journal.KindToolResult, "", "call_id", "s3", "outcome", "ok")},
			{at(18, 40, 9.4), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(18, 41, 0), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "living_room")},
		},
		ConvList: {
			{at(21, 4, 0), rec(journal.KindSessionOpened, "", "satellite", "office", "speaker_id", "teagan", "resumed", "false")},
			{at(21, 4, 0.3), rec(journal.KindUtteranceTranscribed, "blob://mic/list-oatmilk", "text", "add oat milk to the shopping list", "speaker_id", "teagan")},
			{at(21, 4, 0.4), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c1", "args_json", `{"domain":"todo","service":"add_item","entity_id":"todo.shopping_list","data":{"item":"oat milk"}}`)},
			{at(21, 4, 0.5), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok")},
			{at(21, 4, 0.55), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(21, 4, 0.65), started("s1", 600)},
			{at(21, 4, 1.65), rec(journal.KindSpeechSpoken, "blob://tts/list-added", "text", "Added oat milk.", "frames_played", "16000", "call_id", "s1")},
			{at(21, 4, 1.75), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(21, 4, 1.85), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(21, 4, 8), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "office")},
		},
		ConvLock: {
			{at(-2, 30, 0), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "teagan", "resumed", "false")},
			{at(-2, 30, 0.3), rec(journal.KindUtteranceTranscribed, "blob://mic/lock-ask", "text", "lock the front door", "speaker_id", "teagan")},
			{at(-2, 30, 0.4), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c1", "args_json", `{"domain":"lock","service":"lock","entity_id":"lock.front_door"}`)},
			{at(-2, 30, 0.5), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok")},
			{at(-2, 30, 0.55), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(-2, 30, 0.65), started("s1", 600)},
			{at(-2, 30, 1.65), rec(journal.KindSpeechSpoken, "blob://tts/lock-done", "text", "Front door locked.", "frames_played", "16000", "call_id", "s1")},
			{at(-2, 30, 1.75), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(-2, 30, 1.85), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(-2, 30, 8), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "kitchen")},
			{at(-2, 30, 8.1), rec(journal.KindConversationSummarized, "", "people_json", `["teagan"]`, "summary", LastNight()[0].Text)},
		},
		// Book club arrives. Unlocking waits for a yes the log can vouch for
		// (ADR-0038), and the summary is written after the close (ADR-0043).
		ConvDoor: {
			{at(18, 58, 0), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "teagan", "resumed", "false")},
			{at(18, 58, 0.3), rec(journal.KindUtteranceTranscribed, "blob://mic/door-ask", "text", "unlock the front door", "speaker_id", "teagan")},
			{at(18, 58, 0.4), rec(journal.KindMemoryRecalled, "", "person", "teagan", "memories_json", journal.EncodeMemories(BookClub()), "summaries_json", journal.EncodeSummaries(LastNight()))},
			{at(18, 58, 1.1), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c1", "args_json", doorArgs)},
			{at(18, 58, 1.1), rec(journal.KindConfirmationRequested, "", "call_id", "c1", "nonce", DoorNonce)},
			{at(18, 58, 1.1), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "confirmation_required", "result_json", `{"confirmation_required":true,"nonce":"`+DoorNonce+`","note":"Ask the person, then call again with this nonce as confirmation once they agree."}`)},
			{at(18, 58, 1.65), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			// Told what it remembers and held for a yes, the model was slow.
			{at(18, 58, 1.75), started("s1", 1700)},
			{at(18, 58, 3.15), rec(journal.KindSpeechSpoken, "blob://tts/door-confirm", "text", "Unlock the front door?", "frames_played", "22400", "call_id", "s1")},
			{at(18, 58, 3.25), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(18, 58, 3.35), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(18, 58, 4.0), rec(journal.KindUtteranceTranscribed, "blob://mic/door-yes", "text", "yes", "speaker_id", "teagan")},
			{at(18, 58, 4.1), rec(journal.KindToolCalled, "", "tool", "ha_call_service", "call_id", "c2", "args_json", `{"domain":"lock","service":"unlock","entity_id":"lock.front_door","confirmation":"`+DoorNonce+`"}`)},
			{at(18, 58, 4.1), rec(journal.KindConfirmationGiven, "", "call_id", "c2", "nonce", DoorNonce)},
			{at(18, 58, 4.2), rec(journal.KindToolResult, "", "call_id", "c2", "outcome", "ok")},
			{at(18, 58, 4.25), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", speakArgs)},
			{at(18, 58, 4.35), started("s2", 600)},
			{at(18, 58, 6.95), rec(journal.KindSpeechSpoken, "blob://tts/door-unlocked", "text", "Front door unlocked. Book club starts at seven.", "frames_played", "41600", "call_id", "s2")},
			{at(18, 58, 7.05), rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "ok")},
			{at(18, 58, 7.15), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(18, 58, 14), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "kitchen")},
			{at(18, 58, 15.3), rec(journal.KindConversationSummarized, "", "people_json", `["teagan"]`, "summary", "Teagan let book club in: the front door was unlocked after a yes.")},
		},
		// Wakes the second stage threw out: the dishwasher, and a podcast.
		// The kitchen and the living room have the mmWave radar; the office's
		// Voice PE has none, so its log holds no presence.
		"device:kitchen": {
			{at(6, 52, 0), presence("present")}, // Teagan makes coffee
			{at(7, 21, 0), presence("absent")},
			{at(8, 50, 0), presence("present")}, // Alice
			{at(8, 58, 30), presence("absent")}, // she carries the music next door
			{at(12, 8, 0), presence("present")}, // Alan, about the oven
			{at(12, 14, 0), presence("absent")}, // so the timer goes off to an empty room
			{at(14, 2, 0), rec(journal.KindWakeRejected, "blob://wake/kitchen-dishwasher", "reason", "no_speech", "second_audio_ref", "blob://wake/kitchen-dishwasher-second")},
			{at(17, 45, 0), presence("present")}, // dinner
			{at(18, 30, 0), presence("absent")},
		},
		"device:living_room": {
			{at(8, 58, 20), presence("present")},
			{at(9, 40, 0), presence("absent")},
			{at(18, 32, 0), presence("present")},
			{at(20, 11, 0), presence("unknown")}, // the native API dropped for two minutes
			{at(20, 13, 0), presence("present")}, // and still there when the reviewer sits down
		},
		"device:office": {
			{at(21, 15, 0), rec(journal.KindWakeRejected, "blob://wake/office-podcast", "reason", "no_speech")},
		},
	}
}

// Journal writes the day through a real journal, one append at a time, at
// the moment each event happened.
func Journal(ctx context.Context) (*journal.MemStore, error) {
	store := journal.NewMemStore()
	clk := &clock{}
	j := journal.New(store, clk, Versions())
	for id, lines := range Logs() {
		for _, l := range lines {
			clk.now = Day().Add(l.At)
			if _, err := j.Append(ctx, id, l.Rec); err != nil {
				return nil, fmt.Errorf("%s: append %s: %w", id, l.Rec.Kind, err)
			}
		}
	}
	return store, nil
}

// clock is wherever the log being written has got to.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

//go:embed voice.json
var script []byte

//go:embed audio
var audio embed.FS

// Clip is one recording the journal cites, as voice.json scripts it.
type Clip struct {
	Ref     string  `json:"ref"`     // blob ref without the blob:// scheme
	Who     string  `json:"who"`     // a household member, the assistant, or the podcast
	Say     string  `json:"say"`     // what is heard; for a cut clip, up to the cut
	Unheard string  `json:"unheard"` // what a cut clip went on to say
	Heard   int     `json:"heard_frames"`
	Seconds float64 `json:"seconds"`
	Noise   string  `json:"noise"` // a clip that is not speech
}

// DeviceRate is the sample rate of every clip: 16 kHz s16le mono (ADR-0007).
const DeviceRate = 16000

// Frames is the clip's length in device frames.
func (c Clip) Frames() int { return int(c.Seconds*DeviceRate + 0.5) }

// Clips is every recording of the day, in script order.
func Clips() ([]Clip, error) {
	var s struct{ Clips []Clip }
	if err := json.Unmarshal(script, &s); err != nil {
		return nil, fmt.Errorf("voice.json: %w", err)
	}
	return s.Clips, nil
}

// Blobs holds every clip, as chorusd's blob store would.
func Blobs(ctx context.Context) (*blob.Memory, error) {
	clips, err := Clips()
	if err != nil {
		return nil, err
	}
	m := blob.NewMemory()
	for _, c := range clips {
		pcm, err := audio.ReadFile("audio/" + c.Ref + ".pcm")
		if err != nil {
			return nil, err
		}
		w, err := m.Create(ctx, c.Ref)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", c.Ref, err)
		}
		if _, err := w.Write(pcm); err != nil {
			return nil, fmt.Errorf("write %s: %w", c.Ref, err)
		}
		if _, err := w.Commit(); err != nil {
			return nil, fmt.Errorf("commit %s: %w", c.Ref, err)
		}
	}
	return m, nil
}
