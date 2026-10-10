// Package demo builds the mock's data as ui view models: the barge-in trace,
// the room-change trace, the triage queue, the household day, the wake-word
// clips and the DPO pairs. The kit's tests render from it; an app
// replaces it with data read from the event journal.
package demo

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/teagan42/chorus/internal/reviewui/ui"
)

const ctx = "persona v14 · schema v6 · qwen3-32b"

// Header returns the app header for a step.
func Header(step ui.StepID) ui.AppHeader { return ui.NewAppHeader(step, 31, ctx) }

// peaks makes deterministic fake peak levels with speech in the given windows.
func peaks(n int, from, to float64, speech [][2]float64, floor float64, seed uint32) []float64 {
	out := make([]float64, n)
	x := seed
	for i := range out {
		x = x*1664525 + 1013904223
		r := float64(x>>8) / float64(1<<24)
		t := from + (to-from)*float64(i)/float64(n-1)
		v := floor * (0.5 + r)
		for _, w := range speech {
			if t >= w[0] && t <= w[1] {
				env := math.Pow(math.Sin(math.Pi*(t-w[0])/(w[1]-w[0])), 0.4)
				v = env * (0.45 + 0.5*r) * (0.6 + 0.4*math.Abs(math.Sin(t*9)))
			}
		}
		out[i] = v
	}
	return out
}

// ---------------------------------------------------------------- Review

// Event is one journal event in the demo trace.
type Event struct {
	ID, Seq, Label, Title, Body, Unheard, Meta string
	T                                          float64
	Tone                                       ui.Tone
}

// ReviewEvents is the barge-in trace’s journal.
var ReviewEvents = []Event{
	{"e12", "#0012", "wake ✓", "WAKE · STAGE TWO CONFIRMED", "“Hey Eddie” — rescore 0.97 ≥ 0.80, speech present, speaker Teagan 0.94.", "", "stage one: micro_wake_word on-device · pre-roll 1.0 s", 0.00, ui.ToneConv},
	{"e19", "#0019", "Teagan", "USER TURN · TEAGAN", "What’s on tomorrow, and kill the kitchen lights.", "", "endpointed by Smart Turn · speaker 0.94", 3.10, ui.TonePeople},
	{"e21", "#0021", "→ light", "TOOL_CALL · HA.LIGHT.TURN_OFF", `{"area": "kitchen"}`, "", "dispatched as JSON closed · on_interrupt: uninterruptible", 3.48, ui.ToneHome},
	{"e22", "#0022", "→ calendar", "TOOL_CALL · CALENDAR.SEARCH", `{"date": "2026-10-06"}`, "", "on_interrupt: detach · timeout 10 s", 3.49, ui.ToneHome},
	{"e24", "#0024", "speak", "SPEAK · QUEUE", "One sec.", "", "first audio 520 ms after endpoint · target 700", 3.62, ui.ToneVoice},
	{"e27", "#0027", "← light", "TOOL_RESULT · HA.LIGHT.TURN_OFF", "ok", "", "430 ms", 3.95, ui.ToneHome},
	{"e31", "#0031", "← calendar", "TOOL_RESULT · CALENDAR.SEARCH", "3 events — dentist 09:00, standup 10:30, Mouser delivery after 14:00.", "", "1.58 s", 5.10, ui.ToneHome},
	{"e35", "#0035", "speak ✂", "SPEAK · QUEUE · INTERRUPTED", "Kitchen lights are off. Tomorrow you’ve got three things: the dentist at nine, stand", "up at ten-thirty, and your parts order from Mouser arrives after two.", "heard 2.17 s of 4.95 s generated · model sees only the heard part", 5.45, ui.ToneVoice},
	{"e38", "#0038", "✕ candidate", "BARGE_CANDIDATE · REJECTED", "Speaker-ID 0.31 against Teagan — looks like the TV.", "", "logged to barge-in gate corpus", 6.10, ui.ToneMuted},
	{"e41", "#0041", "BARGE-IN", "BARGE_IN", "TTS stopped 318 ms after onset. Truncation at DAC frame 121 920 (7.620 s).", "", "calendar.search already complete · DPO pair harvested", 7.62, ui.TonePeople},
	{"e44", "#0044", "Teagan", "USER TURN · TEAGAN", "Just the first one.", "", "speaker 0.91 · same session", 8.45, ui.TonePeople},
	{"e49", "#0049", "speak", "SPEAK · QUEUE", "Dentist at nine. That’s it.", "", "completed · weak positive", 8.95, ui.ToneVoice},
	{"e52", "#0052", "end", "END_SESSION", "Model-decided close.", "", "20 s silence backstop not reached", 10.10, ui.ToneConv},
}

