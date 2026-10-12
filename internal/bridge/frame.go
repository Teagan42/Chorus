// Package bridge is the host end of the chorus_bridge audio protocol: a raw
// TCP stream the satellite dials out on, carrying full-duplex audio that the
// ESPHome native API cannot carry without forking it (SPEC §3.1).
package bridge

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"
)

// ProtocolVersion is bumped on any incompatible frame change. The firmware
// states it in the opening Hello so a mismatched pair fails loudly.
//
// Version 2 tags each STOP and has the device echo the tag on the PLAYED
// report it emits once the stop has taken effect (ADR-0033). A version 1
// device ignores the tag and answers nothing, so the host would wait out
// every settle and then record a cut a round trip old; refusing the link is
// what makes a stale flash surface as an error instead.
//
// Version 3 authenticated the device before any audio (ADR-0066).
//
// Version 4 authenticates both ends and seals every frame after the
// handshake (ADR-0074). A version 3 device sends no nonce and cannot open a
// sealed frame, so it is refused at its hello.
const ProtocolVersion = 4

// Audio format is fixed by the device: the XMOS pipeline and micro_wake_word
// both run at 16 kHz, and resampling on an ESP32 buys nothing (SPEC §3.2).
const (
	SampleRate    = 16000
	BitsPerSample = 16
)

// HeaderSize is fixed so the firmware parses a header with four byte loads.
// Only the three handshake frames travel under it; every frame after them is
// sealed (seal.go).
const HeaderSize = 4

// MaxPayload is exclusive: the length field is a uint16.
const MaxPayload = 1 << 16

// Type identifies a frame. Device-to-host types are below 0x10, host-to-device
// at or above, so a misdirected frame is obvious in a capture.
type Type uint8

const (
	TypeHello  Type = 0x01 // device: protocol version, audio format, its nonce and name
	TypeMic    Type = 0x02 // device: uplink PCM, Flags carries the channel
	TypeWake   Type = 0x03 // device: micro_wake_word fired, payload is its name
	TypePlayed Type = 0x04 // device: DAC playback position (SPEC §3.2.1); Flags echoes a STOP tag
	TypeMute   Type = 0x05 // device: mute state changed, Flags carries it
	TypeAuth   Type = 0x06 // device: its proof over the handshake

	TypeTTS       Type = 0x10 // host: downlink PCM
	TypeStop      Type = 0x11 // host: barge-in, discard the speaker buffer; Flags carries a tag
	TypeFinish    Type = 0x12 // host: utterance ended, drain the buffer
	TypeDuck      Type = 0x13 // host: mixer ducking
	TypeMicEnable Type = 0x14 // host: start or stop the uplink
	TypeChallenge Type = 0x15 // host: its nonce and its proof over the handshake
)

// WakeButton is the name a TypeWake carries when a person pressed the
// satellite's push-to-talk button rather than said the wake word: the
// Voice PE's centre button calls the same hook micro_wake_word does.
const WakeButton = "button"

// Mic channel, in Frame.Flags on TypeMic.
const (
	ChannelAEC    uint8 = 0 // XMOS fully processed: AEC, IC, NS, AGC
	ChannelSecond uint8 = 1 // XMOS second output; its processing is the firmware's setting (SPEC §3.2)
)

// Mute bits, in Frame.Flags on TypeMute.
const (
	muteHardware uint8 = 1 << 0
	muteSoftware uint8 = 1 << 1
)

func (t Type) String() string {
	switch t {
	case TypeHello:
		return "hello"
	case TypeMic:
		return "mic"
	case TypeWake:
		return "wake"
	case TypePlayed:
		return "played"
	case TypeMute:
		return "mute"
	case TypeAuth:
		return "auth"
	case TypeTTS:
		return "tts"
	case TypeStop:
		return "stop"
	case TypeFinish:
		return "finish"
	case TypeDuck:
		return "duck"
	case TypeMicEnable:
		return "mic_enable"
	case TypeChallenge:
		return "challenge"
	default:
		return fmt.Sprintf("unknown(%#02x)", uint8(t))
	}
}

