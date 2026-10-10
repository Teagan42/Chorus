package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/bridge/bridgetest"
	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/pb"
	"github.com/teagan42/chorus/internal/session"
)

// The inventory every rig runs with. Addresses are the native API's, as
// devices.yaml records them; the audio link matches on the host alone.
const (
	kitchenIP = "10.0.0.11"
	officeIP  = "10.0.0.12"
	goodPSK   = "seJlx7BCna54FicYF6Sg4xUB1y+8cUpFHJRm9+eKf2c="
)

func inventory() *config.Config {
	return &config.Config{Satellites: []config.Satellite{
		{Name: "kitchen", Address: kitchenIP + ":6053", PSK: goodPSK, Room: "kitchen", Profile: "satellite1"},
		{Name: "office", Address: officeIP + ":6053", PSK: goodPSK, Room: "office", Profile: "voice-pe"},
	}}
}

// Voices. alan is enrolled; the stranger is the television.
var (
	alan     = axis(0)
	stranger = axis(3)
)

// silence is enough quiet to end an utterance under the listener's defaults.
const silence = listen.DefaultSilence + chunkBytes

// rig is the daemon running over in-memory doubles: every dependency that
// reads time or does I/O is injected (CONTRIBUTING §1).
type rig struct {
	ln     *memListener
	store  *spyStore
	blobs  *blob.Memory
	engine *scriptEngine
	stt    *fakeSTT
	emb    *fakeEmbedder
	logs   *logBuffer
	cancel context.CancelFunc
	exited chan error
	lines  int16
}

func newRig(t *testing.T, inv *config.Config, tweak ...func(*deps)) *rig {
	t.Helper()
	r := &rig{
		ln: newListener(), store: newSpyStore(), blobs: blob.NewMemory(),
		engine: &scriptEngine{acts: []session.Action{session.TurnEnd{FinishReason: "stop", Completion: "{}"}}},
		stt:    newSTT(), emb: newEmbedder(), logs: &logBuffer{}, exited: make(chan error, 1),
	}
	d := deps{
		Listener: r.ln,
		Store:    r.store,
		Blobs:    r.blobs,
		Clock:    journal.FixedClock(epoch),
		Timers:   neverTimers{},
		providers: providers{
			engine:    r.engine,
			versions:  journal.Versions{Model: "qwen3-32b", Prompt: "p1", ToolSchema: "t1"},
			synth:     silentSynth{},
			stt:       r.stt,
			speakers:  household(t, r.emb),
			household: []string{"alan"},
			tools:     map[string]session.Tool{},
		},
		// Debug, so a test can wait on what the endpointer judged.
		Log: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, f := range tweak {
		f(&d)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() {
		r.exited <- run(ctx, inv, d)
		close(r.exited)
	}()
	t.Cleanup(func() {
		cancel()
		_ = r.exit(t)
	})
	return r
}

// line scripts what a voice says and returns the amplitude that speaks it.
func (r *rig) line(text string, who []float32) int16 {
	amp := int16(8000) + r.lines
	r.lines++
	r.stt.mu.Lock()
	r.stt.by[amp] = text
	r.stt.mu.Unlock()
	r.emb.mu.Lock()
	r.emb.by[amp] = who
	r.emb.mu.Unlock()
	return amp
}

// join connects a device from ip and returns it with its own end of the link.
func (r *rig) join(t *testing.T, ip string) *bridgetest.Device {
	t.Helper()
	return bridgetest.Connect(r.ln.dial(t, ip), 2)
}

// utter is a whole utterance on the AEC channel: speech, then enough quiet
// to end it under the listener's defaults.
func (r *rig) utter(t *testing.T, dev *bridgetest.Device, amplitude int16) {
	t.Helper()
	for range 8 {
		dev.SendMic(t, bridge.ChannelAEC, voice(amplitude, chunkBytes))
	}
	for sent := 0; sent < silence; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}
}

// judged waits until the endpointer has taken n verdicts from Smart Turn,
// counted across every link, so the quiet a test sends next lands after one.
func (r *rig) judged(t *testing.T, n int) {
	t.Helper()
	await(t, fmt.Sprintf("Smart Turn's verdict on pause %d", n), func() bool {
		return strings.Count(r.logs.String(), "semantic endpointing judged the pause") >= n
	})
}

// exit waits for run to return. The channel is closed after the result, so
// a second wait sees a run that has already exited rather than blocking.
func (r *rig) exit(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.exited:
		return err
	case <-time.After(patience):
		t.Fatal("run did not return")
		return nil
	}
}