// ReviewAxis is 0–11 s.
var ReviewAxis = ui.Linear{From: 0, To: 11}

// FindEvent returns the event with id, or the interrupted speak event.
func FindEvent(id string) Event {
	for _, e := range ReviewEvents {
		if e.ID == id {
			return e
		}
	}
	return ReviewEvents[7]
}

// ReviewInspector renders the inspector for an event.
func ReviewInspector(id string) ui.Inspector {
	e := FindEvent(id)
	return ui.Inspector{
		Cap:     e.Seq + " · " + trimZero(e.T) + " s · " + e.Title,
		Body:    e.Body,
		Unheard: e.Unheard,
		Meta:    e.Meta,
		Foot:    "in effect: persona v14 · tool-schema v6 · qwen3-32b-awq",
	}
}

func trimZero(t float64) string { return strconv.FormatFloat(t, 'f', -1, 64) }

func sprintf2(f float64) string { return fmt.Sprintf("%.2f", f) }

func itoa(i int) string { return strconv.Itoa(i) }

// ReviewPins places the journal on the axis; inspectorURL(id) loads the inspector.
func ReviewPins(selected string, inspectorURL func(id string) string) []ui.Pin {
	var pins []ui.Pin
	for _, e := range ReviewEvents {
		pins = append(pins, ui.Pin{
			ID: e.ID, Label: e.Label, Pos: ReviewAxis.Pos(e.T), Tone: e.Tone, Selected: e.ID == selected,
			Hx: ui.Hx{Get: inspectorURL(e.ID), Target: "#review-timeline", Swap: "outerHTML"},
		})
	}
	return ui.AssignRows(pins, 1240)
}

// ReviewTimeline is the barge-in trace on the AEC’d channel.
func ReviewTimeline(selected string, inspectorURL func(string) string) ui.Timeline {
	return ReviewTimelineCh(selected, "aec", inspectorURL)
}

// Channels are the audio channels the Mic track can show.
var Channels = [][2]string{{"aec", "AEC’d"}, {"raw", "Raw"}, {"tts", "TTS out"}, {"mix", "Mix"}}

func micTrack(a ui.Linear, channel string) ui.Track {
	user := [][2]float64{{0.15, 2.9}, {7.3, 8.4}}
	tts := [][2]float64{{3.62, 4.25}, {5.45, 7.62}, {8.95, 10.05}}
	userCaps := []ui.Caption{{Pos: a.Pos(0.15), Text: "“What’s on tomorrow, and kill the kitchen lights.”", Bottom: true}, {Pos: a.Pos(7.3), Text: "“Just the first one.”", Bottom: true}}
	t := ui.Track{Name: "Mic", Kind: ui.TrackAudio, Shade: true, WaveText: "Mic waveform", Captions: userCaps}
	switch channel {
	case "raw":
		t.Sub = "raw · before AEC · 16 kHz"
		t.Wave = ui.WavePath(peaks(320, 0, 11, append(append([][2]float64{}, user...), tts...), 0.16, 9))
		t.Captions = append(userCaps, ui.Caption{Pos: a.Pos(3.7), Text: "TTS echo", Tone: ui.ToneVoice})
	case "tts":
		t.Name, t.Sub, t.WaveText = "TTS out", "what the speaker played", "TTS waveform"
		t.Wave = ui.WavePath(peaks(320, 0, 11, tts, 0.01, 5))
		t.Captions = []ui.Caption{{Pos: a.Pos(3.62), Text: "“One sec.”", Bottom: true}, {Pos: a.Pos(5.45), Text: "“Kitchen lights are off…”", Bottom: true}, {Pos: a.Pos(8.95), Text: "“Dentist at nine.”", Bottom: true}}
	case "mix":
		t.Name, t.Sub = "Mix", "mic + TTS, as heard in the room"
		t.Wave = ui.WavePath(peaks(320, 0, 11, append(append([][2]float64{}, user...), tts...), 0.05, 11))
	default:
		t.Sub = "AEC’d · 16 kHz"
		t.Wave = ui.WavePath(peaks(320, 0, 11, user, 0.05, 7))
	}
	return t
}

