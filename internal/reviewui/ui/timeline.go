package ui

// TrackKind sets a track's default height and how its regions sit.
type TrackKind string

const (
	TrackAudio  TrackKind = "audio"  // 140px — mic waveform with caption
	TrackSpeech TrackKind = "speech" // 104px — assistant speech regions
	TrackLane   TrackKind = "lane"   // 30px  — one child activity or tool
	TrackEvents TrackKind = "events" // 168px — event pins
	TrackGroup  TrackKind = "group"  // 26px  — caps divider, e.g. CONVERSATION · ALAN
)

// RegionStyle is how a region is drawn.
type RegionStyle string

const (
	RegionFill     RegionStyle = "fill"     // solid tone, text on --tone-on
	RegionOutline  RegionStyle = "outline"  // tone outline, tone text (speaker ID)
	RegionUnheard  RegionStyle = "unheard"  // hatched + dashed: generated, never heard
	RegionActivity RegionStyle = "activity" // listening / thinking
	RegionRejected RegionStyle = "rejected" // dashed muted: a gate rejected it
	RegionMarker   RegionStyle = "marker"   // small solid surface block: gate accepted
	RegionGhost    RegionStyle = "ghost"    // muted outline: unchanged in a replay
)

// Region is a span on a track. Left and Width are 0–100 along the shared axis.
type Region struct {
	Left, Width float64
	Tone        Tone
	Style       RegionStyle
	Text        string
	Wrap        bool // allow multi-line text inside the region
	Compact     bool // short block at the top of a speech track; put the words in a Caption below
}

// Caption is a short label placed on a track, e.g. the transcript under the
// mic waveform or "policy · duration" beside a tool bar.
type Caption struct {
	Pos    float64
	Text   string
	Tone   Tone
	Bottom bool // pin to the bottom of the track instead of centring
	Quote  bool // render as UI text instead of mono
}

// Bar is one waveform bar (Height 0–1) at Pos.
type Bar struct {
	Pos, Height float64
}

// Track is one row of the timeline: a header cell and a body.
type Track struct {
	Name     string
	Sub      string
	NameMono bool
	Kind     TrackKind
	Height   int  // px; 0 = default for Kind
	Shade    bool // ground-2 background
	Tone     Tone // group dividers take the tone for their label
	Regions  []Region
	Wave     string // SVG path in a 1000×40 viewBox (see WavePath)
	WaveText string // aria-label for the waveform
	Bars     []Bar
	Captions []Caption
	Pins     []Pin
}

// H returns the track height in px.
func (t Track) H() int {
	if t.Height > 0 {
		return t.Height
	}
	switch t.Kind {
	case TrackAudio:
		return 140
	case TrackSpeech:
		return 104
	case TrackEvents:
		return 168
	case TrackGroup:
		return 26
	default:
		return 30
	}
}

// Tick is a ruler label.
type Tick struct {
	Pos   float64
	Label string
	Tone  Tone
}

// OverlayKind is a vertical mark spanning tracks.
type OverlayKind string

const (
	OverlayCut      OverlayKind = "cut"      // barge-in truncation, People pink, 2px
	OverlayPlayhead OverlayKind = "playhead" // 1px surface
	OverlayGap      OverlayKind = "gap"      // collapsed dead time, hatched
	OverlayMarker   OverlayKind = "marker"   // dashed muted line
)

// Overlay spans every track unless Top/Height (px below the ruler) are set.
type Overlay struct {
	Kind   OverlayKind
	Pos    float64
	Width  float64 // gaps only
	Label  string
	Top    int
	Height int
}

// Timeline is the review instrument: a header column and tracks that share
// one Axis. Positions are percentages — never pixels — so it scales.
type Timeline struct {
	ID       string // set to make the whole timeline an htmx swap target
	OOB      bool   // render with hx-swap-oob (e.g. a channel switch also redraws the waveform)
	Label    string
	MinWidth int // px before it scrolls sideways; default 1100
	Ticks    []Tick
	Tracks   []Track
	Overlays []Overlay
}

func (t Timeline) MinW() int {
	if t.MinWidth > 0 {
		return t.MinWidth
	}
	return 1100
}

// TrackTop is the px offset of track i below the ruler, for overlays that
// should span only some tracks (set Overlay.Top / Overlay.Height from it).
func (t Timeline) TrackTop(i int) int {
	top := 0
	for j := 0; j < i && j < len(t.Tracks); j++ {
		top += t.Tracks[j].H()
	}
	return top
}

// TracksHeight is the combined px height of tracks [from, to).
func (t Timeline) TracksHeight(from, to int) int { return t.TrackTop(to) - t.TrackTop(from) }