// Frame is one protocol message. Flags is type-specific and payload-free
// frames use it to avoid a payload read on the firmware side.
type Frame struct {
	Type    Type
	Flags   uint8
	Payload []byte
}

// Reader decodes frames from a stream. Not safe for concurrent use.
type Reader struct {
	r      io.Reader
	header [HeaderSize]byte
}

func NewReader(r io.Reader) *Reader { return &Reader{r: r} }

// ReadFrame returns the next frame. An unrecognised type is returned as-is
// rather than rejected: the length field makes it skippable, so newer firmware
// can add frames without breaking an older host.
func (r *Reader) ReadFrame() (Frame, error) {
	if _, err := io.ReadFull(r.r, r.header[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return Frame{}, fmt.Errorf("read frame header: %w", err)
		}
		return Frame{}, err
	}
	f := Frame{Type: Type(r.header[0]), Flags: r.header[1]}
	n := binary.BigEndian.Uint16(r.header[2:])
	if n == 0 {
		return f, nil
	}
	f.Payload = make([]byte, n)
	if _, err := io.ReadFull(r.r, f.Payload); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, fmt.Errorf("read %s payload: %w", f.Type, err)
	}
	return f, nil
}

// Writer encodes frames to a stream. Not safe for concurrent use.
type Writer struct {
	w      io.Writer
	header [HeaderSize]byte
}

func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// WriteFrame emits one frame. Header and payload go out in two writes, so the
// caller should wrap a socket in a buffer to keep them in one segment.
func (w *Writer) WriteFrame(f Frame) error {
	if len(f.Payload) >= MaxPayload {
		return fmt.Errorf("write %s: payload %d bytes exceeds %d", f.Type, len(f.Payload), MaxPayload-1)
	}
	w.header[0], w.header[1] = uint8(f.Type), f.Flags
	binary.BigEndian.PutUint16(w.header[2:], uint16(len(f.Payload)))
	if _, err := w.w.Write(w.header[:]); err != nil {
		return fmt.Errorf("write %s header: %w", f.Type, err)
	}
	if len(f.Payload) == 0 {
		return nil
	}
	if _, err := w.w.Write(f.Payload); err != nil {
		return fmt.Errorf("write %s payload: %w", f.Type, err)
	}
	return nil
}

// Hello opens every connection. The device states its format rather than
// trusting the host's assumption, because a YAML change can alter it. It
// also names the satellite it claims to be and brings its own nonce and
// ephemeral X25519 key, so the host can prove itself, over both, before the
// device proves anything (ADR-0074).
type Hello struct {
	Version       uint8
	SampleRate    uint32
	BitsPerSample uint8
	MicChannels   uint8
	// Nonce is the device's, fresh per connection. Join draws one when it is zero.
	Nonce [NonceSize]byte
	// Ephemeral is the device's X25519 public key for this connection only.
	// Join draws the key pair, whatever the field held.
	Ephemeral [PublicKeySize]byte
	// Name is the ESPHome node name, MAC suffix included: the inventory's key.
	Name string
}

// helloFixed is the payload before the name: version, rate, bits, channels,
// the nonce and the ephemeral key.
const (
	helloNonce     = 7
	helloEphemeral = helloNonce + NonceSize
	helloFixed     = helloEphemeral + PublicKeySize
)

func (h Hello) Frame() Frame {
	p := make([]byte, helloFixed, helloFixed+len(h.Name))
	p[0] = h.Version
	binary.BigEndian.PutUint32(p[1:5], h.SampleRate)
	p[5], p[6] = h.BitsPerSample, h.MicChannels
	copy(p[helloNonce:helloEphemeral], h.Nonce[:])
	copy(p[helloEphemeral:helloFixed], h.Ephemeral[:])
	p = append(p, h.Name...)
	return Frame{Type: TypeHello, Payload: p}
}