// ReviewTimelineCh is the barge-in trace with the Mic track on channel
// (aec, raw, tts, mix).
func ReviewTimelineCh(selected, channel string, inspectorURL func(string) string) ui.Timeline {
	a := ReviewAxis
	r := func(from, to float64, tone ui.Tone, style ui.RegionStyle, text string) ui.Region {
		return ui.Region{Left: a.Pos(from), Width: a.Pos(to) - a.Pos(from), Tone: tone, Style: style, Text: text, Wrap: true}
	}
	sel := FindEvent(selected)
	return ui.Timeline{
		ID:    "review-timeline",
		Label: "Barge-in trace",
		Ticks: a.Ticks(1, "s"),
		Tracks: []ui.Track{
			micTrack(a, channel),
			{Name: "Speech", Sub: "TTS · kokoro af_heart", Kind: ui.TrackSpeech, Regions: []ui.Region{
				r(3.62, 4.25, ui.ToneVoice, ui.RegionFill, "One sec."),
				r(5.45, 7.62, ui.ToneVoice, ui.RegionFill, "Kitchen lights are off. Tomorrow you’ve got three things: the dentist at nine, stand—"),
				r(7.62, 8.90, ui.ToneVoice, ui.RegionUnheard, "—up at ten-thirty, and your parts order… never heard"),
				r(8.95, 10.05, ui.ToneVoice, ui.RegionFill, "Dentist at nine. That’s it."),
			}},
			{Name: "Listening", Kind: ui.TrackLane, Regions: []ui.Region{r(0.15, 3.1, "", ui.RegionActivity, ""), r(7.3, 8.45, "", ui.RegionActivity, "")}},
			{Name: "Thinking", Kind: ui.TrackLane, Regions: []ui.Region{r(3.1, 3.55, "", ui.RegionActivity, ""), r(5.12, 5.4, "", ui.RegionActivity, ""), r(8.45, 8.9, "", ui.RegionActivity, "")}},
			{
				Name: "ha.light.turn_off", NameMono: true, Kind: ui.TrackLane, Regions: []ui.Region{r(3.52, 3.95, ui.ToneHome, ui.RegionFill, "")},
				Captions: []ui.Caption{{Pos: a.Pos(4.0), Text: "uninterruptible · 430 ms", Tone: ui.ToneHome}},
			},
			{
				Name: "calendar.search", NameMono: true, Kind: ui.TrackLane, Regions: []ui.Region{r(3.52, 5.1, ui.ToneHome, ui.RegionFill, "")},
				Captions: []ui.Caption{{Pos: a.Pos(5.15), Text: "detach · 3 events · 1.58 s", Tone: ui.ToneHome}},
			},
			{Name: "Speaker ID", Kind: ui.TrackLane, Regions: []ui.Region{r(0.15, 2.9, ui.TonePeople, ui.RegionOutline, "Teagan 0.94"), r(7.3, 8.4, ui.TonePeople, ui.RegionOutline, "Teagan 0.91")}},
			{Name: "Barge-in gate", Kind: ui.TrackLane, Regions: []ui.Region{r(5.96, 7.22, "", ui.RegionRejected, "✕ TV · ID 0.31"), r(7.3, 7.62, "", ui.RegionMarker, "")}},
			{Name: "Events", Sub: "journal at its moment · 13", Kind: ui.TrackEvents, Shade: true, Pins: ReviewPins(selected, inspectorURL)},
		},
		Overlays: []ui.Overlay{
			{Kind: ui.OverlayPlayhead, Pos: a.Pos(sel.T)},
			{Kind: ui.OverlayCut, Pos: a.Pos(7.62), Label: "cut 7.620 s · frame 121 920"},
		},
	}
}

// ReviewMetrics is the latency strip.
func ReviewMetrics() ui.MetricStrip {
	return ui.MetricStrip{Label: "Latency", Items: []ui.Metric{
		{Label: "Endpoint → first audio", Value: "520 ms", Note: "target 700"},
		{Label: "Barge-in onset → TTS stop", Value: "318 ms"},
		{Label: "Truncation source", Value: "DAC callback"},
		{Label: "Heard / generated", Value: "2.17 s / 4.95 s"},
	}}
}