func gone(t *testing.T, dev *bridgetest.Device, what string) {
	t.Helper()
	select {
	case <-dev.Gone():
	case <-time.After(patience):
		t.Fatalf("%s: the device was never hung up on", what)
	}
}

// The inventory is the authority on which devices exist (SPEC §13), and the
// audio link's hello carries no name, so the source address is what a
// satellite is known by. Anything else dialing the audio port is refused
// before a frame is read, and the refusal is logged with the address.
//
// verifies SPEC §13
func TestAnAddressNotInTheInventoryIsRefused(t *testing.T) {
	r := newRig(t, inventory())

	dev := r.join(t, "10.0.0.99")
	gone(t, dev, "an unknown address")
	r.logs.await(t, "10.0.0.99")
	if !strings.Contains(r.logs.String(), "inventory") {
		t.Errorf("the refusal does not say why:\n%s", r.logs.String())
	}
	if n := len(r.store.events()); n != 0 {
		t.Errorf("%d events journalled for a refused connection", n)
	}

	// The daemon is still accepting.
	known := r.join(t, kitchenIP)
	r.logs.await(t, "satellite connected")
	select {
	case <-known.Gone():
		t.Fatal("a known device was hung up on after the refusal")
	default:
	}
}

// A device dials in, a wake word and an utterance follow, and the journal
// records the session opening on that satellite for that speaker before the
// utterance it opened on: the whole stack, device to log, composed (SPEC §2).
//
// verifies SPEC §2, §4.5
func TestAKnownSatelliteIsHeard(t *testing.T) {
	r := newRig(t, inventory())
	dev := r.join(t, kitchenIP)
	zep := r.line("find zeppelin", alan)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, zep)

	opened := r.store.awaitKind(t, journal.KindSessionOpened, 1)
	if opened.Fields["satellite"] != "kitchen" || opened.Fields["speaker_id"] != "alan" {
		t.Errorf("session_opened = %v, want kitchen/alan", opened.Fields)
	}
	heard := r.store.awaitKind(t, journal.KindUtteranceTranscribed, 1)
	if heard.ConversationID != opened.ConversationID {
		t.Errorf("utterance in %s, session in %s", heard.ConversationID, opened.ConversationID)
	}
	if heard.Fields["text"] != "find zeppelin" || heard.Fields["speaker_id"] != "alan" {
		t.Errorf("utterance_transcribed = %v", heard.Fields)
	}
	if opened.Seq >= heard.Seq {
		t.Errorf("session_opened at seq %d, utterance at %d: wrong order", opened.Seq, heard.Seq)
	}
	if _, ok := r.blobs.Bytes(heard.AudioRef); !ok {
		t.Errorf("the utterance's audio %q is not in the blob store", heard.AudioRef)
	}
	await(t, "the turn", func() bool { return len(r.engine.heard()) == 1 })
	if in := r.engine.heard()[0]; in.Text != "find zeppelin" || in.Speaker != "alan" {
		t.Errorf("the model was given %+v", in)
	}
}

// The inventory's room reaches the model: a satellite named for its board
// stands in the living room, and "dim the lights" heard there is asked with
// that room, recorded on the session so a replay is asked the same (SPEC §5).
//
// verifies SPEC §5
func TestTheModelIsToldTheRoomTheInventoryNames(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{
		{Name: "satellite1-4b2c10", Address: kitchenIP + ":6053", PSK: goodPSK, Room: "living_room", Profile: "satellite1"},
	}}
	r := newRig(t, inv)
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("dim the lights", alan))

	opened := r.store.awaitKind(t, journal.KindSessionOpened, 1)
	if opened.Fields["satellite"] != "satellite1-4b2c10" || opened.Fields["room"] != "living_room" {
		t.Errorf("session_opened = %v, want satellite1-4b2c10 in the living_room", opened.Fields)
	}
	await(t, "the turn", func() bool { return len(r.engine.heard()) == 1 })
	if in := r.engine.heard()[0]; in.Room != "living_room" || in.Text != "dim the lights" {
		t.Errorf("the model was given %+v, want dim the lights from the living_room", in)
	}
}

