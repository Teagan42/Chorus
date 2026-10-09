package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/harvest"
	"github.com/teaganglenn/chorus/internal/reviewui/audio"
	"github.com/teaganglenn/chorus/internal/reviewui/ui"
)

// clipView is one playable clip: the label, the WAV URL, and the transcript
// the reviewer is judging it against.
type clipView struct {
	Label string
	Tone  ui.Tone
	Src   string
	Text  string
}

func audioSrc(ref string) string { return "/audio?ref=" + url.QueryEscape(ref) }

// framesMS is how long n device frames play.
func framesMS(n int) int { return n * 1000 / bridge.SampleRate }

// durations reads each ref's length from the blob store. A missing blob is
// zero: the page still renders, the timeline just cannot size that clip.
func (s *server) durations(ctx context.Context, refs ...string) map[string]int {
	out := map[string]int{}
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		rc, err := s.blobs.Open(ctx, ref)
		if err != nil {
			continue
		}
		n, err := io.Copy(io.Discard, rc)
		_ = rc.Close()
		if err == nil {
			out[ref] = audio.DurationMS(int(n))
		}
	}
	return out
}

// reviewLayout places the pair's clips on one axis, in milliseconds from the
// start of the rejected turn's playback. The offsets are derived from clip
// lengths and the recorded barge-in position, not wall clocks: close enough
// to judge a cut by ear, and honest about where each sound sits.
type reviewLayout struct {
	cut      int // where playback stopped
	rejEnd   int // end of the synthesized turn, heard or not
	bargeAt  int // where the interrupting speech starts
	corrAt   int
	corrEnd  int
	asSaidAt int
	total    int
}

// gapMS separates clips whose true spacing the clips themselves cannot say.
const gapMS = 250

// cutMS places the cut on the turn's axis. frames_played is the
// DAC-confirmed point within the cut clip, which plays after every clip the
// user already heard; the detection snapshot is the fallback for a cut that
// discarded unstarted clips and so confirmed no frames.
func cutMS(p harvest.Pair, dur map[string]int) int {
	if p.CutFrames == 0 {
		return p.BargeInPositionMS
	}
	ms := framesMS(p.CutFrames)
	for _, ref := range heardBefore(p) {
		ms += dur[ref]
	}
	return ms
}

// heardBefore is every rejected clip that finished before the cut one. The
// cut clip is the last: Audio.Rejected is in the order the user heard, and
// nothing plays after a truncation.
func heardBefore(p harvest.Pair) []string {
	if p.CutFrames == 0 || len(p.Audio.Rejected) == 0 {
		return p.Audio.Rejected
	}
	return p.Audio.Rejected[:len(p.Audio.Rejected)-1]
}

func layout(p harvest.Pair, dur map[string]int) reviewLayout {
	sum := func(refs []string) int {
		n := 0
		for _, r := range refs {
			n += dur[r]
		}
		return n
	}
	l := reviewLayout{cut: cutMS(p, dur)}
	l.rejEnd = max(sum(p.Audio.Rejected), l.cut)
	if l.rejEnd == 0 {
		l.rejEnd = gapMS // an unsized, uncut turn still gets a visible region
	}
	l.bargeAt = max(0, l.cut-dur[p.Audio.BargeIn])
	l.corrAt = l.cut + gapMS
	l.corrEnd = l.corrAt + dur[p.Audio.Correction]
	l.asSaidAt = l.corrEnd + gapMS
	l.total = max(l.rejEnd, l.asSaidAt+sum(p.Audio.AsSaid)) + gapMS
	return l
}

// reviewTimeline draws the pair: the assistant's turn with its unheard tail
// hatched, the mic with the interruption and the correction, the answering
// turn, and the cut as the overlay the whole screen exists to justify.
func reviewTimeline(p harvest.Pair, l reviewLayout) ui.Timeline {
	pct := func(ms int) float64 { return float64(ms) / float64(l.total) * 100 }
	sec := func(ms int) string { return ui.Timecode(float64(ms) / 1000) }

	speech := ui.Track{Name: "assistant", Sub: "tts out", Kind: ui.TrackSpeech, Regions: []ui.Region{
		{Left: 0, Width: pct(l.cut), Tone: ui.ToneVoice, Style: ui.RegionFill, Text: p.Rejected, Wrap: true},
	}}
	if l.rejEnd > l.cut {
		speech.Regions = append(speech.Regions, ui.Region{
			Left: pct(l.cut), Width: pct(l.rejEnd - l.cut), Tone: ui.ToneVoice,
			Style: ui.RegionUnheard, Text: p.RejectedUnheard, Wrap: true,
		})
	}
	if len(p.Audio.AsSaid) > 0 || p.AsSaid != "" {
		// Unsized clips can leave the slot narrower than the gap; floor it
		// so the answering turn never vanishes.
		w := max(l.total-gapMS-l.asSaidAt, gapMS)
		speech.Regions = append(speech.Regions, ui.Region{
			Left: pct(l.asSaidAt), Width: pct(w), Tone: ui.ToneVoice,
			Style: ui.RegionFill, Text: p.AsSaid, Wrap: true,
		})
	}

	mic := ui.Track{Name: "mic", Sub: p.HeardSpeaker, Kind: ui.TrackAudio, Regions: []ui.Region{
		{Left: pct(l.bargeAt), Width: pct(l.cut - l.bargeAt), Tone: ui.TonePeople, Style: ui.RegionOutline, Text: "barge-in"},
		{Left: pct(l.corrAt), Width: pct(l.corrEnd - l.corrAt), Tone: ui.TonePeople, Style: ui.RegionFill, Text: p.Heard, Wrap: true},
	}}

	return ui.Timeline{
		ID:    "review-timeline",
		Label: "Barge-in " + p.ID,
		Ticks: []ui.Tick{
			{Pos: 0, Label: "0:00.000"},
			{Pos: pct(l.cut), Label: sec(l.cut), Tone: ui.TonePeople},
			{Pos: 100, Label: sec(l.total)},
		},
		Tracks: []ui.Track{speech, mic},
		Overlays: []ui.Overlay{
			{Kind: ui.OverlayCut, Pos: pct(l.cut), Label: "cut · " + sec(l.cut)},
		},
	}
}

