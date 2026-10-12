package volume_test

import (
	"context"
	"maps"
	"strings"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/session"
	"github.com/teagan42/chorus/internal/volume"
)

// The house: a Satellite1 in the kitchen, a Voice PE in the office whose
// firmware has no volume control, two satellites in the living room, and
// the garage one Alan unplugged to flash.
var house = []config.Satellite{
	{Name: "kitchen", Room: "kitchen"},
	{Name: "office", Room: "office"},
	{Name: "living-room", Room: "living room"},
	{Name: "living-room-pe", Room: "living room"},
	{Name: "garage-pe", Room: "garage"},
}

// speakers stands in for each satellite's native API connection: the level
// its volume entity last reported, or why it has none.
type speakers struct {
	mu     sync.Mutex
	levels map[string]int
	none   map[string]error
	set    map[string]int
	// reports is what a device reports back for a level sent to it, where
	// that is not the level itself; silent is a device that reports nothing.
	reports map[string]map[int]int
	silent  map[string]bool
}

func newSpeakers() *speakers {
	return &speakers{
		levels: map[string]int{"kitchen": 50, "living-room": 70},
		none: map[string]error{
			"office": volume.ErrNotImplemented, "living-room-pe": volume.ErrNotImplemented,
			"garage-pe": volume.ErrNotConnected,
		},
		set: map[string]int{},
	}
}

func (s *speakers) Level(sat string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.none[sat]; err != nil {
		return 0, err
	}
	l, ok := s.levels[sat]
	if !ok {
		return 0, volume.ErrLevelUnknown
	}
	return l, nil
}

func (s *speakers) SetLevel(_ context.Context, sat string, level int) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.none[sat]; err != nil {
		return 0, false, err
	}
	s.set[sat] = level
	if s.silent[sat] {
		s.levels[sat] = level
		return level, false, nil
	}
	got, ok := s.reports[sat][level]
	if !ok {
		got = level
	}
	s.levels[sat] = got
	return got, true, nil
}

func (s *speakers) sent() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.set)
}

var (
	teaganInTheKitchen = session.Caller{Person: "teagan", ConversationID: "conv-0742-kitchen", CallID: "call_v1", Satellite: "kitchen"}
	alanInTheOffice    = session.Caller{Person: "alan", ConversationID: "conv-2105-office", CallID: "call_v2", Satellite: "office"}
)

func call(t *testing.T, s *speakers, caller session.Caller, args string) (string, error) {
	t.Helper()
	return volume.Tool(s, house).Invoke(session.WithCaller(context.Background(), caller), args)
}