// ParseHello reads the version before anything else, so a device on another
// protocol is told that, rather than that its hello is malformed.
func ParseHello(p []byte) (Hello, error) {
	if len(p) == 0 {
		return Hello{}, errors.New("hello: empty payload")
	}
	if p[0] != ProtocolVersion {
		return Hello{}, fmt.Errorf("%w: device speaks chorus_bridge protocol %d, this host speaks %d: "+
			"flash firmware from the same checkout", ErrVersion, p[0], ProtocolVersion)
	}
	if len(p) <= helloFixed {
		return Hello{}, fmt.Errorf("hello: payload %d bytes, want %d and a name", len(p), helloFixed)
	}
	name := p[helloFixed:]
	if len(name) > MaxNameLen {
		return Hello{}, fmt.Errorf("hello: name is %d bytes, want at most %d", len(name), MaxNameLen)
	}
	if !utf8.Valid(name) {
		return Hello{}, errors.New("hello: name is not UTF-8")
	}
	h := Hello{
		Version:       p[0],
		SampleRate:    binary.BigEndian.Uint32(p[1:5]),
		BitsPerSample: p[5],
		MicChannels:   p[6],
		Name:          string(name),
	}
	copy(h.Nonce[:], p[helloNonce:helloEphemeral])
	copy(h.Ephemeral[:], p[helloEphemeral:helloFixed])
	return h, nil
}

// Played is the truncation point. Frames is cumulative since the connection
// opened; the firmware accumulates the per-DMA-buffer deltas that ESPHome's
// add_audio_output_callback reports (SPEC §3.2.1).
type Played struct {
	Frames          uint64
	TimestampMicros int64 // esp_timer, device boot epoch
	// Stop is the tag of the STOP this report answers, or zero for a routine
	// report. The device emits exactly one report per stop, after the stop
	// has gated its counter, so Frames on that report is the position the
	// device cut at; a routine report can predate the stop (ADR-0033).
	Stop uint8
}

func (p Played) Frame() Frame {
	b := make([]byte, 16)
	binary.BigEndian.PutUint64(b[0:8], p.Frames)
	binary.BigEndian.PutUint64(b[8:16], uint64(p.TimestampMicros))
	return Frame{Type: TypePlayed, Flags: p.Stop, Payload: b}
}

func ParsePlayed(flags uint8, p []byte) (Played, error) {
	if len(p) < 16 {
		return Played{}, fmt.Errorf("played: payload %d bytes, want 16", len(p))
	}
	return Played{
		Frames:          binary.BigEndian.Uint64(p[0:8]),
		TimestampMicros: int64(binary.BigEndian.Uint64(p[8:16])),
		Stop:            flags,
	}, nil
}

// Position is the audio the user has actually heard, bounded by the DAC FIFO
// and amp delay rather than by a send-buffer estimate.
func (p Played) Position(sampleRate uint32) time.Duration {
	if sampleRate == 0 {
		return 0
	}
	return time.Duration(p.Frames) * time.Second / time.Duration(sampleRate)
}

// Duck attenuates the mixer's other sources. apply_ducking lives on
// SourceSpeaker, not on the Speaker base class (SPEC §3.2.1).
type Duck struct {
	Decibels       uint8
	DurationMillis uint32
}

func (d Duck) Frame() Frame {
	p := make([]byte, 5)
	p[0] = d.Decibels
	binary.BigEndian.PutUint32(p[1:5], d.DurationMillis)
	return Frame{Type: TypeDuck, Payload: p}
}

func ParseDuck(p []byte) (Duck, error) {
	if len(p) < 5 {
		return Duck{}, fmt.Errorf("duck: payload %d bytes, want 5", len(p))
	}
	return Duck{Decibels: p[0], DurationMillis: binary.BigEndian.Uint32(p[1:5])}, nil
}

// Mute is reported, never requested. Hardware mute is a user-facing privacy
// control and the host has no frame to clear it.
type Mute struct {
	Hardware bool
	Software bool
}

func (m Mute) Frame() Frame {
	var flags uint8
	if m.Hardware {
		flags |= muteHardware
	}
	if m.Software {
		flags |= muteSoftware
	}
	return Frame{Type: TypeMute, Flags: flags}
}

func ParseMute(flags uint8) Mute {
	return Mute{
		Hardware: flags&muteHardware != 0,
		Software: flags&muteSoftware != 0,
	}
}
