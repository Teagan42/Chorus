package bridge

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// DeviceConn is a satellite's end of a link once Join has finished: frames
// it writes are sealed under the device-to-host key and frames it reads are
// opened under the other. One reader and one writer may run at once.
type DeviceConn struct {
	hello Hello
	r     *SealedReader
	w     *SealedWriter
}

// Hello is what the device sent, nonce included.
func (d *DeviceConn) Hello() Hello { return d.hello }

func (d *DeviceConn) ReadFrame() (Frame, error) { return d.r.ReadFrame() }

func (d *DeviceConn) WriteFrame(f Frame) error { return d.w.WriteFrame(f) }

// Join is the device's half of the handshake, step for step what
// chorus_bridge.cpp does: send the hello, check the host's proof, and only
// then answer with its own. h.Name is the satellite it claims to be; a zero
// h.Nonce is drawn fresh. bridgetest's satellite joins through it.
func Join(rw io.ReadWriter, h Hello, psk []byte) (*DeviceConn, error) {
	if h.Name == "" {
		return nil, errors.New("join: the hello names no satellite")
	}
	if h.Nonce == ([NonceSize]byte{}) {
		_, _ = rand.Read(h.Nonce[:])
	}
	key, err := LinkKey(psk)
	if err != nil {
		return nil, fmt.Errorf("join: %w", err)
	}
	if err := writeWhole(rw, h.Frame()); err != nil {
		return nil, fmt.Errorf("join: send hello: %w", err)
	}
	f, err := NewReader(rw).ReadFrame()
	if err != nil {
		return nil, fmt.Errorf("join: read challenge: %w", err)
	}
	if f.Type != TypeChallenge {
		return nil, fmt.Errorf("%w: host sent %s, want challenge", ErrUnauthenticated, f.Type)
	}
	c, err := ParseChallenge(f.Payload)
	if err != nil {
		return nil, fmt.Errorf("join: %w", err)
	}
	// Before the device proves anything: a host without the key learns
	// nothing from this connection but the hello.
	if !c.Verify(key, h) {
		return nil, fmt.Errorf("%w: as %q", ErrHostUnproven, h.Name)
	}
	if err := writeWhole(rw, AnswerChallenge(key, h, c).Frame()); err != nil {
		return nil, fmt.Errorf("join: send auth: %w", err)
	}
	deviceToHost, hostToDevice, err := SessionKeys(key, h, c.Nonce)
	if err != nil {
		return nil, err
	}
	d := &DeviceConn{hello: h}
	if d.r, err = NewSealedReader(rw, hostToDevice); err != nil {
		return nil, err
	}
	if d.w, err = NewSealedWriter(rw, deviceToHost); err != nil {
		return nil, err
	}
	return d, nil
}