// Teagan, making breakfast, says "turn it down". With no room named it is
// the kitchen she said it to, stepped down from where it was, and the model
// is told both levels so it can say what changed.
//
// verifies SPEC §5, §6
func TestTurnItDownStepsTheSatelliteItWasSaidTo(t *testing.T) {
	s := newSpeakers()
	out, err := call(t, s, teaganInTheKitchen, `{"step":-10}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"set":[{"satellite":"kitchen","room":"kitchen","level":40,"was":50}]}`; out != want {
		t.Errorf("result = %s, want %s", out, want)
	}
	if got := s.sent(); len(got) != 1 || got["kitchen"] != 40 {
		t.Errorf("set %v, want the kitchen alone at 40", got)
	}
}

// "Mute the speaker" is level 0, and "turn it back on" is the level the
// mute's result said it was: no second tool.
//
// verifies SPEC §6
func TestMutingIsLevelZeroAndTheResultSaysWhatItWas(t *testing.T) {
	s := newSpeakers()
	out, err := call(t, s, teaganInTheKitchen, `{"level":0}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"set":[{"satellite":"kitchen","room":"kitchen","level":0,"was":50}]}`; out != want {
		t.Errorf("result = %s, want %s", out, want)
	}
	if _, err := call(t, s, teaganInTheKitchen, `{"level":50,"step":null,"room":null}`); err != nil {
		t.Fatalf("unmuting with the level the mute reported, nulls for the rest: %v", err)
	}
	if got := s.sent()["kitchen"]; got != 50 {
		t.Errorf("the kitchen is at %d after unmuting, want 50", got)
	}
}

// A step stops at the ends: "much louder" at 95 is 100, "quieter" at 5 is
// silence, and neither is an error the model has to apologise for.
//
// verifies SPEC §6
func TestAStepStopsAtTheEnds(t *testing.T) {
	s := newSpeakers()
	s.levels["kitchen"] = 95
	if _, err := call(t, s, teaganInTheKitchen, `{"step":25}`); err != nil {
		t.Fatal(err)
	}
	if got := s.sent()["kitchen"]; got != 100 {
		t.Errorf("95 up 25 is %d, want 100", got)
	}
	s.levels["kitchen"] = 5
	if _, err := call(t, s, teaganInTheKitchen, `{"step":-10}`); err != nil {
		t.Fatal(err)
	}
	if got := s.sent()["kitchen"]; got != 0 {
		t.Errorf("5 down 10 is %d, want 0", got)
	}
}

// Teagan, in the kitchen, asks for the living room to be turned down where
// the kids are watching. Both satellites there are meant; the Voice PE has
// no volume control, so the model is told which one was set and which was
// not, rather than that it worked everywhere.
//
// verifies SPEC §5, §7
func TestARoomNamedSetsEverySatelliteInIt(t *testing.T) {
	s := newSpeakers()
	out, err := call(t, s, teaganInTheKitchen, `{"level":30,"room":"Living Room"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"set":[{"satellite":"living-room","room":"living room","level":30,"was":70}],` +
		`"unavailable":[{"satellite":"living-room-pe","reason":"not_implemented"}]}`
	if out != want {
		t.Errorf("result = %s, want %s", out, want)
	}
	if got := s.sent(); len(got) != 1 || got["living-room"] != 30 {
		t.Errorf("set %v, want the living room's Satellite1 alone", got)
	}
	// A satellite's own name names it alone.
	if _, err := call(t, s, alanInTheOffice, `{"level":45,"room":"kitchen"}`); err != nil {
		t.Fatal(err)
	}
	if got := s.sent()["kitchen"]; got != 45 {
		t.Errorf("the kitchen, asked from the office, is at %d, want 45", got)
	}
}

// Alan asks the office's Voice PE to turn it up. Its firmware lists no
// volume control, so the call answers not_implemented, the same result a
// tool with nothing behind it gives, and the model says it cannot (SPEC §7).
// The unplugged garage answers not_connected.
//
// verifies SPEC §3.3.1, §7
func TestASatelliteWithNoVolumeControlAnswersNotImplemented(t *testing.T) {
	s := newSpeakers()
	_, err := call(t, s, alanInTheOffice, `{"step":10}`)
	if err == nil || err.Error() != "not_implemented" {
		t.Errorf("the office: %v, want exactly not_implemented", err)
	}
	if _, err := call(t, s, alanInTheOffice, `{"level":20,"room":"garage"}`); err == nil || err.Error() != "not_connected" {
		t.Errorf("the garage: %v, want exactly not_connected", err)
	}
	if got := s.sent(); len(got) != 0 {
		t.Errorf("set %v on satellites that cannot be set", got)
	}
}

// A level the device has not reported yet cannot be stepped from, but an
// exact level can still be set: the result just has no previous level.
//
// verifies SPEC §7
func TestAStepNeedsTheCurrentLevelAndALevelDoesNot(t *testing.T) {
	s := newSpeakers()
	delete(s.levels, "kitchen")
	if _, err := call(t, s, teaganInTheKitchen, `{"step":-10}`); err == nil || err.Error() != "level_unknown" {
		t.Errorf("a step from an unknown level: %v, want level_unknown", err)
	}
	out, err := call(t, s, teaganInTheKitchen, `{"level":35}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"set":[{"satellite":"kitchen","room":"kitchen","level":35}]}`; out != want {
		t.Errorf("result = %s, want %s", out, want)
	}
}

// Arguments the tool cannot act on are refused before anything is set, with
// what was wrong, so the model can ask again: neither or both of level and
// step, a level off the scale, a room nothing is in, or the wrong type.
//
// verifies SPEC §7
func TestVolumeArgumentsItCannotActOnAreRefused(t *testing.T) {
	s := newSpeakers()
	for args, want := range map[string]string{
		`{}`:                         "level or step",
		`{"room":"kitchen"}`:         "level or step",
		`{"level":40,"step":-10}`:    "not both",
		`{"level":140}`:              "0 to 100",
		`{"level":-5}`:               "0 to 100",
		`{"level":40,"room":"loft"}`: "garage, kitchen, living room, office",
		`{"level":"loud"}`:           "bad arguments",
		`not json`:                   "bad arguments",
	} {
		_, err := call(t, s, teaganInTheKitchen, args)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want an error saying %q", args, err, want)
		}
	}
	nowhere := session.Caller{Person: "teagan", ConversationID: "conv-rerun", CallID: "call_v9"}
	if _, err := call(t, s, nowhere, `{"step":-10}`); err == nil || !strings.Contains(err.Error(), "room") {
		t.Errorf("no room and no satellite: %v, want to be asked for a room", err)
	}
	if got := s.sent(); len(got) != 0 {
		t.Errorf("set %v from arguments that were refused", got)
	}
}

// The level the model is told is the one the kitchen reports back, not the
// one sent: a firmware that steps its volume in fives answers 45 for 43, and
// "turn it down a bit" is said as what the speaker is now at (ADR-0067).
//
// verifies SPEC §3.3.1, §6
func TestTheResultIsTheLevelTheDeviceReportsBack(t *testing.T) {
	s := newSpeakers()
	s.reports = map[string]map[int]int{"kitchen": {43: 45}}
	out, err := call(t, s, teaganInTheKitchen, `{"step":-7}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"set":[{"satellite":"kitchen","room":"kitchen","level":45,"was":50}]}`; out != want {
		t.Errorf("result = %s, want %s", out, want)
	}
	if got := s.sent()["kitchen"]; got != 43 {
		t.Errorf("sent the kitchen %d, want 43", got)
	}
}

// A satellite that takes the command but reports nothing back in time is
// still set, as far as anyone knows, but the result says the level is the
// one sent and unconfirmed, so the model does not claim it as fact.
//
// verifies SPEC §3.3.1, §7
func TestALevelTheDeviceNeverConfirmsIsMarkedUnconfirmed(t *testing.T) {
	s := newSpeakers()
	s.silent = map[string]bool{"kitchen": true}
	out, err := call(t, s, teaganInTheKitchen, `{"level":30}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"set":[{"satellite":"kitchen","room":"kitchen","level":30,"was":50,"unconfirmed":true}]}`; out != want {
		t.Errorf("result = %s, want %s", out, want)
	}
}
