// Package household is one made-up household's Thursday, 9 October 2025, as
// chorusd would have journaled it: three people, three satellites, every
// signal Triage knows, a timer going off, and one conversation from the night
// before. The review UI's browser tests walk a reviewer through this day, and
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
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/journal"
)

// The conversations, named for what happens in them.
const (
	ConvWeather  = "conv-0705-kitchen"     // Teagan cuts the forecast off: barge-in
	ConvZeppelin = "conv-0853-kitchen"     // Alice's album, then she walks to the living room
	ConvGarage   = "conv-0930-office"      // the garage sensor times out: failure
	ConvTimer    = "conv-1210-kitchen"     // Alan asks for the oven timer twice: repeated
	ConvOven     = "conv-1222-kitchen"     // the oven timer goes off: an announcement
	ConvJazz     = "conv-1840-living_room" // Alice dims, Alan asks for jazz and cuts it: flip, unattributed barge-in
	ConvList     = "conv-2104-office"      // nothing wrong at all
	ConvLock     = "conv-2230-kitchen"     // the night before
)

// OvenTimer is the timer Alan set for the oven.
const OvenTimer = "t_0a7e11c3"

// The harvested pairs, by the seq of each cut.
const (
	PairWeather  = ConvWeather + "/7"
	PairZeppelin = ConvZeppelin + "/7"
	PairJazz     = ConvJazz + "/14"
)

// Day is midnight of the day under review.
func Day() time.Time { return time.Date(2025, time.October, 9, 0, 0, 0, 0, time.UTC) }

// ReviewedAt is when the reviewer sits down: that evening.
func ReviewedAt() time.Time { return Day().Add(22*time.Hour + 30*time.Minute) }

// Versions is what every turn of the day ran under.
func Versions() journal.Versions {
	return journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"}
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

func at(h, m int, s float64) time.Duration {
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s*float64(time.Second))
}

const speakArgs = `{"mode":"queue","streamed":true}`

// Logs is every conversation's log, keyed by conversation id.
func Logs() map[string][]Line {
	return map[string][]Line{
		ConvWeather: {
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
		ConvZeppelin: {
			{at(8, 53, 20), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "alice", "resumed", "false")},
			{at(8, 53, 20.3), rec(journal.KindUtteranceTranscribed, "blob://mic/zeppelin-ask", "text", "play something by zeppelin", "speaker_id", "alice")},
			{at(8, 53, 21.2), rec(journal.KindToolCalled, "", "tool", "media_search", "call_id", "c1", "args_json", `{"query":"Led Zeppelin","media_type":"album","limit":5}`)},
			{at(8, 53, 21.9), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok", "result_json", `{"results":["Led Zeppelin","Led Zeppelin II","Led Zeppelin IV"]}`)},
			{at(8, 53, 22.3), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(8, 53, 23.1), rec(journal.KindBargeInDetected, "blob://mic/zeppelin-bargein", "tts_position_ms", "800")},
			{at(8, 53, 23.2), rec(journal.KindSpeechTruncated, "blob://tts/zeppelin-list", "spoken_text", "I found three", "unspoken_text", " albums by that artist: Led Zeppelin, Led Zeppelin II and Led Zeppelin IV.", "frames_played", "12800")},
			{at(8, 53, 23.2), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "cancelled")},
			{at(8, 53, 23.3), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
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
		ConvGarage: {
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
		ConvTimer: {
			{at(12, 10, 0), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "alan", "resumed", "false")},
			{at(12, 10, 0.2), rec(journal.KindUtteranceTranscribed, "blob://mic/timer-oven", "text", "set a timer for the oven", "speaker_id", "alan")},
			{at(12, 10, 2.6), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", speakArgs)},
			{at(12, 10, 4.4), rec(journal.KindSpeechSpoken, "blob://tts/timer-howlong", "text", "Sure, how long?", "frames_played", "16000")},
			{at(12, 10, 4.5), rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok")},
			{at(12, 10, 4.6), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(12, 10, 6.4), rec(journal.KindUtteranceTranscribed, "blob://mic/timer-twelve", "text", "set a timer for twelve minutes", "speaker_id", "alan")},
			{at(12, 10, 6.9), rec(journal.KindToolCalled, "", "tool", "timer_start", "call_id", "c1", "args_json", `{"seconds":720,"label":"oven"}`)},
			{at(12, 10, 7.2), rec(journal.KindToolResult, "", "call_id", "c1", "outcome", "ok", "result_json", `{"timer_id":"`+OvenTimer+`","label":"oven","satellite":"kitchen","seconds_left":720,"says":"The oven timer is done."}`)},
			{at(12, 10, 7.4), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", speakArgs)},
			{at(12, 10, 9.0), rec(journal.KindSpeechSpoken, "blob://tts/timer-started", "text", "Twelve minute oven timer started.", "frames_played", "24000")},
			{at(12, 10, 9.1), rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "ok")},
			{at(12, 10, 9.2), rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop")},
			{at(12, 10, 14), rec(journal.KindSessionClosed, "", "reason", "model_ended", "satellite", "kitchen")},
		},
		// Twelve minutes on, with nobody talking to it, the kitchen says so.
		ConvOven: {
			{at(12, 22, 7.1), rec(journal.KindSessionOpened, "", "satellite", "kitchen", "announced", "true", "resumed", "false")},
			{at(12, 22, 7.1), rec(journal.KindAnnouncementMade, "", "text", "The oven timer is done.", "call_id", "an_5c19e2d0", "source", "timer", "timer_id", OvenTimer, "requested_by", "alan", "from_satellite", "kitchen", "from_conversation", ConvTimer, "start_conversation", "false")},
			{at(12, 22, 7.1), rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "an_5c19e2d0", "args_json", `{"text":"The oven timer is done.","mode":"queue"}`)},
			{at(12, 22, 8.6), rec(journal.KindSpeechSpoken, "blob://tts/oven-done", "text", "The oven timer is done.", "frames_played", "24000")},
			{at(12, 22, 8.6), rec(journal.KindToolResult, "", "call_id", "an_5c19e2d0", "outcome", "ok")},
			{at(12, 22, 8.7), rec(journal.KindSessionClosed, "", "reason", "announced", "satellite", "kitchen")},
		},
		// Timers belong to the house, not to the conversation that set them.
		journal.HouseTimers: {
			{at(12, 10, 7.1), rec(journal.KindTimerStarted, "", "timer_id", OvenTimer, "seconds", "720", "fires_at", Day().Add(at(12, 22, 7.1)).Format(time.RFC3339Nano), "label", "oven", "satellite", "kitchen", "person", "alan", "conversation_id", ConvTimer, "call_id", "c1")},
			{at(12, 22, 8.7), rec(journal.KindTimerFinished, "", "timer_id", OvenTimer, "outcome", "announced", "conversation_id", ConvOven)},
		},
		ConvJazz: {
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
			{at(18, 40, 7.3), rec(journal.KindBargeInDetected, "blob://mic/jazz-bargein", "tts_position_ms", "1100")},
			{at(18, 40, 7.4), rec(journal.KindSpeechTruncated, "blob://tts/jazz-playlist", "spoken_text", "Playing Late Night Jazz", "unspoken_text", " from Spotify, starting with Take Five.", "frames_played", "17600")},
			{at(18, 40, 7.4), rec(journal.KindSpeechDiscarded, "", "unspoken_text", "Say skip to hear the next one.", "reason", "barge_in")},
			{at(18, 40, 7.4), rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "cancelled")},
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
		ConvList: {
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
		ConvLock: {
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
