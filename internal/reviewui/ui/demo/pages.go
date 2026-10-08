package demo

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/teaganglenn/chorus/internal/reviewui/ui"
)

// ---------------------------------------------------------------- Triage

// QueueItem is a triage row plus the fields the filters need.
type QueueItem struct {
	Conv   string // conversation id; reviewing it removes it from the queue
	Kind   string // barge, repeat, fail, slow, flip, confirm
	Person string
	Room   string
	Row    ui.ListRow
}

// QueueItems is the triage queue with filter metadata.
func QueueItems() []QueueItem {
	rows := QueueList().Rows
	meta := []struct{ conv, kind, person, room string }{
		{"c_7f3a91e2", "barge", "Teagan", "Living room"},
		{"c_4b2e19d0", "repeat", "Alan", "Kitchen"},
		{"c_88d1e0a4", "fail", "Teagan", "Office"},
		{"c_19ac77b2", "slow", "Teagan", "Office"},
		{"c_2f60b1c9", "flip", "Guest", "Kitchen"},
		{"c_0d5e4a13", "confirm", "Teagan", "Living room"},
	}
	out := make([]QueueItem, len(rows))
	for i, r := range rows {
		out[i] = QueueItem{Conv: meta[i].conv, Kind: meta[i].kind, Person: meta[i].person, Room: meta[i].room, Row: r}
	}
	return out
}

// QueueTabs are the signal filters: id, label, kinds included.
var QueueTabs = []struct {
	ID, Label string
	Kinds     []string
}{
	{"all", "All", nil},
	{"barge", "Barge-in pairs", []string{"barge"}},
	{"repeat", "Repeated", []string{"repeat"}},
	{"fail", "Failures", []string{"fail"}},
	{"slow", "Slow", []string{"slow"}},
	{"people", "Speaker flips", []string{"flip"}},
}

// QueueFilter is the triage filter state.
type QueueFilter struct {
	Tab, Q, Person string
	Reviewed       map[string]bool
}

// Match reports whether an item passes the filter (ignoring the tab when ignoreTab).
func (f QueueFilter) Match(it QueueItem, ignoreTab bool) bool {
	if f.Reviewed[it.Conv] {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Q)); q != "" && !strings.Contains(strings.ToLower(it.Row.Title+" "+it.Row.Detail), q) {
		return false
	}
	if f.Person != "" && f.Person != "Anyone" && it.Person != f.Person {
		return false
	}
	if ignoreTab || f.Tab == "" || f.Tab == "all" {
		return true
	}
	for _, t := range QueueTabs {
		if t.ID == f.Tab {
			for _, k := range t.Kinds {
				if k == it.Kind {
					return true
				}
			}
		}
	}
	return false
}

// ---------------------------------------------------------------- Room change

