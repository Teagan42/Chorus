package main

import (
	"context"
	"errors"
	"testing"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/pb"
	"github.com/teagan42/chorus/internal/session"
	"github.com/teagan42/chorus/internal/volume"
)

// volumeKey is the kitchen's speaker volume. Made up, as the device's keys are.
const volumeKey = 0x50da0b1e

// volumeNumber is the number esphome/satellite1.yaml declares: 0 to 100,
// named Speaker Volume, so listed as speaker_volume.
func volumeNumber() *pb.ListEntitiesNumberResponse {
	return &pb.ListEntitiesNumberResponse{
		ObjectId: "speaker_volume", Key: volumeKey, Name: "Speaker Volume",
		MinValue: 0, MaxValue: 100, Step: 1, UnitOfMeasurement: "%",
	}
}

// developerVolumeRange is the stock firmware's developer number of the same
// family: a 0 to 1 bound on the amplifier, not the household's volume.
func developerVolumeRange() *pb.ListEntitiesNumberResponse {
	return &pb.ListEntitiesNumberResponse{
		ObjectId: "volume_range_min", Key: 0x0dd5e7, Name: "Volume Range Min", MinValue: 0, MaxValue: 1, Step: 0.01,
	}
}

// volumeAt has the device report its volume, and returns once the daemon has
// read it: a ping sent after the report is answered only after it.
func volumeAt(t *testing.T, conn *fakeNative, level float32) {
	t.Helper()
	conn.in <- &pb.NumberStateResponse{Key: volumeKey, State: level}
	conn.in <- &pb.PingRequest{}
	sent[*pb.PingResponse](t, conn, "the ping's answer, after the volume report")
}

// volumeSetTo waits for the daemon to set the kitchen's volume.
func volumeSetTo(t *testing.T, conn *fakeNative, level float32) {
	t.Helper()
	c := sent[*pb.NumberCommandRequest](t, conn, "the volume command")
	if c.GetKey() != volumeKey || c.GetState() != level {
		t.Errorf("number command %v, want key %#x set to %v", c, volumeKey, level)
	}
}

// setVolume runs the daemon's set_volume executor as a turn on sat would.
func setVolume(t *testing.T, r *rig, sat, args string) (string, error) {
	t.Helper()
	tool, ok := r.dm.tools["set_volume"]
	if !ok {
		t.Fatal("set_volume has no executor")
	}
	ctx := session.WithCaller(context.Background(), session.Caller{Person: "teagan", ConversationID: "conv-" + sat, CallID: "call_v1", Satellite: sat})
	return tool.Invoke(ctx, args)
}

// quieterModel turns the volume down when asked and says so.
func quieterModel() *scriptEngine {
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	return &scriptEngine{decide: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		switch {
		case last.Kind == journal.EntryHeard && last.Text == "turn it down":
			return []session.Action{session.ToolCall{ID: "call_v1", Tool: "set_volume", Args: `{"step":-10}`}, done}
		case last.Kind == journal.EntryResult && last.Tool == "set_volume":
			return []session.Action{session.SpeechDelta{CallID: "call_s1", Text: "Turned down.", Last: true}, done}
		}
		return []session.Action{done}
	}}
}

// Teagan, in the kitchen at breakfast, says "turn it down". The model steps
// the volume down with no room named, so it is the kitchen's: the satellite's
// Speaker Volume number receives the new level over the native API, and the
// result the model is shown, and the journal keeps, says what it was.
//
// verifies SPEC §3.3.1, §5, §6
func TestTeaganTurnsTheKitchenDown(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[0]}}
	dialer, clk, emb := newDialer(), newClock(), newEmbedder()
	r := newRig(t, inv, func(d *deps) {
		d.Native, d.Clock, d.Timers = dialer, clk, clk
		d.engine = quieterModel()
		d.household = []string{"alan", "teagan", "alice"}
		d.speakers = threeVoices(t, emb)
	})
	r.emb = emb

	conn := connect(t, dialer)
	listing(t, conn, developerVolumeRange(), volumeNumber())
	sent[*pb.SubscribeStatesRequest](t, conn, "the state subscription, for the volume's level")
	volumeAt(t, conn, 60)

	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("turn it down", teagan))
	volumeSetTo(t, conn, 50)

	res := r.store.awaitKind(t, journal.KindToolResult, 1)
	want := `{"set":[{"satellite":"kitchen","room":"kitchen","level":50,"was":60}]}`
	if res.Fields["outcome"] != "ok" || res.Fields["result_json"] != want {
		t.Errorf("tool_result = %v, want ok with %s", res.Fields, want)
	}
	dev.AwaitTTS(t, 2*len("Turned down."))
}