// The whole stack keeps the satellite's second channel: what the device
// sends on it lands in the blob store beside the utterance, and the journal
// says where (SPEC §8).
//
// verifies SPEC §8
func TestTheSecondMicChannelIsJournalledBesideTheFirst(t *testing.T) {
	r := newRig(t, inventory())
	dev := r.join(t, kitchenIP)
	kettle := r.line("put the kettle on", alan)
	const lighter = 2900

	dev.SendWake(t, "hey_eddie")
	for range 8 {
		dev.SendMic(t, bridge.ChannelAEC, voice(kettle, chunkBytes))
		dev.SendMic(t, bridge.ChannelSecond, voice(lighter, chunkBytes))
	}
	for sent := 0; sent < silence; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
		dev.SendMic(t, bridge.ChannelSecond, quiet(chunkBytes))
	}

	heard := r.store.awaitKind(t, journal.KindUtteranceTranscribed, 1)
	second, ok := r.blobs.Bytes(heard.Fields["second_audio_ref"])
	if !ok {
		t.Fatalf("second_audio_ref %q is not in the blob store", heard.Fields["second_audio_ref"])
	}
	if !bytes.Contains(second, voice(lighter, 8*chunkBytes)) {
		t.Errorf("the second channel's blob (%d bytes) does not hold what the device sent on it", len(second))
	}
}

// The link's lifetime owns the listener: when the device drops, its session
// closes as device_lost. The conversation does not end with it -- the same
// person on a fresh link resumes it (SPEC §4.5), which is what the one
// shared Conversations across per-link supervisors exists for (ADR-0022).
//
// verifies SPEC §4.5
func TestADroppedLinkClosesTheSessionAndAReconnectResumesIt(t *testing.T) {
	r := newRig(t, inventory())
	first := r.ln.dial(t, kitchenIP)
	dev := bridgetest.Connect(first, 2)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("find zeppelin", alan))
	opened := r.store.awaitKind(t, journal.KindSessionOpened, 1)
	r.store.awaitKind(t, journal.KindUtteranceTranscribed, 1)

	_ = first.Close()
	closed := r.store.awaitKind(t, journal.KindSessionClosed, 1)
	if closed.Fields["reason"] != "device_lost" || closed.Fields["satellite"] != "kitchen" {
		t.Errorf("session_closed = %v, want device_lost on kitchen", closed.Fields)
	}
	r.logs.await(t, "satellite disconnected")

	again := r.join(t, kitchenIP)
	again.SendWake(t, "hey_eddie")
	r.utter(t, again, r.line("and the bedroom", alan))
	resumed := r.store.awaitKind(t, journal.KindSessionOpened, 2)
	if resumed.ConversationID != opened.ConversationID || resumed.Fields["resumed"] != "true" {
		t.Errorf("second open = %v in %s, want resumed in %s", resumed.Fields, resumed.ConversationID, opened.ConversationID)
	}
}

// Shutdown is a cancel: every link closes, every session the devices carried
// ends as device_lost, and run returns only once every goroutine it started
// has exited (SPEC §4, CONTRIBUTING §6).
//
// verifies SPEC §4
func TestCancelClosesEveryLinkAndReturns(t *testing.T) {
	r := newRig(t, inventory())
	kitchen := r.join(t, kitchenIP)
	office := r.join(t, officeIP)
	kitchen.SendWake(t, "hey_eddie")
	r.utter(t, kitchen, r.line("find zeppelin", alan))
	r.store.awaitKind(t, journal.KindUtteranceTranscribed, 1)

	r.cancel()
	if err := r.exit(t); err != nil {
		t.Errorf("run returned %v on cancel, want nil", err)
	}
	gone(t, kitchen, "kitchen")
	gone(t, office, "office")
	closed := r.store.awaitKind(t, journal.KindSessionClosed, 1)
	if closed.Fields["reason"] != "device_lost" {
		t.Errorf("close reason = %q, want device_lost", closed.Fields["reason"])
	}
}