// Labels is the annotation vocabulary.
func Labels(on map[string]bool, toggleURL func(string) string) ui.ChipGroup {
	vocab := [][2]string{{"transcript", "Transcript wrong"}, {"intent", "Misunderstood intent"}, {"tool", "Wrong tool / args"}, {"silent", "Should have spoken"}, {"verbose", "Spoke when it shouldn’t"}, {"slow", "Too slow"}, {"person", "Wrong person"}, {"exemplar", "Good — exemplar"}}
	g := ui.ChipGroup{Label: "Labels"}
	for _, v := range vocab {
		g.Chips = append(g.Chips, ui.Chip{Label: v[1], On: on[v[0]], Hx: ui.Hx{Post: toggleURL(v[0]), Target: "#labels", Swap: "outerHTML"}})
	}
	return g
}

// ---------------------------------------------------------------- Pairs

// Pairs is a fresh copy of the demo pair store.
func Pairs() map[string]*ui.Pair {
	return map[string]*ui.Pair{
		"p41": {
			ID: "p41", Source: ui.SourceBargeIn, Status: ui.PairUnreviewed,
			Rejected: "Kitchen lights are off. Tomorrow you’ve got three things: the dentist at nine, stand", RejectedUnheard: "up at ten-thirty, and your parts order from Mouser arrives after two.",
			Heard: "“Just the first one.”", AsSaid: "Dentist at nine. That’s it.", Chosen: "Dentist at nine. That’s it.",
			Suggestion: "Kitchen lights are off. Three things tomorrow — first is the dentist at nine. Want the rest?",
		},
		"p38": {
			ID: "p38", Source: ui.SourceBargeIn, Status: ui.PairUnreviewed,
			Rejected: "Saturday starts cloudy at around eight degrees, warming to fourteen by mid", RejectedUnheard: "afternoon with winds from the southwest at fifteen to twenty kilometres an hour, gusting…",
			Heard: "“No, just whether it rains.”", AsSaid: "Mostly dry — twenty percent chance after six.", Chosen: "Mostly dry — twenty percent chance after six.",
			Suggestion: "Saturday looks dry — twenty percent chance of rain after six.",
		},
		"p35": {
			ID: "p35", Source: ui.SourceAnnotation, Status: ui.PairUnreviewed,
			Rejected: `tool_call ha.light.turn_off {"area": "office_2"}`, Heard: "labelled “wrong tool / args”",
			Chosen: `tool_call ha.light.turn_off {"area": "office"}`,
		},
	}
}

// ---------------------------------------------------------------- Triage

