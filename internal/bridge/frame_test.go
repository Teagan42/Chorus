package bridge_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/bridge"
)

func TestFrameRoundTrip(t *testing.T) {
	want := []bridge.Frame{
		{Type: bridge.TypeMic, Flags: bridge.ChannelAEC, Payload: []byte{0x01, 0x02}},
		{Type: bridge.TypeStop},
		{Type: bridge.TypeWake, Payload: []byte("hey_eddie")},
	}

	var buf bytes.Buffer
	w := bridge.NewWriter(&buf)
	for _, f := range want {
		if err := w.WriteFrame(f); err != nil {
			t.Fatalf("WriteFrame(%v): %v", f.Type, err)
		}
	}

	r := bridge.NewReader(&buf)
	for _, exp := range want {
		got, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if got.Type != exp.Type || got.Flags != exp.Flags {
			t.Errorf("header = %v/%v, want %v/%v", got.Type, got.Flags, exp.Type, exp.Flags)
		}
		if !bytes.Equal(got.Payload, exp.Payload) {
			t.Errorf("payload = %x, want %x", got.Payload, exp.Payload)
		}
	}
	if _, err := r.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Errorf("after last frame err = %v, want io.EOF", err)
	}
}

// Header is fixed at 4 bytes so the firmware can parse it without a decoder.
func TestFrameWireLayout(t *testing.T) {
	var buf bytes.Buffer
	w := bridge.NewWriter(&buf)
	if err := w.WriteFrame(bridge.Frame{
		Type:    bridge.TypeMic,
		Flags:   bridge.ChannelRaw,
		Payload: []byte{0xaa, 0xbb, 0xcc},
	}); err != nil {
		t.Fatal(err)
	}
	want := []byte{byte(bridge.TypeMic), 0x01, 0x00, 0x03, 0xaa, 0xbb, 0xcc}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("wire = % x, want % x", buf.Bytes(), want)
	}
}

// An unknown type must be skipped by length, so a newer firmware can add
// frames without breaking an older host.
func TestReadFrameSkipsUnknownTypeByLength(t *testing.T) {
	wire := []byte{0x7f, 0x00, 0x00, 0x02, 0xde, 0xad, byte(bridge.TypeStop), 0x00, 0x00, 0x00}

	r := bridge.NewReader(bytes.NewReader(wire))
	unknown, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(unknown): %v", err)
	}
	if unknown.Type != 0x7f {
		t.Errorf("type = %v, want 0x7f", unknown.Type)
	}
	next, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(after unknown): %v", err)
	}
	if next.Type != bridge.TypeStop {
		t.Errorf("type = %v, want TypeStop", next.Type)
	}
}

func TestWriteFrameRejectsOversizePayload(t *testing.T) {
	w := bridge.NewWriter(io.Discard)
	err := w.WriteFrame(bridge.Frame{Type: bridge.TypeTTS, Payload: make([]byte, 65536)})
	if err == nil {
		t.Fatal("WriteFrame(65536 bytes) = nil, want error: length field is uint16")
	}
}