// A guest wakes the house: nobody enrolled is the fresh install, and the
// session opens with no speaker (SPEC §5). The shared stack treats the two
// satellites alike.
//
// verifies SPEC §5
func TestAGuestOpensASessionOnAnySatellite(t *testing.T) {
	r := newRig(t, inventory(), func(d *deps) { d.speakers, d.household = nil, nil })
	dev := r.join(t, officeIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("what time is it", stranger))

	opened := r.store.awaitKind(t, journal.KindSessionOpened, 1)
	if opened.Fields["satellite"] != "office" || opened.Fields["speaker_id"] != "" {
		t.Errorf("session_opened = %v, want office for a guest", opened.Fields)
	}
}

// The audio link identifies a device by source address, so two satellites
// on one host could not be told apart and a hostname could not be matched
// at all. Both are startup errors that name the satellite.
func TestRunRefusesAnInventoryItCannotMatchDevicesAgainst(t *testing.T) {
	cases := map[string]*config.Config{
		"shared host": {Satellites: []config.Satellite{
			{Name: "kitchen", Address: kitchenIP + ":6053", PSK: goodPSK},
			{Name: "pantry", Address: kitchenIP + ":6054", PSK: goodPSK},
		}},
		"hostname": {Satellites: []config.Satellite{
			{Name: "kitchen", Address: "kitchen.local:6053", PSK: goodPSK},
		}},
	}
	for name, inv := range cases {
		t.Run(name, func(t *testing.T) {
			err := run(t.Context(), inv, deps{
				Listener: newListener(), Store: newSpyStore(), Blobs: blob.NewMemory(),
				Clock: journal.FixedClock(epoch), Timers: neverTimers{},
				providers: providers{engine: &scriptEngine{}, synth: silentSynth{}, stt: newSTT()},
				Log:       slog.New(slog.DiscardHandler),
			})
			if err == nil || !strings.Contains(err.Error(), "kitchen") {
				t.Errorf("err = %v, want the satellite named", err)
			}
		})
	}
}

// The native API is held open per satellite: the device's api component
// reboots the board when no client is connected for its reboot_timeout, and
// its keepalive drops a client that does not answer pings. The daemon dials
// with the inventory's PSK, logs what the device says it is, answers pings,
// and redials with backoff when the connection fails or drops (SPEC §3.1).
//
// verifies SPEC §3.1
func TestTheNativeAPIIsHeldAndRedialed(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[0]}}
	dialer := newDialer()
	clk := newClock()
	r := newRig(t, inv, func(d *deps) {
		d.Native, d.Clock, d.Timers = dialer, clk, clk
	})

	// Offline at startup: the audio link is independent, so this is a retry,
	// not a failed start.
	dialer.results <- dialResult{err: errors.New("connection refused")}
	r.logs.await(t, "native api unreachable")
	clk.awaitWait(t, nativeRetryMin)
	if got := dialer.dialed(); len(got) != 1 || got[0] != kitchenIP+":6053 "+goodPSK {
		t.Fatalf("dialed %q, want the inventory's address and psk", got)
	}

	conn := newNative()
	dialer.results <- dialResult{conn: conn}
	clk.advance(nativeRetryMin)
	r.logs.await(t, "satellite1 esphome 2026.7.2")

	conn.in <- &pb.PingRequest{}
	select {
	case m := <-conn.sent:
		if _, ok := m.(*pb.PingResponse); !ok {
			t.Errorf("answered a ping with %T", m)
		}
	case <-time.After(patience):
		t.Fatal("the ping was never answered")
	}

	// The device drops the connection; the daemon arms a retry.
	_ = conn.Close()
	r.logs.await(t, "native api dropped")
	await(t, "a second retry", func() bool {
		clk.mu.Lock()
		defer clk.mu.Unlock()
		for _, tm := range clk.timers {
			if tm.waited == nativeRetryMin && tm.deadline.After(epoch.Add(nativeRetryMin)) {
				return true
			}
		}
		return false
	})

	r.cancel()
	if err := r.exit(t); err != nil {
		t.Errorf("run returned %v on cancel", err)
	}
}

