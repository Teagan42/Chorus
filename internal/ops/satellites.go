package ops

import (
	"sync/atomic"
	"time"
)

// Satellite is one inventory entry, as /readyz names it.
type Satellite struct {
	Name string
	Room string
}

// Connected and Disconnected are a link's state in /readyz.
const (
	Connected    = "connected"
	Disconnected = "disconnected"
)

// SatelliteState is one satellite as /readyz reports it.
type SatelliteState struct {
	Name        string     `json:"name"`
	Room        string     `json:"room,omitempty"`
	AudioLink   string     `json:"audio_link"`
	NativeAPI   string     `json:"native_api"`
	LastFrameAt *time.Time `json:"last_frame_at,omitempty"`
}

// Satellites is each inventory satellite's links, as the daemon tells them.
// A name the inventory does not hold is ignored.
type Satellites struct {
	clock Clock
	order []*satellite
	by    map[string]*satellite
}

type satellite struct {
	Satellite
	audio, native atomic.Bool
	// lastFrame is unix nanoseconds, 0 for never: written on every frame,
	// so it takes no lock.
	lastFrame atomic.Int64
}

func newSatellites(clock Clock, inv []Satellite) *Satellites {
	s := &Satellites{clock: clock, by: map[string]*satellite{}}
	for _, sat := range inv {
		e := &satellite{Satellite: sat}
		s.order = append(s.order, e)
		s.by[sat.Name] = e
	}
	return s
}

// Linked records the satellite's verified audio link coming up or going.
func (s *Satellites) Linked(name string, up bool) {
	if e := s.by[name]; e != nil {
		e.audio.Store(up)
	}
}

// Native records the satellite's native API connection held or dropped.
func (s *Satellites) Native(name string, up bool) {
	if e := s.by[name]; e != nil {
		e.native.Store(up)
	}
}

// Frame records a frame arriving on the satellite's audio link now.
func (s *Satellites) Frame(name string) {
	if e := s.by[name]; e != nil {
		e.lastFrame.Store(s.clock.Now().UnixNano())
	}
}

func (s *Satellites) states() []SatelliteState {
	out := make([]SatelliteState, 0, len(s.order))
	for _, e := range s.order {
		st := SatelliteState{Name: e.Name, Room: e.Room, AudioLink: linkState(e.audio.Load()), NativeAPI: linkState(e.native.Load())}
		if ns := e.lastFrame.Load(); ns != 0 {
			at := time.Unix(0, ns).UTC()
			st.LastFrameAt = &at
		}
		out = append(out, st)
	}
	return out
}

func linkState(up bool) string {
	if up {
		return Connected
	}
	return Disconnected
}