// MovedEvents is the room-change journal.
var MovedEvents = []Event{
	{"e1", "#0002", "wake ✓ kitchen", "WAKE · NEW CONVERSATION", "“Hey Eddie” on the kitchen Voice PE. Stage two confirmed, speaker Alan 0.90.", "", "no open conversation for Alan · c_4b2e19d0 created", 0, ui.ToneConv},
	{"e2", "#0005", "Alan", "USER TURN · ALAN", "Set a timer for the oven.", "", "speaker 0.90", 1.2, ui.TonePeople},
	{"e3", "#0008", "speak", "SPEAK · QUEUE", "Sure — how long?", "", "asked instead of guessing a duration · flagged “repeated” by triage", 2.9, ui.ToneVoice},
	{"e4", "#0011", "Alan", "USER TURN · ALAN", "Set a timer for twelve minutes.", "", "restated the whole request, not just “twelve”", 6.2, ui.TonePeople},
	{"e5", "#0013", "→ timer.start", "TOOL_CALL · TIMER.START", `{"duration": "12m", "label": "oven"}`, "", "on_interrupt: uninterruptible", 7.6, ui.ToneHome},
	{"e6", "#0014", "← t_12", "TOOL_RESULT · TIMER.START", "ok · id t_12 · fires 19:53:09", "", "290 ms", 7.9, ui.ToneHome},
	{"e7", "#0016", "speak", "SPEAK · QUEUE", "Twelve minutes, starting now.", "", "completed", 8.1, ui.ToneVoice},
	{"e8", "#0019", "end_session · held", "END_SESSION · CONVERSATION HELD", "Model closed the session. The kitchen audio stream ends; the conversation stays open for Alan for two minutes.", "", "resume window 2:00 · keyed on person, not device", 10.4, ui.ToneConv},
	{"e9", "#0031", "wake ✓ resumed", "WAKE · RESUMED c_4b2e19d0", "“Hey Eddie” on the living-room Satellite1, 1 m 32 s after the kitchen closed. Speaker Alan 0.88 matched the held conversation, so it resumed instead of starting fresh.", "", "threshold 0.75 · window 2:00 · TV audio present, stage two still confirmed", 102, ui.ToneConv},
	{"e10", "#0033", "Alan", "USER TURN · ALAN", "How long’s left on that?", "", "“that” only means something because the context came with him", 103, ui.TonePeople},
	{"e11", "#0035", "→ timer.status", "TOOL_CALL · TIMER.STATUS", `{"id": "t_12"}`, "", "id taken from the carried state, not a search", 104.6, ui.ToneHome},
	{"e12", "#0036", "← 10:23", "TOOL_RESULT · TIMER.STATUS", "10 m 23 s remaining", "", "80 ms", 104.8, ui.ToneHome},
	{"e13", "#0038", "speak", "SPEAK · QUEUE", "Ten minutes twenty left.", "", "played on Satellite1 only", 105.1, ui.ToneVoice},
	{"e14", "#0040", "✕ TV", "BARGE_CANDIDATE · REJECTED", "TV dialogue crossed the energy gate; speaker-ID 0.18 against Alan.", "", "logged to gate corpus", 108.5, ui.ToneMuted},
	{"e15", "#0043", "end", "END_SESSION", "Model-decided close. Conversation held again for two minutes, then archived.", "", "timer t_12 keeps running — it belongs to the house, not the session", 113, ui.ToneConv},
}

// MovedInspector renders the inspector for a room-change event.
func MovedInspector(id string) ui.Inspector {
	e := MovedEvents[8]
	for _, x := range MovedEvents {
		if x.ID == id {
			e = x
		}
	}
	room := "kitchen"
	at := trimZero(e.T) + " s"
	if e.T >= 100 {
		room, at = "living room", "+"+ui.Clock(e.T)
	}
	return ui.Inspector{Cap: e.Seq + " · " + at + " · " + e.Title, Body: e.Body, Meta: e.Meta, Foot: "heard on: " + room}
}

