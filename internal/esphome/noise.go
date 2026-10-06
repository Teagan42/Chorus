// Package esphome implements enough of the ESPHome native API client to drive a
// voice satellite without Home Assistant.
//
// Transport shape (see docs/SPEC.md §3.1): the DEVICE is the TCP server and we
// dial out to it. Frames are Noise_NNpsk0_25519_ChaChaPoly_SHA256 over TCP 6053.
package esphome

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/flynn/noise"
)

const (
	// Frame indicator byte. 0x00 is the plaintext API, 0x01 is Noise.
	indicatorNoise = 0x01

	// Only one cipher suite is defined; the device echoes it in ServerHello.
	chosenProtoNoise = 0x01

	// Handshake bodies carry a leading status byte: 0x00 ok, otherwise the rest
	// of the body is a human-readable failure reason.
	handshakeOK = 0x00

	// Inner plaintext header is type_hi, type_lo, len_hi, len_lo.
	innerHeaderLen = 4

	maxFrame = 1 << 16
)

// noisePrologue is the literal "NoiseAPIInit" followed by the big-endian length
// of the client-hello body. The body is currently always empty; ESPHome
// reserves it for future flags, so the two zero bytes are not padding.
var noisePrologue = []byte("NoiseAPIInit\x00\x00")

// frameConn reads and writes ESPHome Noise frames over a byte stream.
type frameConn struct {
	rw  io.ReadWriter
	buf [maxFrame]byte
}

// writeFrame emits [indicator][len BE16][body].
func (c *frameConn) writeFrame(body []byte) error {
	if len(body) > 0xFFFF {
		return fmt.Errorf("frame body too large: %d", len(body))
	}
	hdr := [3]byte{indicatorNoise, byte(len(body) >> 8), byte(len(body))}
	if _, err := c.rw.Write(hdr[:]); err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	_, err := c.rw.Write(body)
	return err
}

// readFrame returns a slice valid only until the next readFrame call.
func (c *frameConn) readFrame() ([]byte, error) {
	var hdr [3]byte
	if _, err := io.ReadFull(c.rw, hdr[:]); err != nil {
		return nil, err
	}
	if hdr[0] != indicatorNoise {
		// 0x00 here means the device is running a plaintext API: no encryption
		// key configured, or the wrong one is set in our inventory.
		return nil, fmt.Errorf("unexpected frame indicator 0x%02x (device may have encryption disabled)", hdr[0])
	}
	n := int(binary.BigEndian.Uint16(hdr[1:]))
	if n == 0 {
		return nil, nil
	}
	if _, err := io.ReadFull(c.rw, c.buf[:n]); err != nil {
		return nil, err
	}
	return c.buf[:n], nil
}

// handshake performs the NNpsk0 exchange and returns the two cipher states.
func handshake(c *frameConn, pskBase64 string) (send, recv *noise.CipherState, nodeName string, err error) {
	psk, err := base64.StdEncoding.DecodeString(pskBase64)
	if err != nil {
		return nil, nil, "", fmt.Errorf("decode psk: %w", err)
	}
	if len(psk) != 32 {
		return nil, nil, "", fmt.Errorf("psk must be 32 bytes, got %d", len(psk))
	}

	// Empty client hello opens the exchange.
	if err := c.writeFrame(nil); err != nil {
		return nil, nil, "", fmt.Errorf("client hello: %w", err)
	}

	serverHello, err := c.readFrame()
	if err != nil {
		return nil, nil, "", fmt.Errorf("server hello: %w", err)
	}
	nodeName, err = parseServerHello(serverHello)
	if err != nil {
		return nil, nil, "", err
	}

	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:           noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256),
		Pattern:               noise.HandshakeNN,
		Initiator:             true,
		Prologue:              noisePrologue,
		PresharedKey:          psk,
		PresharedKeyPlacement: 0,
	})
	if err != nil {
		return nil, nil, "", fmt.Errorf("init noise: %w", err)
	}

	msg, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, nil, "", fmt.Errorf("handshake write: %w", err)
	}
	if err := c.writeFrame(append([]byte{handshakeOK}, msg...)); err != nil {
		return nil, nil, "", fmt.Errorf("send handshake: %w", err)
	}

	resp, err := c.readFrame()
	if err != nil {
		return nil, nil, "", fmt.Errorf("read handshake: %w", err)
	}
	if len(resp) < 1 {
		return nil, nil, "", errors.New("empty handshake response")
	}
	if resp[0] != handshakeOK {
		// Almost always a PSK mismatch; the device explains itself here.
		return nil, nil, "", fmt.Errorf("device rejected handshake: %s", string(resp[1:]))
	}

	_, cs1, cs2, err := hs.ReadMessage(nil, resp[1:])
	if err != nil {
		return nil, nil, "", fmt.Errorf("handshake read: %w", err)
	}
	if cs1 == nil || cs2 == nil {
		return nil, nil, "", errors.New("handshake completed without cipher states")
	}
	// Initiator: cs1 encrypts to the device, cs2 decrypts from it.
	return cs1, cs2, nodeName, nil
}

// parseServerHello reads [chosen_proto][node name\0][mac\0].
func parseServerHello(b []byte) (string, error) {
	if len(b) < 1 {
		return "", errors.New("short server hello")
	}
	if b[0] != chosenProtoNoise {
		return "", fmt.Errorf("device chose unsupported noise proto 0x%02x", b[0])
	}
	fields := splitNul(b[1:])
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

func splitNul(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == 0 {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	return out
}

// encodeInner prepends the 4-byte type/length header to a serialized message.
func encodeInner(msgType uint32, payload []byte) []byte {
	out := make([]byte, innerHeaderLen+len(payload))
	out[0] = byte(msgType >> 8)
	out[1] = byte(msgType)
	out[2] = byte(len(payload) >> 8)
	out[3] = byte(len(payload))
	copy(out[innerHeaderLen:], payload)
	return out
}

// decodeInner splits a decrypted frame into message type and payload.
func decodeInner(b []byte) (msgType uint32, payload []byte, err error) {
	if len(b) < innerHeaderLen {
		return 0, nil, fmt.Errorf("short inner frame: %d bytes", len(b))
	}
	msgType = uint32(b[0])<<8 | uint32(b[1])
	n := int(b[2])<<8 | int(b[3])
	if innerHeaderLen+n > len(b) {
		return 0, nil, fmt.Errorf("inner length %d exceeds frame %d", n, len(b)-innerHeaderLen)
	}
	return msgType, b[innerHeaderLen : innerHeaderLen+n], nil
}