// A truncated payload must not be reported as a short frame.
func TestReadFrameTruncatedPayload(t *testing.T) {
	r := bridge.NewReader(bytes.NewReader([]byte{byte(bridge.TypeMic), 0x00, 0x00, 0x04, 0x01}))
	if _, err := r.ReadFrame(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestHelloRoundTrip(t *testing.T) {
	want := bridge.Hello{Version: bridge.ProtocolVersion, SampleRate: 16000, BitsPerSample: 16, MicChannels: 2}

	got, err := bridge.ParseHello(want.Frame().Payload)
	if err != nil {
		t.Fatalf("ParseHello: %v", err)
	}
	if got != want {
		t.Errorf("Hello = %+v, want %+v", got, want)
	}
}

func TestParseHelloRejectsForeignVersion(t *testing.T) {
	for name, version := range map[string]uint8{
		"future": bridge.ProtocolVersion + 1,
		// A device still on version 1 would ignore the stop tag and never
		// answer a stop, so the host would wait out every settle and record a
		// cut a round trip old. Both halves live in one checkout (ADR-0017);
		// the error has to say which half is stale.
		"stale flash": 1,
	} {
		t.Run(name, func(t *testing.T) {
			h := bridge.Hello{Version: version, SampleRate: 16000, BitsPerSample: 16, MicChannels: 2}
			_, err := bridge.ParseHello(h.Frame().Payload)
			if err == nil {
				t.Fatal("ParseHello = nil, want error")
			}
			if !strings.Contains(err.Error(), "flash") {
				t.Errorf("err = %q, want it to say the firmware is what to update", err)
			}
		})
	}
}

// verifies SPEC §3.2.1
func TestParsePlayedCarriesCumulativeFramesAndTimestamp(t *testing.T) {
	// 16000 frames at 16 kHz is exactly 1 s of audio the user has heard.
	want := bridge.Played{Frames: 16000, TimestampMicros: -1}

	f := want.Frame()
	got, err := bridge.ParsePlayed(f.Flags, f.Payload)
	if err != nil {
		t.Fatalf("ParsePlayed: %v", err)
	}
	if got != want {
		t.Fatalf("Played = %+v, want %+v", got, want)
	}
	if pos := got.Position(16000); pos.Seconds() != 1 {
		t.Errorf("Position(16000) = %v, want 1s", pos)
	}
}

// verifies SPEC §4.4
//
// The report that answers a stop is told from a routine one on the wire, in
// the flags byte, so the host never has to infer which report followed the
// stop from when it arrived. The firmware reads the tag with one byte load
// and writes it back the same way.
func TestPlayedEchoesTheStopItAnswersInTheFlagsByte(t *testing.T) {
	want := bridge.Played{Frames: 0x0102030405060708, TimestampMicros: 0x1112131415161718, Stop: 0x2a}

	var buf bytes.Buffer
	if err := bridge.NewWriter(&buf).WriteFrame(want.Frame()); err != nil {
		t.Fatal(err)
	}
	wire := []byte{
		byte(bridge.TypePlayed), 0x2a, 0x00, 0x10,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
	}
	if !bytes.Equal(buf.Bytes(), wire) {
		t.Errorf("wire = % x, want % x", buf.Bytes(), wire)
	}

	f, err := bridge.NewReader(&buf).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	got, err := bridge.ParsePlayed(f.Flags, f.Payload)
	if err != nil {
		t.Fatalf("ParsePlayed: %v", err)
	}
	if got != want {
		t.Errorf("Played = %+v, want %+v", got, want)
	}
	// A routine report has no stop to answer, and the zero flags byte says so.
	if routine := (bridge.Played{Frames: 1}).Frame(); routine.Flags != 0 {
		t.Errorf("routine report flags = %#02x, want 0", routine.Flags)
	}
}

func TestParsePlayedRejectsShortPayload(t *testing.T) {
	if _, err := bridge.ParsePlayed(0, make([]byte, 15)); err == nil {
		t.Fatal("ParsePlayed(15 bytes) = nil, want error")
	}
}

func TestDuckRoundTrip(t *testing.T) {
	want := bridge.Duck{Decibels: 20, DurationMillis: 500}

	got, err := bridge.ParseDuck(want.Frame().Payload)
	if err != nil {
		t.Fatalf("ParseDuck: %v", err)
	}
	if got != want {
		t.Errorf("Duck = %+v, want %+v", got, want)
	}
}

// Hardware mute is authoritative: the host may never clear it, so it travels
// as its own bit and is reported, not negotiated.
func TestParseMuteSeparatesHardwareFromSoftware(t *testing.T) {
	for _, tc := range []bridge.Mute{
		{},
		{Hardware: true},
		{Software: true},
		{Hardware: true, Software: true},
	} {
		f := tc.Frame()
		got := bridge.ParseMute(f.Flags)
		if got != tc {
			t.Errorf("ParseMute(%08b) = %+v, want %+v", f.Flags, got, tc)
		}
	}
}