// MovedTimelineFor draws the room change with the gap skipped ("skip") or at
// real time ("real"), with selected highlighted and pins wired to pinURL.
func MovedTimelineFor(mode, selected string, pinURL func(string) string) ui.Timeline {
	t := MovedTimeline()
	t.ID = "moved-timeline"
	var a ui.Axis = MovedAxis
	if mode == "real" {
		a = ui.Linear{From: 0, To: 116}
	}
	sel := MovedEvents[8]
	for _, e := range MovedEvents {
		if e.ID == selected {
			sel = e
		}
	}
	var pins []ui.Pin
	for _, e := range MovedEvents {
		pins = append(pins, ui.Pin{
			ID: e.ID, Label: e.Label, Pos: a.Pos(e.T), Tone: e.Tone, Selected: e.ID == sel.ID,
			Hx: ui.Hx{Get: pinURL(e.ID), Target: "#moved-timeline", Swap: "outerHTML"},
		})
	}
	t.Tracks[len(t.Tracks)-1].Pins = ui.AssignRows(pins, 1240)
	if mode == "real" {
		// Re-place everything on a linear axis: the gap takes its real share.
		lin := a.(ui.Linear)
		re := func(tr *ui.Track, spans [][2]float64) {
			for i := range tr.Regions {
				if i < len(spans) {
					tr.Regions[i].Left = lin.Pos(spans[i][0])
					tr.Regions[i].Width = lin.Pos(spans[i][1]) - lin.Pos(spans[i][0])
				}
			}
		}
		t.Tracks[0].Bars = ui.BarsOver(lin, 0, 12, peaks(40, 0, 12, [][2]float64{{1.2, 2.6}, {6.2, 7.4}}, 0.06, 7))
		t.Tracks[0].Captions = []ui.Caption{{Pos: 30, Text: "stream closed at 10.4 s", Tone: ui.ToneMuted}}
		t.Tracks[1].Bars = ui.BarsOver(lin, 100, 116, peaks(60, 100, 116, [][2]float64{{103, 104.2}}, 0.2, 13))
		t.Tracks[1].Captions = []ui.Caption{{Pos: 2, Text: "idle · TV audio stays on the device", Tone: ui.ToneMuted}}
		re(&t.Tracks[3], [][2]float64{{2.9, 4.1}, {8.1, 10.0}, {105.1, 107}})
		t.Tracks[3].Captions = []ui.Caption{{Pos: lin.Pos(2.9), Text: "kitchen ×2", Bottom: true, Quote: true}, {Pos: lin.Pos(105.1) - 9, Text: "living room", Bottom: true, Quote: true}}
		re(&t.Tracks[4], [][2]float64{{7.6, 7.9}, {104.6, 104.8}})
		t.Tracks[4].Captions = []ui.Caption{{Pos: lin.Pos(8.2), Text: "start t_12", Tone: ui.ToneHome}, {Pos: lin.Pos(104.9) - 9, Text: "status", Tone: ui.ToneHome}}
		re(&t.Tracks[5], [][2]float64{{1.2, 2.6}, {6.2, 7.4}, {101.6, 104.2}})
		for i := range t.Tracks[5].Regions {
			t.Tracks[5].Regions[i].Text = ""
		}
		re(&t.Tracks[6], [][2]float64{{108.2, 109.9}})
		t.Tracks[6].Regions[0].Text = ""
		t.Ticks = lin.Ticks(20, "")
		for i := range t.Ticks {
			t.Ticks[i].Label = ui.Clock(float64(i * 20))
		}
		t.Overlays = []ui.Overlay{{Kind: ui.OverlayMarker, Pos: lin.Pos(10.4), Label: "held"}, {Kind: ui.OverlayMarker, Pos: lin.Pos(102), Label: "resumed · 1:32 later"}}
	} else {
		t.Overlays = append(MovedAxis.Gaps(), ui.Overlay{Kind: ui.OverlayPlayhead, Pos: a.Pos(sel.T)})
		t.Overlays[0].Label = "held 1:32"
	}
	if mode == "real" {
		t.Overlays = append(t.Overlays, ui.Overlay{Kind: ui.OverlayPlayhead, Pos: a.Pos(sel.T)})
	}
	return t
}

// ---------------------------------------------------------------- Browse

// Day is one day of household data.
type Day struct {
	Label string
	Lanes ui.DayLanes
	Rows  []QueueItem
}