// The kitchen's level is the device's to report: turned up on the
// satellite's own buttons or from Home Assistant, the next step starts from
// there. Its absolute levels go through as they are, 0 included.
//
// verifies SPEC §3.3.1
func TestTheKitchensVolumeStepsFromWhatTheDeviceLastReported(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[0]}}
	dialer, clk := newDialer(), newClock()
	r := newRig(t, inv, func(d *deps) { d.Native, d.Clock, d.Timers = dialer, clk, clk })
	r.join(t, kitchenIP)

	conn := connect(t, dialer)
	listing(t, conn, volumeNumber())
	sent[*pb.SubscribeStatesRequest](t, conn, "the state subscription")
	volumeAt(t, conn, 60)
	volumeAt(t, conn, 30)

	out, err := setVolume(t, r, "kitchen", `{"step":15}`)
	if err != nil {
		t.Fatal(err)
	}
	volumeSetTo(t, conn, 45)
	if want := `{"set":[{"satellite":"kitchen","room":"kitchen","level":45,"was":30}]}`; out != want {
		t.Errorf("result = %s, want %s", out, want)
	}
	// Before the device echoes it, the level just set is where the next step starts.
	if _, err := setVolume(t, r, "kitchen", `{"step":-5}`); err != nil {
		t.Fatal(err)
	}
	volumeSetTo(t, conn, 40)
	if _, err := setVolume(t, r, "kitchen", `{"level":0}`); err != nil {
		t.Fatal(err)
	}
	volumeSetTo(t, conn, 0)
}

// The office's Voice PE lists no volume control, so set_volume there answers
// not_implemented and sends the device nothing. Before any connection has
// listed a satellite's entities, and after one drops, it is not_connected:
// what the satellite has is not known (SPEC §7).
//
// verifies SPEC §3.3.1, §7
func TestASatelliteWithNoVolumeEntityAnswersNotImplemented(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[1]}}
	dialer, clk := newDialer(), newClock()
	r := newRig(t, inv, func(d *deps) { d.Native, d.Clock, d.Timers = dialer, clk, clk })
	r.join(t, officeIP)

	if _, err := setVolume(t, r, "office", `{"step":10}`); err == nil || err.Error() != "not_connected" {
		t.Errorf("before the native API connects: %v, want not_connected", err)
	}

	conn := connect(t, dialer)
	listing(t, conn, developerVolumeRange())
	r.logs.await(t, "no speaker volume")
	if _, err := setVolume(t, r, "office", `{"step":10}`); err == nil || err.Error() != "not_implemented" {
		t.Errorf("with no volume entity: %v, want exactly not_implemented", err)
	}
	if lvl, err := r.dm.Level("office"); !errors.Is(err, volume.ErrNotImplemented) {
		t.Errorf("Level = %d, %v, want ErrNotImplemented", lvl, err)
	}
	conn.in <- &pb.PingRequest{}
	sent[*pb.PingResponse](t, conn, "the ping's answer, and no volume command or subscription before it")

	_ = conn.Close()
	r.logs.await(t, "native api dropped")
	if _, err := setVolume(t, r, "office", `{"level":20}`); err == nil || err.Error() != "not_connected" {
		t.Errorf("after the drop: %v, want not_connected", err)
	}
}
