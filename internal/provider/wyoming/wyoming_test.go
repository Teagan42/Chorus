package wyoming

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/provider/pcm"
)

// server runs a scripted Wyoming server on loopback: handle reads what the
// client sent and answers. It returns the address to dial.
func server(t *testing.T, handle func(r *bufio.Reader, c net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				handle(bufio.NewReader(c), c)
			}()
		}
	}()
	return ln.Addr().String()
}

// until reads events until one of type stop, returning all of them.
func until(t *testing.T, r *bufio.Reader, stop string) []event {
	var evs []event
	for {
		ev, err := readEvent(r)
		if err != nil {
			t.Errorf("server read: %v", err)
			return evs
		}
		evs = append(evs, ev)
		if ev.Type == stop {
			return evs
		}
	}
}

func TestTranscriberStreamsTheUtteranceAndReturnsTheTranscript(t *testing.T) {
	got := make(chan []event, 1)
	addr := server(t, func(r *bufio.Reader, c net.Conn) {
		got <- until(t, r, "audio-stop")
		_ = writeEvent(c, event{Type: "transcript-start"})
		_ = writeEvent(c, event{Type: "transcript-chunk", Data: map[string]any{"text": "turn on"}})
		_ = writeEvent(c, event{Type: "transcript", Data: map[string]any{"text": "turn on the porch light"}})
	})
	tr, err := NewTranscriber(TranscriberConfig{Address: "tcp://" + addr, Model: "faster-whisper/small-int8", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	audio := make([]byte, 5000)
	for i := range audio {
		audio[i] = byte(i)
	}
	res, err := tr.Transcribe(context.Background(), audio)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "turn on the porch light" {
		t.Errorf("text = %q", res.Text)
	}
	evs := <-got
	if evs[0].Type != "transcribe" || evs[0].str("language") != "en" {
		t.Errorf("first event = %+v, want transcribe in en", evs[0])
	}
	if evs[1].Type != "audio-start" || evs[1].num("rate") != 16000 || evs[1].num("width") != 2 || evs[1].num("channels") != 1 {
		t.Errorf("second event = %+v, want audio-start in the device's format", evs[1])
	}
	var sent []byte
	for _, ev := range evs[2 : len(evs)-1] {
		if ev.Type != "audio-chunk" || len(ev.Payload) > chunkBytes {
			t.Errorf("chunk = %s of %d bytes", ev.Type, len(ev.Payload))
		}
		sent = append(sent, ev.Payload...)
	}
	if !bytes.Equal(sent, audio) {
		t.Error("the chunks are not the utterance")
	}
	if tr.Version() != "faster-whisper/small-int8" {
		t.Errorf("version = %q", tr.Version())
	}
}

func TestTranscriberReportsTheServersError(t *testing.T) {
	addr := server(t, func(r *bufio.Reader, c net.Conn) {
		until(t, r, "audio-stop")
		_ = writeEvent(c, event{Type: "error", Data: map[string]any{"text": "model not loaded", "code": "busy"}})
	})
	tr, _ := NewTranscriber(TranscriberConfig{Address: addr, Model: "m"})
	_, err := tr.Transcribe(context.Background(), []byte{0, 0})
	if err == nil || !strings.Contains(err.Error(), "model not loaded") || !strings.Contains(err.Error(), "busy") {
		t.Errorf("err = %v", err)
	}
}

// A server that never answers costs the utterance, not the session: the
// caller's context ends the exchange.
func TestTranscriberHonoursTheContext(t *testing.T) {
	addr := server(t, func(r *bufio.Reader, _ net.Conn) {
		until(t, r, "audio-stop")
		time.Sleep(2 * time.Second)
	})
	tr, _ := NewTranscriber(TranscriberConfig{Address: addr, Model: "m"})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := tr.Transcribe(ctx, []byte{0, 0})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

func TestTranscriberSendsNothingForSilence(t *testing.T) {
	// Nothing listens here; dialling would fail.
	tr, _ := NewTranscriber(TranscriberConfig{Address: "127.0.0.1:1", Model: "m"})
	if res, err := tr.Transcribe(context.Background(), nil); res.Text != "" || err != nil {
		t.Errorf("silence: %v, %v", res, err)
	}
	if _, err := tr.Transcribe(context.Background(), []byte{1}); err == nil {
		t.Error("a torn sample was sent")
	}
}

// Piper renders at 22.05 kHz in chunks; the clause comes out whole at the
// device's rate, and the voice asked for is the one named.
func TestSynthCollectsTheChunksAtTheDevicesRate(t *testing.T) {
	asked := make(chan event, 1)
	addr := server(t, func(r *bufio.Reader, c net.Conn) {
		ev, err := readEvent(r)
		if err != nil {
			t.Error(err)
			return
		}
		asked <- ev
		format := map[string]any{"rate": 22050, "width": 2, "channels": 1}
		_ = writeEvent(c, event{Type: "audio-start", Data: format})
		one := pcm.Encode(make([]int16, 11025))
		_ = writeEvent(c, event{Type: "audio-chunk", Data: format, Payload: one})
		_ = writeEvent(c, event{Type: "audio-chunk", Data: format, Payload: one})
		_ = writeEvent(c, event{Type: "audio-stop"})
	})
	s, err := NewSynth(SynthConfig{Address: addr, Voice: "en_US-lessac-medium"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Synthesize(context.Background(), "The timer is done.")
	if err != nil {
		t.Fatal(err)
	}
	// One second of 22.05 kHz is one second of 16 kHz.
	if len(out) != 2*16000 {
		t.Errorf("got %d bytes, want %d", len(out), 2*16000)
	}
	ev := <-asked
	voice, _ := ev.Data["voice"].(map[string]any)
	if ev.Type != "synthesize" || ev.str("text") != "The timer is done." || voice["name"] != "en_US-lessac-medium" {
		t.Errorf("asked %+v", ev)
	}
	if _, ok := voice["speaker"]; ok {
		t.Error("speaker sent though unset")
	}
	if s.Version() != "en_US-lessac-medium" {
		t.Errorf("version = %q", s.Version())
	}
}

func TestSynthRefusesAFormatItCannotPlay(t *testing.T) {
	addr := server(t, func(r *bufio.Reader, c net.Conn) {
		_, _ = readEvent(r)
		_ = writeEvent(c, event{Type: "audio-start", Data: map[string]any{"rate": 22050, "width": 4, "channels": 1}})
	})
	s, _ := NewSynth(SynthConfig{Address: addr, Voice: "v", Speaker: "p239"})
	if _, err := s.Synthesize(context.Background(), "hi"); err == nil || !strings.Contains(err.Error(), "4-byte") {
		t.Errorf("err = %v", err)
	}
	if s.Version() != "v/p239" {
		t.Errorf("version = %q", s.Version())
	}
}

// Data may come after the header rather than in it, as the protocol's own
// library writes it, or in both.
func TestReadEventMergesInlineAndTrailingData(t *testing.T) {
	in := `{"type":"transcript","data":{"a":1},"data_length":13,"payload_length":3}` + "\n" + `{"text":"hi"}` + "xyz"
	ev, err := readEvent(bufio.NewReader(strings.NewReader(in)))
	if err != nil {
		t.Fatal(err)
	}
	if ev.str("text") != "hi" || ev.num("a") != 1 || string(ev.Payload) != "xyz" {
		t.Errorf("event = %+v", ev)
	}
	big := `{"type":"audio-chunk","payload_length":999999999}` + "\n"
	if _, err := readEvent(bufio.NewReader(strings.NewReader(big))); err == nil {
		t.Error("a gigabyte payload was accepted")
	}
}

func TestProbeWantsInfo(t *testing.T) {
	addr := server(t, func(r *bufio.Reader, c net.Conn) {
		if ev, _ := readEvent(r); ev.Type == "describe" {
			_ = writeEvent(c, event{Type: "info", Data: map[string]any{"asr": []any{}}})
		}
	})
	if err := Probe(addr)(context.Background()); err != nil {
		t.Errorf("probe: %v", err)
	}
	if err := Probe("127.0.0.1:1")(context.Background()); err == nil {
		t.Error("probe of nothing passed")
	}
}

func TestNewRefusesBadWiring(t *testing.T) {
	if _, err := NewTranscriber(TranscriberConfig{Address: "whisper", Model: "m"}); err == nil {
		t.Error("an address without a port was accepted")
	}
	if _, err := NewTranscriber(TranscriberConfig{Address: "h:1"}); err == nil {
		t.Error("an unnamed model was accepted")
	}
	if _, err := NewSynth(SynthConfig{Address: "h:1"}); err == nil {
		t.Error("an unnamed voice was accepted")
	}
}