// Days returns Sun 4, Mon 5 and Tue 6 Oct (today, morning only). Index 1 is Mon.
func Days() []Day {
	mon := Household()
	monRows := QueueItems()

	sun := Household()
	keep := func(lane *ui.DayLane, hours ...float64) {
		var out []ui.Session
		for _, h := range hours {
			out = append(out, ui.Session{Hour: h, Href: "/review", Label: "session"})
		}
		lane.Sessions = out
	}
	keep(&sun.Lanes[0], 9.4, 11.2, 18.9, 20.5)
	sun.Lanes[0].Sessions[2].Flag = ui.TonePeople
	sun.Lanes[0].Rejects = []float64{20.7, 21.4}
	keep(&sun.Lanes[1], 8.1, 13.3, 19.0)
	sun.Lanes[1].Sessions[0].Flag = ui.TonePeople
	keep(&sun.Lanes[2], 15.2, 16.0)
	keep(&sun.Lanes[3], 7.1, 23.2)
	sun.Migrations = nil
	for i, s := range []string{"Satellite1 · 4 sessions", "Voice PE · 3 sessions", "Satellite1 · 2 sessions", "Voice PE · 2 sessions"} {
		sun.Lanes[i].Sub = s
	}
	t := func() *ui.ConvThumb { return ui.NewConvThumb(12) }
	sunRows := []QueueItem{
		{Conv: "c_3b7e2210", Kind: "barge", Person: "Guest", Room: "Living room", Row: ui.ListRow{Href: "/curate/pairs?pair=p38", Tag: ui.SigTag{Text: "barge-in", Tone: ui.TonePeople}, Title: "Play something chill.", Detail: "barge-in was the TV — pair discarded", Thumb: t().Add(ui.ThumbUser, 0.2, 1.2).Add(ui.ThumbSpeech, 1.5, 2.4).Add(ui.ThumbCut, 1.6, 1.6), Who: "Guest · Living room", Figure: "470 ms", When: "18:54"}},
		{Conv: "c_9de0a1f3", Kind: "barge", Person: "Alan", Room: "Kitchen", Row: ui.ListRow{Href: "/curate/pairs", Tag: ui.SigTag{Text: "barge-in", Tone: ui.TonePeople}, Title: "Did the dishwasher finish?", Detail: "cut 4.400 s → “Yeah, okay, thanks.”", Thumb: t().Add(ui.ThumbUser, 0.2, 1.3).Add(ui.ThumbTool, 1.6, 1.9).Add(ui.ThumbSpeech, 2.0, 4.4).Add(ui.ThumbCut, 4.4, 4.4), Who: "Alan · Kitchen", Figure: "540 ms", When: "08:06"}},
		{Conv: "c_7aa01d55", Kind: "slow", Person: "Teagan", Room: "Office", Row: ui.ListRow{Href: "/review", Tag: ui.SigTag{Text: "slow audio", Tone: ui.ToneVoice}, Title: "What’s left on the print queue?", Detail: "replayed under v14 · promoted", Thumb: t().Add(ui.ThumbUser, 0.2, 1.6).Add(ui.ThumbTool, 2.0, 2.4).Add(ui.ThumbSpeech, 2.9, 5.6), Who: "Teagan · Office", Figure: "910 ms", FigureTone: ui.ToneVoice, When: "15:12"}},
	}

	tue := Household()
	keep(&tue.Lanes[0])
	tue.Lanes[0].Rejects = nil
	tue.Lanes[0].Presence = [][2]float64{{6.9, 7.6}}
	keep(&tue.Lanes[1], 7.25)
	tue.Lanes[1].Rejects = nil
	tue.Lanes[1].Presence = [][2]float64{{7.0, 7.8}}
	keep(&tue.Lanes[2])
	tue.Lanes[2].Rejects, tue.Lanes[2].Presence = nil, nil
	keep(&tue.Lanes[3], 6.68)
	tue.Lanes[3].Presence = [][2]float64{{6.5, 7.0}}
	tue.Migrations = nil
	for i, s := range []string{"Satellite1 · 0 sessions", "Voice PE · 1 session", "Satellite1 · 0 sessions", "Voice PE · 1 session"} {
		tue.Lanes[i].Sub = s
	}
	tueRows := []QueueItem{
		{Conv: "c_e1f0a2b3", Kind: "", Person: "Alan", Room: "Kitchen", Row: ui.ListRow{Href: "/review", Title: "Will it rain this afternoon?", Detail: "clean · weak positive", Thumb: t().Add(ui.ThumbUser, 0.2, 1.4).Add(ui.ThumbTool, 1.7, 2.0).Add(ui.ThumbSpeech, 1.9, 3.1), Who: "Alan · Kitchen", Figure: "480 ms", When: "07:15"}},
		{Conv: "c_e1f0a2b4", Kind: "", Person: "Alan", Room: "Bedroom", Row: ui.ListRow{Href: "/review", Title: "Stop the alarm.", Detail: "clean", Thumb: t().Add(ui.ThumbUser, 0.2, 0.8).Add(ui.ThumbTool, 1.0, 1.2).Add(ui.ThumbSpeech, 1.1, 1.6), Who: "Alan · Bedroom", Figure: "390 ms", When: "06:41"}},
	}
	return []Day{{"Sun 4 Oct", sun, sunRows}, {"Mon 5 Oct", mon, monRows}, {"Tue 6 Oct", tue, tueRows}}
}

// ---------------------------------------------------------------- Replay

// ReplayOverrides are the inputs a replay can change.
var ReplayOverrides = [][2]string{{"prompt", "System prompt"}, {"schema", "Tool schema"}, {"model", "LLM provider"}, {"policy", "on_interrupt policy"}}

// ReplayRun is what the last run applied.
type ReplayRun struct {
	N       int
	Applied map[string]bool
	Invalid bool // reviewer marked turn 2 onwards invalid
}

// Changed reports whether the run produced different output (only the prompt
// override changes anything in this trace).
func (r ReplayRun) Changed() bool { return r.Applied["prompt"] }

