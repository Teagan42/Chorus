package ui

// Session is one conversation placed on its satellite's lane. Flag adds a
// coloured top edge: People barge-in, Home tool failure, Voice slow/repeated.
type Session struct {
	Hour  float64 // decimal hour, e.g. 19.72
	Flag  Tone
	Href  string
	Label string // accessible label, e.g. "Living room 23:04 · barge-in"
}

// DayLane is one satellite across the day.
type DayLane struct {
	Name     string
	Sub      string // "Satellite1 · 7 sessions"
	On       bool   // filtering the list to this room
	Hx       Hx     // click the lane header to filter
	Presence [][2]float64
	Sessions []Session
	Rejects  []float64 // stage-two wake rejections, decimal hours
}

// Migration links a conversation that moved from one lane to another.
type Migration struct {
	Hour     float64
	FromLane int // index into Lanes
	ToLane   int
	Label    string
}

// DayLanes is Browse’s household day: one lane per satellite, From–To hours.
type DayLanes struct {
	From, To   int
	Lanes      []DayLane
	Migrations []Migration
	Legend     *Legend
}

// LaneH is the lane height in px; the ruler is 26px.
const LaneH = 52

// P converts a decimal hour to a 0–100 position.
func (d DayLanes) P(h float64) float64 { return Linear{float64(d.From), float64(d.To)}.Pos(h) }

// W is the width of an hour span.
func (d DayLanes) W(span [2]float64) float64 { return d.P(span[1]) - d.P(span[0]) }

// Hours are the ruler labels, every 3 hours.
func (d DayLanes) Hours() []Tick {
	var out []Tick
	for h := d.From; h <= d.To; h += 3 {
		out = append(out, Tick{Pos: d.P(float64(h)), Label: twoDigit(h)})
	}
	return out
}

// MigTop and MigH place a migration line between two lanes (px below the ruler).
func (d DayLanes) MigTop(m Migration) int {
	a := min(m.FromLane, m.ToLane)
	return a*LaneH + LaneH/2 - 2
}

func (d DayLanes) MigH(m Migration) int {
	diff := m.ToLane - m.FromLane
	if diff < 0 {
		diff = -diff
	}
	return diff*LaneH + 4
}

func twoDigit(h int) string {
	if h < 10 {
		return "0" + string(rune('0'+h))
	}
	return string(rune('0'+h/10)) + string(rune('0'+h%10))
}
