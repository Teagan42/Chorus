package announce_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/teaganglenn/chorus/internal/announce"
	"github.com/teaganglenn/chorus/internal/config"
	"github.com/teaganglenn/chorus/internal/session"
)

// The house: a kitchen satellite and a Voice PE in the office, and the
// garage one Alan unplugged to flash.
var house = []config.Satellite{
	{Name: "kitchen", Room: "kitchen"},
	{Name: "office", Room: "office"},
	{Name: "garage-pe", Room: "garage"},
}

// rooms stands in for the satellites' links. The garage is not connected,
// and the office's link is failing.
type rooms struct {
	mu      sync.Mutex
	said    map[string]session.Announcement
	offline map[string]bool
	broken  map[string]error
}

func newRooms() *rooms {
	return &rooms{
		said:    map[string]session.Announcement{},
		offline: map[string]bool{"garage-pe": true},
		broken:  map[string]error{},
	}
}

func (r *rooms) Announce(_ context.Context, sat string, a session.Announcement) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.offline[sat] {
		return "", announce.ErrNotConnected
	}
	if err := r.broken[sat]; err != nil {
		return "", err
	}
	r.said[sat] = a
	return "conv-" + sat, nil
}

var teaganInTheOffice = session.Caller{
	Person: "teagan", ConversationID: "conv-1838-office", CallID: "call_a1", Satellite: "office",
}

func call(t *testing.T, r *rooms, caller session.Caller, args string) (string, error) {
	t.Helper()
	return announce.Tool(r, house).Invoke(session.WithCaller(context.Background(), caller), args)
}

// Teagan, in the office, asks the kitchen whether anyone wants wine with
// dinner and to listen for the answer. Only the kitchen hears it, carrying
// who asked and from where, so the kitchen's model knows whom it is for.
//
// verifies SPEC §4
func TestAnAnnouncementGoesToTheRoomNamed(t *testing.T) {
	r := newRooms()
	out, err := call(t, r, teaganInTheOffice, `{"text":"Dinner is ready. Does anyone want wine?","room":"Kitchen","start_conversation":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"announced_in":["kitchen"]}` {
		t.Errorf("result = %s", out)
	}
	want := session.Announcement{
		Text: "Dinner is ready. Does anyone want wine?", Source: session.SourceRequest,
		RequestedBy: "teagan", FromSatellite: "office", FromConversation: "conv-1838-office",
		StartConversation: true,
	}
	if len(r.said) != 1 || r.said["kitchen"] != want {
		t.Errorf("said = %+v, want %+v in the kitchen alone", r.said, want)
	}
}

// With no room named it is a broadcast: every room but the one it was asked
// in. The garage is unplugged, and the model is told so rather than told it
// worked everywhere.
//
// verifies SPEC §4, §7
func TestABroadcastSkipsTheRoomItWasAskedIn(t *testing.T) {
	r := newRooms()
	alan := session.Caller{Person: "alan", ConversationID: "conv-1850-kitchen", CallID: "call_a2", Satellite: "kitchen"}
	out, err := call(t, r, alan, `{"text":"The car is here."}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"announced_in":["office"],"not_connected":["garage-pe"]}` {
		t.Errorf("result = %s", out)
	}
	if _, echoed := r.said["kitchen"]; echoed {
		t.Error("the broadcast was said back in the kitchen it was asked in")
	}
}

// A room nothing is in is refused with the rooms there are, which the model
// can use to ask again or say what it could not do (SPEC §7). So is an
// announcement that reached nowhere, and one with nothing to say.
//
// verifies SPEC §7
func TestAnAnnouncementThatCannotBeSaidIsAnError(t *testing.T) {
	r := newRooms()
	_, err := call(t, r, teaganInTheOffice, `{"text":"Bath time.","room":"bathroom"}`)
	if err == nil || !strings.Contains(err.Error(), "garage, kitchen, office") {
		t.Errorf("an unknown room: %v, want the rooms listed", err)
	}
	if _, err := call(t, r, teaganInTheOffice, `{"text":"Bring the bins in.","room":"garage"}`); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("an unplugged room: %v", err)
	}
	r.broken["kitchen"] = errors.New("tts unavailable")
	if _, err := call(t, r, teaganInTheOffice, `{"text":"Dinner in five.","room":"kitchen"}`); err == nil || !strings.Contains(err.Error(), "tts unavailable") {
		t.Errorf("a failing room: %v", err)
	}
	if _, err := call(t, r, teaganInTheOffice, `{"text":"   ","room":"kitchen"}`); err == nil {
		t.Error("announced nothing")
	}
	if _, err := call(t, r, teaganInTheOffice, `{"text":42}`); err == nil {
		t.Error("announced a number")
	}
	alone := []config.Satellite{{Name: "office", Room: "office"}}
	if _, err := announce.Tool(r, alone).Invoke(session.WithCaller(context.Background(), teaganInTheOffice), `{"text":"Anyone home?"}`); err == nil {
		t.Error("broadcast from the only room in the house")
	}
	if len(r.said) != 0 {
		t.Errorf("said %+v", r.said)
	}
}