// ReplayMetricsFor is the outcome strip for a run.
func ReplayMetricsFor(r ReplayRun) ui.MetricStrip {
	if !r.Changed() {
		return ui.MetricStrip{Label: "Outcome", Items: []ui.Metric{
			{Label: "Speech generated", Value: "4.95 s", Note: "unchanged"},
			{Label: "Recorded barge-in", Value: "still fires at 7.62"},
			{Label: "Tool calls", Value: "identical"},
			{Label: "First audio", Value: "520 ms"},
			{Label: "Validity", Value: "identical to recorded", Tone: ui.ToneMuted},
		}}
	}
	m := ReplayMetrics()
	if r.Invalid {
		m.Items[4] = ui.Metric{Label: "Validity", Value: "scored to #0041 only", Tone: ui.ToneMuted}
	}
	return m
}

// ReplayTimeline compares the recorded take with the replayed one on one axis.
func ReplayTimeline(r ReplayRun) ui.Timeline {
	a := ReviewAxis
	rg := func(from, to float64, tone ui.Tone, style ui.RegionStyle, text string) ui.Region {
		return ui.Region{Left: a.Pos(from), Width: a.Pos(to) - a.Pos(from), Tone: tone, Style: style, Text: text, Wrap: true}
	}
	pin := func(id, label string, t float64, row int, tone ui.Tone) ui.Pin {
		return ui.Pin{ID: id, Label: label, Pos: a.Pos(t), Row: row, Tone: tone}
	}
	recorded := []ui.Track{
		{Name: "Recorded · v14", Kind: ui.TrackGroup, Tone: ui.ToneHuman},
		{Name: "Speech", Kind: ui.TrackSpeech, Height: 76, Regions: []ui.Region{
			rg(3.62, 4.25, ui.ToneVoice, ui.RegionFill, "One sec."),
			rg(5.45, 7.62, ui.ToneVoice, ui.RegionFill, "Kitchen lights are off. Tomorrow you’ve got three things: the dentist at nine, stand—"),
			rg(7.62, 8.9, ui.ToneVoice, ui.RegionUnheard, "never heard"),
			rg(8.95, 10.05, ui.ToneVoice, ui.RegionFill, "Dentist at nine. That’s it."),
		}},
		{Name: "Tools", Kind: ui.TrackLane, Regions: []ui.Region{rg(3.52, 5.1, ui.ToneHome, ui.RegionFill, "")}},
		{Name: "Events", Kind: ui.TrackEvents, Height: 56, Shade: true, Pins: []ui.Pin{
			pin("r1", "speak ✂", 5.45, 0, ui.ToneVoice), pin("r2", "BARGE-IN", 7.62, 0, ui.TonePeople), pin("r3", "“Just the first one.”", 8.45, 1, ui.TonePeople),
		}},
	}
	var replayed []ui.Track
	if r.Changed() {
		replayed = []ui.Track{
			{Name: "Replayed · v15-draft", Kind: ui.TrackGroup, Tone: ui.ToneVoice},
			{Name: "Speech", Kind: ui.TrackSpeech, Height: 76, Regions: []ui.Region{
				rg(3.62, 4.25, "", ui.RegionGhost, "One sec. · same"),
				rg(5.45, 7.05, ui.ToneVoice, ui.RegionFill, "Kitchen lights are off. Three things tomorrow — first is the dentist at nine. Want the rest?"),
				rg(8.8, 9.9, ui.ToneVoice, ui.RegionFill, "Just that one, then. You’re clear after it."),
			}},
			{
				Name: "Tools", Kind: ui.TrackLane, Regions: []ui.Region{{Left: a.Pos(3.52), Width: a.Pos(5.1) - a.Pos(3.52), Style: ui.RegionGhost}},
				Captions: []ui.Caption{{Pos: a.Pos(5.15), Text: "identical · served from journal", Tone: ui.ToneMuted}},
			},
			{Name: "Events", Kind: ui.TrackEvents, Height: 56, Shade: true, Pins: []ui.Pin{
				pin("p1", "speak · finished 7.05", 5.45, 0, ui.ToneVoice), pin("p2", "recorded audio → new user turn", 7.62, 1, ui.ToneMuted),
			}},
		}
	} else {
		replayed = []ui.Track{
			{Name: "Replayed · no overrides", Kind: ui.TrackGroup, Tone: ui.ToneMuted},
			{Name: "Speech", Kind: ui.TrackSpeech, Height: 76, Regions: []ui.Region{
				rg(3.62, 4.25, "", ui.RegionGhost, "same"), rg(5.45, 7.62, "", ui.RegionGhost, "same as recorded"), rg(8.95, 10.05, "", ui.RegionGhost, "same"),
			}},
			{Name: "Tools", Kind: ui.TrackLane, Regions: []ui.Region{{Left: a.Pos(3.52), Width: a.Pos(5.1) - a.Pos(3.52), Style: ui.RegionGhost}}},
			{Name: "Events", Kind: ui.TrackEvents, Height: 56, Shade: true, Pins: []ui.Pin{pin("p0", "deterministic · byte-identical", 0.1, 0, ui.ToneMuted)}},
		}
	}
	tracks := []ui.Track{
		{Name: "Input · pinned", Kind: ui.TrackGroup, Tone: ui.ToneMuted},
		{
			Name: "Mic", Sub: "from journal", Kind: ui.TrackAudio, Height: 80, Shade: true,
			Wave: ui.WavePath(peaks(320, 0, 11, [][2]float64{{0.15, 2.9}, {7.3, 8.4}}, 0.05, 7)), WaveText: "Recorded mic",
		},
	}
	tracks = append(tracks, recorded...)
	tracks = append(tracks, replayed...)
	t := ui.Timeline{ID: "replay-timeline", Label: "Take comparison", Ticks: a.Ticks(1, "s"), Tracks: tracks}
	recFrom, recTo := 2, 2+len(recorded)
	repFrom := recTo
	t.Overlays = []ui.Overlay{{Kind: ui.OverlayCut, Pos: a.Pos(7.62), Top: t.TrackTop(recFrom), Height: t.TracksHeight(recFrom, recTo), Label: "cut 7.620 s"}}
	if r.Changed() {
		t.Overlays = append(t.Overlays, ui.Overlay{Kind: ui.OverlayMarker, Pos: a.Pos(7.62), Top: t.TrackTop(repFrom + 1), Height: t.TracksHeight(repFrom+1, len(tracks))})
	}
	return t
}