// QueueList is the triage queue.
func QueueList() ui.List {
	t := func() *ui.ConvThumb { return ui.NewConvThumb(12) }
	return ui.List{
		ID:      "queue",
		Columns: [6]string{"Signal", "First utterance · why", "Shape · 0–12 s", "Who · where", "First audio", "When"},
		Empty:   &ui.EmptyState{Title: "Nothing matches.", Body: "Clear the search or widen the filters."},
		Rows: []ui.ListRow{
			{
				Href: "/review", Tag: ui.SigTag{Text: "barge-in pair", Tone: ui.TonePeople}, Title: "What’s on tomorrow, and kill the kitchen lights.", Detail: "cut 7.620 s → “Just the first one.”",
				Thumb: t().Add(ui.ThumbUser, 0.15, 3.1).Add(ui.ThumbSpeech, 3.62, 4.25).Add(ui.ThumbTool, 3.5, 5.1).Add(ui.ThumbSpeech, 5.45, 7.62).Add(ui.ThumbUnheard, 7.62, 10.4).Add(ui.ThumbCut, 7.62, 7.62).Add(ui.ThumbUser, 7.3, 8.45).Add(ui.ThumbSpeech, 8.95, 10.05),
				Who:   "Teagan · Living room", Figure: "520 ms", When: "23:04",
			},
			{
				Href: "/review/moved", Tag: ui.SigTag{Text: "repeated", Tone: ui.ToneConv}, Title: "Set a timer for the oven. … Set a timer for twelve minutes.", Detail: "second ask 6.2 s after first · no tool call on turn 1",
				Thumb: t().Add(ui.ThumbUser, 0.2, 2.0).Add(ui.ThumbSpeech, 2.6, 4.4).Add(ui.ThumbUser, 6.2, 8.4).Add(ui.ThumbTool, 8.9, 9.2).Add(ui.ThumbSpeech, 9.0, 10.1),
				Who:   "Alan · Kitchen", Figure: "610 ms", When: "19:41",
			},
			{
				Href: "/review", Tag: ui.SigTag{Text: "timed_out", Tone: ui.ToneHome}, Title: "Is the garage door closed?", Detail: "ha.cover.state hit 10 s · model said it couldn’t reach the door",
				Thumb: t().Add(ui.ThumbUser, 0.2, 1.4).Add(ui.ThumbTool, 1.8, 11.8).Add(ui.ThumbSpeech, 2.0, 2.5),
				Who:   "Teagan · Office", Figure: "10.4 s", FigureTone: ui.ToneVoice, When: "18:02",
			},
			{
				Href: "/review", Tag: ui.SigTag{Text: "slow audio", Tone: ui.ToneVoice}, Title: "How much filament is left on the Prusa?", Detail: "LLM prefill 690 ms · TTS first chunk 240 ms",
				Thumb: t().Add(ui.ThumbUser, 0.2, 2.1).Add(ui.ThumbTool, 2.5, 3.0).Add(ui.ThumbSpeech, 3.25, 6.0),
				Who:   "Teagan · Office", Figure: "1 140 ms", FigureTone: ui.ToneVoice, When: "17:55",
			},
			{
				Href: "/review", Tag: ui.SigTag{Text: "speaker flip", Tone: ui.TonePeople}, Title: "Add oat milk to the list.", Detail: "Teagan 0.92 → guest 0.44 mid-session · person tools gated",
				Thumb: t().Add(ui.ThumbUser, 0.2, 1.6).Add(ui.ThumbTool, 2.0, 2.4).Add(ui.ThumbSpeech, 2.2, 3.4).Add(ui.ThumbUser, 4.5, 6.0).Add(ui.ThumbSpeech, 6.6, 8.2),
				Who:   "Guest · Kitchen", Figure: "550 ms", When: "16:47",
			},
			{
				Href: "/review", Tag: ui.SigTag{Text: "confirmation", Tone: ui.ToneHome}, Title: "Unlock the front door.", Detail: "nonce n_4c1e · “yep” · speaker 0.97 · audit ok",
				Thumb: t().Add(ui.ThumbUser, 0.2, 1.4).Add(ui.ThumbSpeech, 1.9, 3.4).Add(ui.ThumbUser, 4.0, 4.6).Add(ui.ThumbTool, 5.0, 5.8).Add(ui.ThumbSpeech, 5.2, 6.2),
				Who:   "Teagan · Living room", Figure: "500 ms", When: "08:31",
			},
		},
	}
}

// FilterList keeps rows whose title or detail contains q (case-insensitive).
func FilterList(l ui.List, q string) ui.List {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return l
	}
	var rows []ui.ListRow
	for _, r := range l.Rows {
		if strings.Contains(strings.ToLower(r.Title+" "+r.Detail), q) {
			rows = append(rows, r)
		}
	}
	l.Rows = rows
	return l
}

// ---------------------------------------------------------------- Browse

// Household is Browse’s day view.
func Household() ui.DayLanes {
	s := func(h float64, flag ui.Tone) ui.Session {
		return ui.Session{Hour: h, Flag: flag, Href: "/review", Label: "session"}
	}
	return ui.DayLanes{
		From: 6, To: 24,
		Lanes: []ui.DayLane{
			{
				Name: "Living room", Sub: "Satellite1 · 7 sessions", Presence: [][2]float64{{7, 9}, {12, 12.5}, {17, 23.95}},
				Sessions: []ui.Session{s(7.52, ""), s(8.52, ""), s(12.1, ""), s(17.33, ui.TonePeople), {Hour: 19.72, Flag: ui.ToneVoice, Href: "/review/moved", Label: "19:43 resumed from kitchen"}, s(21.4, ""), s(23.07, ui.TonePeople)},
				Rejects:  []float64{20.1, 20.3, 20.36, 21.02},
			},
			{
				Name: "Kitchen", Sub: "Voice PE · 6 sessions", Presence: [][2]float64{{6.9, 7.6}, {16.5, 19.8}},
				Sessions: []ui.Session{s(7.1, ""), s(7.4, ""), s(16.78, ui.TonePeople), s(18.5, ""), {Hour: 19.68, Flag: ui.ToneVoice, Href: "/review/moved", Label: "19:41 moved to living room"}},
				Rejects:  []float64{7.3, 18.6},
			},
			{
				Name: "Office", Sub: "Satellite1 · 6 sessions", Presence: [][2]float64{{9, 18.2}},
				Sessions: []ui.Session{s(9.2, ""), s(10.5, ""), s(14.22, ui.ToneVoice), s(15.9, ""), s(17.92, ui.ToneVoice), s(18.03, ui.ToneHome)},
				Rejects:  []float64{14.9},
			},
			{
				Name: "Bedroom", Sub: "Voice PE · 2 sessions", Presence: [][2]float64{{6.5, 7}, {22.3, 24}},
				Sessions: []ui.Session{s(6.75, ""), s(22.6, "")},
			},
		},
		Migrations: []ui.Migration{{Hour: 19.71, FromLane: 1, ToLane: 0, Label: "Alan moved rooms · same conversation"}},
		Legend: &ui.Legend{Items: []ui.LegendItem{
			{Label: "conversation", Shape: "block", Tone: ui.ToneConv},
			{Label: "barge-in", Shape: "flag", Tone: ui.TonePeople},
			{Label: "tool failure", Shape: "flag", Tone: ui.ToneHome},
			{Label: "slow or repeated", Shape: "flag", Tone: ui.ToneVoice},
			{Label: "wake rejected at stage two", Shape: "tick"},
			{Label: "room occupied (mmWave)", Shape: "presence"},
		}},
	}
}