// The whole stack measures the wait: the listener stamps when speech stopped
// with the daemon's clock, and the satellite's first PLAYED report past the
// answer's start journals it (ADR-0035). The clock is fixed, so the wait is
// exactly the default endpointer's 800 ms of quiet; a daemon that left the
// listener without a clock would record none.
//
// verifies SPEC §11
func TestTheFirstPlayedFrameIsJournalledWithItsWait(t *testing.T) {
	answer := "Turning off the kitchen lights."
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = &scriptEngine{acts: []session.Action{
			session.SpeechDelta{CallID: "call_1", Text: answer, Last: true},
			session.TurnEnd{FinishReason: "stop", Completion: "{}"},
		}}
	})
	dev := r.join(t, kitchenIP)
	lights := r.line("turn off the kitchen lights", alan)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, lights)

	dev.AwaitTTS(t, 2*len(answer))
	dev.PlayAll(t)
	start := r.store.awaitKind(t, journal.KindSpeechStarted, 1)
	if start.Fields["call_id"] != "call_1" || start.Fields["wait_ms"] != "800" {
		t.Errorf("speech_started = %v, want call_1 with the wait measured", start.Fields)
	}
	spoken := r.store.awaitKind(t, journal.KindSpeechSpoken, 1)
	if start.Seq >= spoken.Seq {
		t.Errorf("speech_started at seq %d, speech_spoken at %d: wrong order", start.Seq, spoken.Seq)
	}
}

// With SMARTTURN_URL set every link endpoints semantically: Alan's answer
// waits on the short pause Smart Turn is asked after, not the 800 ms the
// test above records without it (ADR-0036). The test sends audio faster than
// it was spoken, on a clock that does not move, so the quiet after the
// verdict waits for it, as it does after a stalled radio's burst.
//
// verifies SPEC §4.5, §11
func TestSmartTurnShortensTheWaitForAnAnswer(t *testing.T) {
	answer := "Turning off the kitchen lights."
	j := &turnJudge{}
	r := newRig(t, inventory(), func(d *deps) {
		d.judge = j
		d.engine = &scriptEngine{acts: []session.Action{
			session.SpeechDelta{CallID: "call_1", Text: answer, Last: true},
			session.TurnEnd{FinishReason: "stop", Completion: "{}"},
		}}
	})
	dev := r.join(t, kitchenIP)
	lights := r.line("turn off the kitchen lights", alan)

	dev.SendWake(t, "hey_eddie")
	for range 8 {
		dev.SendMic(t, bridge.ChannelAEC, voice(lights, chunkBytes))
	}
	sent := 0
	for ; sent < listen.DefaultPause; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}
	r.judged(t, 1)
	for ; sent < silence; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}

	dev.AwaitTTS(t, 2*len(answer))
	dev.PlayAll(t)
	start := r.store.awaitKind(t, journal.KindSpeechStarted, 1)
	wait, err := strconv.Atoi(start.Fields["wait_ms"])
	if err != nil {
		t.Fatalf("wait_ms = %q: %v", start.Fields["wait_ms"], err)
	}
	if wait < 200 || wait >= 800 {
		t.Errorf("waited %d ms, want the pause the judge was asked after, under Energy's 800", wait)
	}
}