// ---------------------------------------------------------------- Export

// ExportKind describes one dataset.
type ExportKind struct {
	ID, Label, Sub, Format, Dest string
}

var ExportKinds = []ExportKind{
	{"dpo", "DPO pairs", "TRL preference JSONL", "TRL conversational preference", "s3://chorus-datasets/dpo/2026-10-06/"},
	{"sft", "Tool-call SFT", "qwen3 chat template", "chat messages with tool_calls", "s3://chorus-datasets/sft/2026-10-06/"},
	{"stt", "STT adaptation", "NeMo manifest + wav", "NeMo JSON manifest", "s3://chorus-datasets/stt/2026-10-06/"},
	{"wake", "Wake corpus", "positives + hard negatives", "wav + CSV labels · both channels", "s3://chorus-datasets/wake/2026-10-06/"},
}

// ExportRules are the household rules.
type ExportRules struct {
	Guests, Memories, Embeddings, Bedroom bool   // exclude guests, drop unshared memories, strip embeddings, include bedroom
	Include                               string // "accepted+edited", "accepted", "all"
	Split                                 int    // eval %
}

// ExportStats is what a build would produce.
type ExportStats struct {
	Rows, Train, Eval, Convs int
	Size, Warn               string
	Preview                  string
}

// ComputeExport sizes the export from live state: pairs from Curate and clip
// marks from Hard negatives feed the counts, so the pages agree.
func ComputeExport(kind string, rules ExportRules, pairs map[string]*ui.Pair, marks map[string]string) ExportStats {
	var s ExportStats
	switch kind {
	case "dpo":
		base, mismatched := 24, 0
		var first *ui.Pair
		for _, id := range []string{"p41", "p38", "p35"} {
			p := pairs[id]
			if p == nil {
				continue
			}
			ok := p.Exportable() || (rules.Include != "accepted" && p.Status == ui.PairEdited && !p.Mismatch()) || (rules.Include == "all" && !p.Mismatch() && p.Status != ui.PairDiscarded)
			if ok {
				base++
				if first == nil && p.Source == ui.SourceBargeIn {
					first = p
				}
			}
			if p.Status == ui.PairAccepted && (p.Unfixed || p.Mismatch()) {
				mismatched++
			}
		}
		s.Rows, s.Convs = base, base-2
		s.Size = fmt.Sprintf("%d KB", base*2+13)
		switch {
		case mismatched > 0:
			s.Warn = fmt.Sprintf("%d accepted pair(s) still answer a different prompt than they claim — fix them in Curate or they’ll be skipped.", mismatched)
		case first == nil:
			s.Warn = "No barge-in pair from this week is exportable yet — the record above shows p41 as it will look once it’s fixed and accepted."
		}
		p := pairs["p41"]
		if first != nil {
			p = first
		}
		chosen := p.Chosen
		if p.Mismatch() && p.Suggestion != "" {
			chosen = p.Suggestion
		}
		rec := map[string]any{
			"prompt":   []map[string]string{{"role": "system", "content": "<persona v14>"}, {"role": "user", "content": "What’s on tomorrow, and kill the kitchen lights."}, {"role": "tool", "name": "calendar.search", "content": "3 events: dentist 09:00, standup 10:30, delivery 14:00"}},
			"chosen":   []map[string]string{{"role": "assistant", "content": chosen}},
			"rejected": []map[string]string{{"role": "assistant", "content": p.Rejected + p.RejectedUnheard}},
			"meta":     map[string]string{"pair": p.ID, "source": string(p.Source), "persona": "v14"},
		}
		if p.ID == "p38" {
			rec["prompt"] = []map[string]string{{"role": "system", "content": "<persona v14>"}, {"role": "user", "content": "Read me the weather for Saturday."}, {"role": "tool", "name": "weather.forecast", "content": "48 h"}}
		}
		s.Preview = pretty(rec)
	case "sft":
		s.Rows = 412
		if !rules.Guests {
			s.Rows += 18
		}
		if rules.Bedroom {
			s.Rows += 9
		}
		s.Convs, s.Size = s.Rows-14, fmt.Sprintf("%.1f MB", float64(s.Rows)*0.0046)
		s.Warn = "Only completed turns with no correction and exemplar labels. Interrupted turns are never SFT targets."
		s.Preview = pretty(map[string]any{"messages": []map[string]any{
			{"role": "system", "content": "<persona v14>"},
			{"role": "user", "content": "Dim the lights to movie mode."},
			{"role": "assistant", "tool_calls": []map[string]any{{"name": "ha.scene.turn_on", "arguments": map[string]string{"scene": "movie"}}}},
			{"role": "tool", "name": "ha.scene.turn_on", "content": "ok"},
			{"role": "assistant", "content": "Movie mode."},
		}, "tools": "<schema v6>"})
	case "stt":
		s.Rows = 3118
		if !rules.Guests {
			s.Rows += 96
		}
		if rules.Bedroom {
			s.Rows += 240
		}
		s.Convs, s.Size = s.Rows*2/5, fmt.Sprintf("%d MB", s.Rows*69/1000)
		s.Warn = "Transcripts marked “transcript wrong” use your correction, not the STT output."
		rec := map[string]any{"audio_filepath": "s3://chorus-audio/c_7f3a91e2/mic_aec_000310.wav", "duration": 2.95, "text": "what's on tomorrow and kill the kitchen lights", "channel": "aec", "satellite": "living-room"}
		if !rules.Embeddings {
			rec["speaker_embedding"] = "ecapa:192f…"
		}
		s.Preview = pretty(rec)
	case "wake":
		wake, neg := 0, 0
		for _, m := range marks {
			if m == "wake" {
				wake++
			} else if m == "neg" {
				neg++
			}
		}
		base := 1318
		if rules.Bedroom {
			base += 64 // the bedroom satellite's rejected wakes
		}
		s.Rows, s.Convs, s.Size = base+wake+neg, 0, fmt.Sprintf("%d MB", (base+wake+neg)*73/1000)
		s.Warn = fmt.Sprintf("%d clip(s) you marked “was a wake” go in as positives; %d marked negative join the hard negatives.", wake, neg)
		s.Preview = pretty(map[string]any{"audio_filepath": "wake/neg/w4_aec.wav", "label": 0, "source": "stage2_reject", "checks": map[string]any{"rescore": 0.41, "speech": false, "speaker": 0.71}, "pair": "wake/neg/w4_raw.wav", "split": "train"})
	}
	split := rules.Split
	if split == 0 {
		split = 10
	}
	s.Eval = s.Rows * split / 100
	s.Train = s.Rows - s.Eval
	return s
}

func pretty(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return strings.TrimRight(b.String(), "\n")
}

// Thousands formats 1318 as "1 318".
func Thousands(n int) string {
	s := fmt.Sprint(n)
	if n < 1000 {
		return s
	}
	return s[:len(s)-3] + " " + s[len(s)-3:]
}