// ---------------------------------------------------------------- Negatives

// Clips is the hard-negatives grid.
func Clips(marks map[string]string, markURL func(id, mark string) string) ui.ClipGrid {
	type c struct {
		id, title, where string
		rescore, speaker float64
		speech           bool
		kind             string
		seed             uint32
	}
	src := []c{
		{"w1", "“Hey Eddie” — Alan, hoarse", "Kitchen · 07:18", 0.84, 0.52, true, "speech", 11},
		{"w2", "TV ad — “Hey, Eddie!”", "Living room · 20:06", 0.86, 0.12, true, "tv", 4},
		{"w3", "Guest says the wake word", "Living room · 19:44", 0.91, 0.33, true, "speech", 23},
		{"w5", "Laugh", "Living room · 21:01", 0.62, 0.66, true, "burst", 31},
		{"w4", "Cough", "Office · 14:52", 0.41, 0.71, false, "burst", 7},
		{"w7", "Door slam", "Kitchen · 07:20", 0.22, 0.05, false, "thud", 3},
	}
	g := ui.ClipGrid{ID: "clips", Empty: &ui.EmptyState{Title: "Nothing reviewed yet.", Body: "Mark clips in Split decisions and they collect here."}}
	for _, s := range src {
		var pk []float64
		switch s.kind {
		case "speech":
			pk = peaks(32, 0, 1, [][2]float64{{0.18, 0.8}}, 0.06, s.seed)
		case "tv":
			pk = peaks(32, 0, 1, nil, 0.45, s.seed)
		case "burst":
			pk = peaks(32, 0, 1, [][2]float64{{0.2, 0.45}}, 0.05, s.seed)
		default:
			pk = peaks(32, 0, 1, [][2]float64{{0.1, 0.25}}, 0.03, s.seed)
		}
		passes := 0
		for _, b := range []bool{s.rescore >= 0.8, s.speech, s.speaker >= 0.6} {
			if b {
				passes++
			}
		}
		verdict := ui.SigTag{Text: itoa(passes) + " of 3 passed", Tone: ui.ToneVoice}
		if !s.speech && s.rescore < 0.8 {
			verdict = ui.SigTag{Text: "auto negative", Tone: ui.ToneMuted}
		}
		m := marks[s.id]
		if m == "wake" {
			verdict = ui.SigTag{Text: "false reject", Tone: ui.ToneVoice}
		} else if m == "neg" {
			verdict = ui.SigTag{Text: "negative", Tone: ui.ToneMuted}
		}
		g.Tiles = append(g.Tiles, ui.ClipTile{
			ID: s.id, Title: s.title, Where: s.where, Peaks: pk, Verdict: verdict, Reviewed: m != "",
			Checks: []ui.Check{
				{Text: "rescore " + sprintf2(s.rescore), On: s.rescore >= 0.8},
				{Text: map[bool]string{true: "speech", false: "no speech"}[s.speech], On: s.speech},
				{Text: "speaker " + sprintf2(s.speaker), On: s.speaker >= 0.6},
			},
			MarkedNeg: m == "neg", MarkedWake: m == "wake",
			NegHx:  ui.Hx{Post: markURL(s.id, "neg"), Target: "#clip-" + s.id, Swap: "outerHTML"},
			WakeHx: ui.Hx{Post: markURL(s.id, "wake"), Target: "#clip-" + s.id, Swap: "outerHTML"},
		})
	}
	return g
}