// The kitchen satellite's radio stalls while Alan thinks after "set a timer
// for", then delivers the second of quiet it buffered in one burst. Smart
// Turn has not answered yet: by the audio, Energy's 800 ms are long gone, but
// by the clock the judge has had no time at all. The turn waits for it, and
// the model is still asked once, for the whole command.
//
// verifies SPEC §4.5
func TestABurstAfterARadioStallWaitsForSmartTurn(t *testing.T) {
	answer := "Twelve minutes, starting now."
	j := &turnJudge{gate: make(chan struct{})}
	engine := &scriptEngine{acts: []session.Action{
		session.SpeechDelta{CallID: "call_1", Text: answer, Last: true},
		session.TurnEnd{FinishReason: "stop", Completion: "{}"},
	}}
	r := newRig(t, inventory(), func(d *deps) {
		d.judge = j
		d.engine = engine
	})
	dev := r.join(t, kitchenIP)
	cut := r.line("Set a timer for", alan)
	whole := r.line("Set a timer for twelve minutes.", alan)

	dev.SendWake(t, "hey_eddie")
	for range 8 {
		dev.SendMic(t, bridge.ChannelAEC, voice(cut, chunkBytes))
	}
	// The burst: the pause and the second of thought behind it, all at once.
	for sent := 0; sent < listen.DefaultPause+2*bridge.SampleRate; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}
	close(j.gate)
	r.judged(t, 1)
	for range 8 {
		dev.SendMic(t, bridge.ChannelAEC, voice(whole, chunkBytes))
	}
	sent := 0
	for ; sent < listen.DefaultPause; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}
	r.judged(t, 2)
	for ; sent < silence; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}

	dev.AwaitTTS(t, 2*len(answer))
	dev.PlayAll(t)
	r.store.awaitKind(t, journal.KindSpeechStarted, 1)
	if heard := engine.heard(); len(heard) != 1 || heard[0].Text != "Set a timer for twelve minutes." {
		var said []string
		for _, in := range heard {
			said = append(said, in.Text)
		}
		t.Errorf("the model was asked %q, want the whole command once", said)
	}
	if n := len(r.store.ofKind(journal.KindUtteranceTranscribed)); n != 1 {
		t.Errorf("%d utterances transcribed, want one", n)
	}
}

// Alan asks the kitchen satellite to "set a timer for", stops for a second to
// think, then says "twelve minutes". Smart Turn hears the first pause as
// finished, as it did on every voice in the corpus (ADR-0036); the daemon
// reads the words with the same transcriber, holds the turn past Energy's
// 800 ms, and the model is asked once, for the whole command (ADR-0042).
//
// verifies SPEC §4.5
func TestACutOffCommandReachesTheModelWhole(t *testing.T) {
	answer := "Twelve minutes, starting now."
	j := &turnJudge{}
	engine := &scriptEngine{acts: []session.Action{
		session.SpeechDelta{CallID: "call_1", Text: answer, Last: true},
		session.TurnEnd{FinishReason: "stop", Completion: "{}"},
	}}
	r := newRig(t, inventory(), func(d *deps) {
		d.judge = j
		d.engine = engine
	})
	dev := r.join(t, kitchenIP)
	cut := r.line("Set a timer for", alan)
	// The doubles decode the loudest voice, so the whole turn reads as this.
	whole := r.line("Set a timer for twelve minutes.", alan)

	dev.SendWake(t, "hey_eddie")
	for range 8 {
		dev.SendMic(t, bridge.ChannelAEC, voice(cut, chunkBytes))
	}
	sent := 0
	for ; sent < listen.DefaultPause; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}
	r.judged(t, 1)
	// A second of thought, past the 800 ms Energy would have ended it at.
	for ; sent < listen.DefaultPause+2*bridge.SampleRate; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}
	for range 8 {
		dev.SendMic(t, bridge.ChannelAEC, voice(whole, chunkBytes))
	}
	for sent = 0; sent < listen.DefaultPause; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}
	r.judged(t, 2)
	for ; sent < silence; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}

	dev.AwaitTTS(t, 2*len(answer))
	dev.PlayAll(t)
	r.store.awaitKind(t, journal.KindSpeechStarted, 1)
	heard := engine.heard()
	if len(heard) != 1 || heard[0].Text != "Set a timer for twelve minutes." {
		var said []string
		for _, in := range heard {
			said = append(said, in.Text)
		}
		t.Errorf("the model was asked %q, want the whole command once", said)
	}
	if n := len(r.store.ofKind(journal.KindUtteranceTranscribed)); n != 1 {
		t.Errorf("%d utterances transcribed, want one", n)
	}
	if j.asks() != 2 {
		t.Errorf("Smart Turn judged %d pauses, want the cut and the end", j.asks())
	}
}
