//go:build models

// Model tier for the daemon: the whole of chorusd over in-memory doubles,
// except the endpointer's judge, which is the real Smart Turn sidecar.
//
//	go test -tags=models ./cmd/chorusd/ -smartturn-url http://127.0.0.1:8891 -smartturn-wav /path/to/turn.wav
//
// The WAV is one finished turn, 16 kHz s16le mono, from outside the repo
// (CONTRIBUTING §7). It is streamed at the satellite's pace, so what the test
// measures is what a household would wait. Without both flags it skips.
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/provider/smartturn"
	"github.com/teaganglenn/chorus/internal/session"
)

var (
	smartTurnURL = flag.String("smartturn-url", "", "Smart Turn sidecar base URL; skips when empty")
	smartTurnWAV = flag.String("smartturn-wav", "", "one finished turn, 16 kHz s16le mono WAV; skips when empty")
)

// Alan asks the kitchen satellite for the lights in his own voice. With the
// real judge deciding, the answer waits on the pause and the verdict, not on
// 800 ms of quiet (ADR-0036).
//
// verifies SPEC §4.5, §11
func TestARealJudgeEndsTheTurnBeforeTheSilence(t *testing.T) {
	if *smartTurnURL == "" || *smartTurnWAV == "" {
		t.Skip("no -smartturn-url and -smartturn-wav")
	}
	take, err := readWAV(*smartTurnWAV)
	if err != nil {
		t.Fatalf("%s: %v", *smartTurnWAV, err)
	}
	judge, err := smartturn.New(smartturn.Config{BaseURL: *smartTurnURL})
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	answer := "Turning off the kitchen lights."
	r := newRig(t, inventory(), func(d *deps) {
		d.judge = judge
		d.engine = &scriptEngine{acts: []session.Action{
			session.SpeechDelta{CallID: "call_1", Text: answer, Last: true},
			session.TurnEnd{FinishReason: "stop", Completion: "{}"},
		}}
	})
	// The doubles key on the peak sample, so the take's peak is Alan saying it.
	p := peak(take)
	r.stt.mu.Lock()
	r.stt.by[p] = "turn off the kitchen lights"
	r.stt.mu.Unlock()
	r.emb.mu.Lock()
	r.emb.by[p] = alan
	r.emb.mu.Unlock()

	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	tick := time.NewTicker(32 * time.Millisecond)
	defer tick.Stop()
	for off := 0; off < len(take)+silence; off += chunkBytes {
		<-tick.C
		chunk := quiet(chunkBytes)
		if off < len(take) {
			chunk = take[off:min(off+chunkBytes, len(take))]
		}
		dev.SendMic(t, bridge.ChannelAEC, chunk)
	}

	dev.AwaitTTS(t, 2*len(answer))
	dev.PlayAll(t)
	start := r.store.awaitKind(t, journal.KindSpeechStarted, 1)
	wait, err := strconv.Atoi(start.Fields["wait_ms"])
	if err != nil {
		t.Fatalf("wait_ms = %q: %v", start.Fields["wait_ms"], err)
	}
	t.Logf("the answer started %d ms after the last loud chunk", wait)
	if wait >= 800 {
		t.Errorf("waited %d ms: the turn ended on the silence, not the judge", wait)
	}
}

// readWAV returns the data chunk of a canonical PCM WAV in the device's format.
func readWAV(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF WAVE file")
	}
	var fmtSeen bool
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:]))
		body := b[off+8 : min(off+8+size, len(b))]
		switch id {
		case "fmt ":
			if len(body) < 16 {
				return nil, errors.New("fmt chunk is short")
			}
			format, channels := binary.LittleEndian.Uint16(body), binary.LittleEndian.Uint16(body[2:])
			rate, bits := binary.LittleEndian.Uint32(body[4:]), binary.LittleEndian.Uint16(body[14:])
			if format != 1 || channels != 1 || rate != bridge.SampleRate || int(bits) != bridge.BitsPerSample {
				return nil, fmt.Errorf("format %d, %d ch, %d Hz, %d bit; want PCM mono %d Hz %d bit",
					format, channels, rate, bits, bridge.SampleRate, bridge.BitsPerSample)
			}
			fmtSeen = true
		case "data":
			if !fmtSeen {
				return nil, errors.New("data chunk before fmt chunk")
			}
			return body, nil
		}
		// Chunks are word-aligned; an odd size carries a pad byte.
		off += 8 + size + size%2
	}
	return nil, io.ErrUnexpectedEOF
}