// ---------------------------------------------------------------- Replay

// PromptDiff is persona v14 → v15-draft.
func PromptDiff() ui.CodeDiff {
	return ui.CodeDiff{Label: "Prompt diff", Lines: []ui.DiffLine{
		{Kind: " ", Text: "## Lists"},
		{Kind: "-", Text: "Read every item, then summarize."},
		{Kind: "+", Text: "Lead with the count. Read the first item,"},
		{Kind: "+", Text: "then offer the rest. Stop if interrupted."},
	}}
}

// ReplayMetrics is the outcome strip.
func ReplayMetrics() ui.MetricStrip {
	return ui.MetricStrip{Label: "Outcome", Items: []ui.Metric{
		{Label: "Speech generated", From: "4.95", Value: "2.40 s", Tone: ui.ToneVoice},
		{Label: "Recorded barge-in", Value: "lands after speech"},
		{Label: "Tool calls", Value: "identical"},
		{Label: "First audio", From: "520", Value: "535 ms"},
		{Label: "Validity", Value: "turn 2 re-timed", Tone: ui.ToneVoice},
	}}
}

// ---------------------------------------------------------------- Room change

// MovedAxis collapses the 1 m 32 s between rooms.
var MovedAxis = ui.Gapped{Spans: []ui.Span{{From: 0, To: 12, Start: 0, Width: 52}, {From: 100, To: 116, Start: 60, Width: 40}}}

