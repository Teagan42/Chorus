package bridge

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"unicode/utf8"
)

// The handshake's sizes. The firmware mirrors them in chorus_auth.h.
const (
	PSKSize   = 32          // the satellite's api.encryption.key, decoded
	NonceSize = 32          // the challenge, from crypto/rand
	MACSize   = sha256.Size // HMAC-SHA256
	// MaxNameLen bounds the name an auth frame may carry. ESPHome node names
	// are 31 bytes at most, MAC suffix included.
	MaxNameLen = 64
)

// authContext opens every MAC input, so an answer is only ever valid as a
// version 3 auth frame.
const authContext = "chorus-bridge v3"

// linkKeyInfo separates the link key from the API's Noise PSK: the same
// secret, so neither protocol's output can stand in for the other's.
const linkKeyInfo = "chorus-bridge auth v3"

// Why a handshake was refused. chorusd logs which one, so a refused device
// says whether to fix its key, its name, or its firmware.
var (
	ErrVersion         = errors.New("bridge: protocol version mismatch")
	ErrUnknownDevice   = errors.New("bridge: device name not in the inventory")
	ErrBadMAC          = errors.New("bridge: auth MAC does not verify")
	ErrUnauthenticated = errors.New("bridge: frame before auth")
)

// Keys returns the PSK of the satellite a device names itself as, or false
// when the inventory has no satellite by that name.
type Keys func(name string) (psk []byte, ok bool)

// LinkKey derives the audio link's MAC key from a satellite's API PSK, by
// HKDF-SHA256 with no salt.
func LinkKey(psk []byte) ([]byte, error) {
	if len(psk) != PSKSize {
		return nil, fmt.Errorf("link key: psk is %d bytes, want %d", len(psk), PSKSize)
	}
	return hkdf.Key(sha256.New, psk, nil, linkKeyInfo, MACSize)
}

// Challenge is the host's nonce, sent once the hello has been checked.
type Challenge struct {
	Nonce [NonceSize]byte
}

// NewChallenge draws a fresh nonce. crypto/rand never fails short of
// crashing the process, so there is no error to return.
func NewChallenge() Challenge {
	var c Challenge
	_, _ = rand.Read(c.Nonce[:])
	return c
}

func (c Challenge) Frame() Frame {
	return Frame{Type: TypeChallenge, Payload: append([]byte(nil), c.Nonce[:]...)}
}

func ParseChallenge(p []byte) (Challenge, error) {
	var c Challenge
	if len(p) != NonceSize {
		return c, fmt.Errorf("challenge: payload %d bytes, want %d", len(p), NonceSize)
	}
	copy(c.Nonce[:], p)
	return c, nil
}

// Auth is the device's answer: the name it claims and a MAC proving it holds
// that satellite's key. On the wire the MAC comes first and the name is the
// rest of the payload.
type Auth struct {
	Name string
	MAC  [MACSize]byte
}

func (a Auth) Frame() Frame {
	p := make([]byte, 0, MACSize+len(a.Name))
	p = append(p, a.MAC[:]...)
	p = append(p, a.Name...)
	return Frame{Type: TypeAuth, Payload: p}
}

func ParseAuth(p []byte) (Auth, error) {
	var a Auth
	if len(p) <= MACSize {
		return a, fmt.Errorf("auth: payload %d bytes, want a %d-byte MAC and a name", len(p), MACSize)
	}
	name := p[MACSize:]
	if len(name) > MaxNameLen {
		return a, fmt.Errorf("auth: name is %d bytes, want at most %d", len(name), MaxNameLen)
	}
	if !utf8.Valid(name) {
		return a, fmt.Errorf("auth: name is not UTF-8")
	}
	copy(a.MAC[:], p[:MACSize])
	a.Name = string(name)
	return a, nil
}

// AnswerChallenge is the device's half: what chorus_bridge.cpp computes when
// a challenge arrives, and what bridgetest's device sends.
func AnswerChallenge(key []byte, c Challenge, name string, h Hello) Auth {
	a := Auth{Name: name}
	copy(a.MAC[:], authMAC(key, c, name, h))
	return a
}

// Verify reports whether a was made with key over this challenge and hello,
// in constant time.
func (a Auth) Verify(key []byte, c Challenge, h Hello) bool {
	return hmac.Equal(a.MAC[:], authMAC(key, c, a.Name, h))
}

// authMAC covers the hello as declared, so an on-path peer cannot alter the
// format either. The name is the only variable-length field, so the
// concatenation is unambiguous without a length prefix.
func authMAC(key []byte, c Challenge, name string, h Hello) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(authContext))
	m.Write(c.Nonce[:])
	m.Write([]byte(name))
	m.Write(h.Frame().Payload)
	return m.Sum(nil)
}