// clips lists every playable side of the pair, in the order a reviewer
// replays an interruption.
func clips(p harvest.Pair) []clipView {
	add := func(out []clipView, label string, tone ui.Tone, ref, text string) []clipView {
		if ref == "" {
			return out
		}
		return append(out, clipView{Label: label, Tone: tone, Src: audioSrc(ref), Text: text})
	}
	var out []clipView
	for i, ref := range p.Audio.Rejected {
		label := "rejected · heard"
		if len(p.Audio.Rejected) > 1 {
			label = fmt.Sprintf("rejected · heard · %d", i+1)
		}
		out = add(out, label, ui.ToneVoice, ref, p.Rejected)
	}
	// The cut clip's blob keeps the rendered tail the user never heard
	// (SPEC §9.1), so its heard player stops at the DAC-confirmed frame and
	// the tail plays on its own.
	if n := len(out); p.CutFrames > 0 && n > 0 {
		out[n-1].Src += fmt.Sprintf("&to=%d", p.CutFrames)
		out = append(out, clipView{
			Label: "rejected · unheard tail", Tone: ui.ToneVoice,
			Src:  audioSrc(p.Audio.Rejected[len(p.Audio.Rejected)-1]) + fmt.Sprintf("&from=%d", p.CutFrames),
			Text: p.RejectedUnheard,
		})
	}
	out = add(out, "barge-in", ui.TonePeople, p.Audio.BargeIn, "")
	out = add(out, "correction", ui.TonePeople, p.Audio.Correction, p.Heard)
	for i, ref := range p.Audio.AsSaid {
		label := "answer · as said"
		if len(p.Audio.AsSaid) > 1 {
			label = fmt.Sprintf("answer · as said · %d", i+1)
		}
		out = add(out, label, ui.ToneVoice, ref, p.AsSaid)
	}
	return out
}

func reviewInspector(p harvest.Pair, l reviewLayout) ui.Inspector {
	return ui.Inspector{
		Cap:     fmt.Sprintf("#%04d · cut at %s · barge-in", p.Seq.Cut, ui.Timecode(float64(l.cut)/1000)),
		Body:    p.Rejected,
		Unheard: p.RejectedUnheard,
		Meta:    "correction · " + p.HeardSpeaker + ": " + p.Heard,
		Foot: fmt.Sprintf("in effect: %s · %s · %s",
			orUnattributed(p.Versions.Model, p.Attributed), p.Versions.Prompt, p.Versions.ToolSchema),
	}
}

func orUnattributed(model string, attributed bool) string {
	if !attributed {
		return "unattributed: the turn recorded no completion"
	}
	return model
}

func reviewHref(id string) string { return ui.Routes[ui.StepReview] + "?pair=" + url.QueryEscape(id) }

func (s *server) review(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pairs, err := s.pairs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sel, ok := selectPair(pairs, r.URL.Query().Get("pair"), "all")
	if !ok {
		s.render(w, "page-review-empty", map[string]any{
			"Doc":    s.doc("Review"),
			"Header": ui.NewAppHeader(ui.StepReview, 0, "chorus · journal"),
			"Empty": ui.EmptyState{
				Title: "Nothing to review.",
				Body:  "Barge-ins land here as the journal records them.",
			},
		})
		return
	}

	refs := append(append([]string{sel.H.Audio.BargeIn, sel.H.Audio.Correction}, sel.H.Audio.Rejected...), sel.H.Audio.AsSaid...)
	l := layout(sel.H, s.durations(r.Context(), refs...))

	// Prev / next walk the harvested order.
	idx := 0
	for i, p := range pairs {
		if p.ID == sel.ID {
			idx = i
		}
	}
	var actions []ui.Button
	if idx > 0 {
		actions = append(actions, ui.Button{Label: "‹ Prev", Href: reviewHref(pairs[idx-1].ID)})
	}
	if idx+1 < len(pairs) {
		actions = append(actions, ui.Button{Label: "Next ›", Href: reviewHref(pairs[idx+1].ID)})
	}

	s.render(w, "page-review", map[string]any{
		"Doc":    s.doc("Review · " + sel.ID),
		"Header": ui.NewAppHeader(ui.StepReview, unreviewedCount(pairs), "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "03 · Review", Trace: true,
			Title:    sel.promptTitle(),
			Subtitle: fmt.Sprintf("%s · %s · pair %d of %d", sel.conversationID, sel.H.HeardSpeaker, idx+1, len(pairs)),
			Meta:     "from the journal", SubtitleMono: true,
			Actions: actions,
		},
		"Timeline":  reviewTimeline(sel.H, l),
		"Inspector": reviewInspector(sel.H, l),
		"Clips":     clips(sel.H),
		"Pair":      s.pairView(sel, ui.PairModeView, ""),
	})
}

// promptTitle is the utterance the rejected turn answered: the page's name.
func (p pair) promptTitle() string {
	for i := len(p.H.Prompt) - 1; i >= 0; i-- {
		if p.H.Prompt[i].Role == "user" {
			return p.H.Prompt[i].Content
		}
	}
	return p.ID
}