// MovedTimeline is the room-change trace: two device tracks, one conversation.
func MovedTimeline() ui.Timeline {
	a := MovedAxis
	r := func(from, to float64, tone ui.Tone, style ui.RegionStyle, text string) ui.Region {
		return ui.Region{Left: a.Pos(from), Width: a.Pos(to) - a.Pos(from), Tone: tone, Style: style, Text: text}
	}
	pins := []ui.Pin{
		{ID: "e1", Label: "wake ✓ kitchen", Pos: a.Pos(0), Tone: ui.ToneConv},
		{ID: "e2", Label: "Alan", Pos: a.Pos(1.2), Tone: ui.TonePeople},
		{ID: "e3", Label: "speak", Pos: a.Pos(2.9), Tone: ui.ToneVoice},
		{ID: "e4", Label: "Alan", Pos: a.Pos(6.2), Tone: ui.TonePeople},
		{ID: "e5", Label: "→ timer.start", Pos: a.Pos(7.6), Tone: ui.ToneHome},
		{ID: "e6", Label: "← t_12", Pos: a.Pos(7.9), Tone: ui.ToneHome},
		{ID: "e7", Label: "speak", Pos: a.Pos(8.1), Tone: ui.ToneVoice},
		{ID: "e8", Label: "end_session · held", Pos: a.Pos(10.4), Tone: ui.ToneConv},
		{ID: "e9", Label: "wake ✓ resumed", Pos: a.Pos(102), Tone: ui.ToneConv, Selected: true},
		{ID: "e10", Label: "Alan", Pos: a.Pos(103), Tone: ui.TonePeople},
		{ID: "e11", Label: "→ timer.status", Pos: a.Pos(104.6), Tone: ui.ToneHome},
		{ID: "e12", Label: "← 10:23", Pos: a.Pos(104.8), Tone: ui.ToneHome},
		{ID: "e13", Label: "speak", Pos: a.Pos(105.1), Tone: ui.ToneVoice},
		{ID: "e14", Label: "✕ TV", Pos: a.Pos(108.5), Tone: ui.ToneMuted},
		{ID: "e15", Label: "end", Pos: a.Pos(113), Tone: ui.ToneConv},
	}
	ticks := []ui.Tick{
		{Pos: a.Pos(0), Label: "0"},
		{Pos: a.Pos(4), Label: "4"},
		{Pos: a.Pos(8), Label: "8 s"},
		{Pos: a.Pos(100), Label: "1:40"},
		{Pos: a.Pos(104), Label: "1:44"},
		{Pos: a.Pos(108), Label: "1:48"},
		{Pos: a.Pos(112), Label: "1:52"},
	}
	ov := a.Gaps()
	ov[0].Label = "held 1:32" // session closed at 10.4 s, resumed at 102 s
	ov = append(ov, ui.Overlay{Kind: ui.OverlayPlayhead, Pos: a.Pos(102)})
	return ui.Timeline{
		Label: "Room-change trace", Ticks: ticks, Overlays: ov,
		Tracks: []ui.Track{
			{
				Name: "Kitchen", Sub: "Voice PE · AEC’d", Kind: ui.TrackAudio, Height: 84, Shade: true,
				Bars:     ui.BarsOver(a, 0, 12, peaks(80, 0, 12, [][2]float64{{1.2, 2.6}, {6.2, 7.4}}, 0.06, 7)),
				Captions: []ui.Caption{{Pos: 62, Text: "stream closed · nobody here", Tone: ui.ToneMuted}},
			},
			{
				Name: "Living room", Sub: "Satellite1 · TV on", Kind: ui.TrackAudio, Height: 84, Shade: true,
				Bars:     ui.BarsOver(a, 100, 116, peaks(106, 100, 116, [][2]float64{{103, 104.2}}, 0.2, 13)),
				Captions: []ui.Caption{{Pos: 2, Text: "idle · TV audio stays on the device", Tone: ui.ToneMuted}},
			},
			{Name: "Conversation · Alan", Kind: ui.TrackGroup, Tone: ui.ToneConv},
			{
				Name: "Speech", Sub: "plays where he is", Kind: ui.TrackSpeech, Height: 92,
				Regions: []ui.Region{compact(r(2.9, 4.1, ui.ToneVoice, ui.RegionFill, "")), compact(r(8.1, 10.0, ui.ToneVoice, ui.RegionFill, "")), compact(r(105.1, 107, ui.ToneVoice, ui.RegionFill, ""))},
				Captions: []ui.Caption{
					{Pos: a.Pos(2.9), Text: "“Sure — how long?” · kitchen", Bottom: true, Quote: true},
					{Pos: a.Pos(8.1), Text: "“Twelve minutes, starting now.” · kitchen", Bottom: true, Quote: true},
					{Pos: a.Pos(105.1), Text: "“Ten minutes twenty left.” · living room", Bottom: true, Quote: true},
				},
			},
			{
				Name: "timer.*", NameMono: true, Kind: ui.TrackLane,
				Regions:  []ui.Region{r(7.6, 7.9, ui.ToneHome, ui.RegionFill, ""), r(104.6, 104.8, ui.ToneHome, ui.RegionFill, "")},
				Captions: []ui.Caption{{Pos: a.Pos(7.95), Text: "start t_12 · 12:00 · “oven”", Tone: ui.ToneHome}, {Pos: a.Pos(105.2), Text: "status t_12 · 10:23", Tone: ui.ToneHome}},
			},
			{Name: "Speaker ID", Kind: ui.TrackLane, Regions: []ui.Region{r(1.2, 2.6, ui.TonePeople, ui.RegionOutline, "Alan 0.90"), r(6.2, 7.4, ui.TonePeople, ui.RegionOutline, "Alan 0.91"), r(101.6, 104.2, ui.TonePeople, ui.RegionOutline, "Alan 0.88")}},
			{Name: "Barge-in gate", Kind: ui.TrackLane, Regions: []ui.Region{r(108.2, 109.9, "", ui.RegionRejected, "✕ TV")}},
			{Name: "Events", Sub: "one journal, both rooms · 15", Kind: ui.TrackEvents, Shade: true, Pins: ui.AssignRows(pins, 1240)},
		},
	}
}

func compact(r ui.Region) ui.Region { r.Compact = true; return r }

// MovedHandoff is the hand-off strip.
func MovedHandoff() ui.MetricStrip {
	return ui.MetricStrip{Label: "Hand-off", Items: []ui.Metric{
		{Label: "Gap · resume window", Value: "1 m 32 s of 2 m", Meter: 0.76},
		{Label: "Resumed because", Value: "Alan 0.88 ≥ 0.75", Tone: ui.TonePeople, Note: "same person, inside the window"},
		{Label: "Context carried", Value: "4 turns · timer t_12", Note: "“that” resolved to the oven timer"},
		{Label: "Audio", Value: "Voice PE → Satellite1", Note: "kitchen stream closed at 10.4 s"},
	}}
}
