package main

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/pb"
	"github.com/teagan42/chorus/internal/session"
)

// ringKey is the kitchen's LED ring. Made up, as the device's keys are.
const ringKey = 0x1ed0c0de

// chorusRing is the ring esphome/satellite1.yaml declares: one light with
// an effect for each state Chorus shows on it.
func chorusRing() *pb.ListEntitiesLightResponse {
	return &pb.ListEntitiesLightResponse{
		ObjectId: "led_ring", Key: ringKey, Name: "LED Ring",
		Effects: []string{"Listening", "Thinking", "Speaking"},
	}
}

// listing answers the daemon's entity list with whatever the device has.
func listing(t *testing.T, conn *fakeNative, es ...proto.Message) {
	t.Helper()
	sent[*pb.ListEntitiesRequest](t, conn, "the entity list")
	for _, e := range es {
		conn.in <- e
	}
	conn.in <- &pb.ListEntitiesDoneResponse{}
}

// ringShows waits for the daemon to set the ring to effect, or off when
// effect is empty. A state passed through faster than it was sent may be
// skipped: the ring shows the newest state, not every one.
func ringShows(t *testing.T, conn *fakeNative, effect string) {
	t.Helper()
	for {
		c := sent[*pb.LightCommandRequest](t, conn, "the ring showing "+effect)
		if c.GetKey() != ringKey || !c.GetHasState() {
			t.Fatalf("ring command %v, want the ring's key and its state", c)
		}
		on := c.GetState() && c.GetHasEffect() && c.GetEffect() == effect
		if on || effect == "" && !c.GetState() {
			return
		}
	}
}

// garageModel reads the garage door when Alan asks about it, says what it
// found, and ends the conversation when he says thanks.
func garageModel() *scriptEngine {
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	return &scriptEngine{decide: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		switch {
		case last.Kind == journal.EntryHeard && last.Text == "is the garage door shut":
			return []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`}, done}
		case last.Kind == journal.EntryResult && last.Tool == "ha_get_state":
			return []session.Action{session.SpeechDelta{CallID: "call_s1", Text: "The garage door is open.", Last: true}, done}
		case last.Kind == journal.EntryHeard && last.Text == "thanks":
			return []session.Action{session.ToolCall{ID: "call_e1", Tool: "end_session", Args: "{}"}, done}
		}
		return []session.Action{done}
	}}
}

// Alan asks the kitchen whether the garage door is shut. The ring is dark
// until the wake, works while Home Assistant is asked, speaks while the
// answer plays, listens while the conversation stays open, and goes dark
// when it ends (SPEC §3.3.1).
//
// verifies SPEC §3.3.1
func TestTheKitchenRingFollowsTheConversation(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[0]}}
	looked := make(chan struct{})
	dialer, clk := newDialer(), newClock()
	r := newRig(t, inv, func(d *deps) {
		d.Native, d.Clock, d.Timers = dialer, clk, clk
		d.engine = garageModel()
		d.tools = map[string]session.Tool{"ha_get_state": session.ToolFunc(func(ctx context.Context, _ string) (string, error) {
			select {
			case <-looked:
				return `{"entity_id":"cover.garage_door","state":"open"}`, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})}
	})
	conn := connect(t, dialer)
	listing(t, conn, chorusRing())
	ringShows(t, conn, "")

	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("is the garage door shut", alan))
	ringShows(t, conn, "Thinking")

	close(looked)
	dev.AwaitTTS(t, 2*len("The garage door is open."))
	ringShows(t, conn, "Speaking")
	dev.PlayAll(t)
	r.spoken(t, "kitchen", 1)
	ringShows(t, conn, "Listening")

	r.utter(t, dev, r.line("thanks", alan))
	r.store.awaitKind(t, journal.KindSessionClosed, 1)
	ringShows(t, conn, "")
}

// The stock Satellite1 and Voice PE firmware list their ring as a plain
// "LED Ring" light, which their own voice assistant animates. It carries
// none of Chorus's effects, so the daemon leaves it alone.
//
// verifies SPEC §3.3.1
func TestARingTheFirmwareAnimatesItselfIsLeftAlone(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[1]}}
	dialer, clk := newDialer(), newClock()
	r := newRig(t, inv, func(d *deps) { d.Native, d.Clock, d.Timers = dialer, clk, clk })

	conn := connect(t, dialer)
	listing(t, conn, &pb.ListEntitiesLightResponse{ObjectId: "led_ring", Key: ringKey, Name: "LED Ring"})
	r.logs.await(t, "no LED ring")
	conn.in <- &pb.PingRequest{}
	sent[*pb.PingResponse](t, conn, "the ping's answer, and no ring command before it")
}
