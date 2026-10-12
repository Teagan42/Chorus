package bridge

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// The handshake's sizes. The firmware mirrors them in chorus_auth.h.
const (
	PSKSize   = 32          // the satellite's api.encryption.key, decoded
	NonceSize = 32          // each side's, from a CSPRNG, fresh per connection
	MACSize   = sha256.Size // HMAC-SHA256, both proofs
	KeySize   = 32          // AES-256, one key per direction
	// PublicKeySize is an X25519 public key, each side's fresh per connection.
	PublicKeySize = 32
	// MaxNameLen bounds the name a hello may carry. ESPHome node names are
	// 31 bytes at most, MAC suffix included.
	MaxNameLen = 64
)

// Labels. Each proof opens with its own role, so neither side's proof can
// be reflected as the other's, and each direction's key has its own info.
// The firmware spells the same bytes in chorus_auth.h.
const (
	linkKeyInfo        = "chorus-bridge link v4"
	hostProofContext   = "chorus-bridge v4 host proof"
	deviceProofContext = "chorus-bridge v4 device proof"
	deviceToHostInfo   = "chorus-bridge v4 device to host"
	hostToDeviceInfo   = "chorus-bridge v4 host to device"
)

// Why a handshake was refused. chorusd logs which one, so a refused device
// says whether to fix its key, its name, or its firmware.
var (
	ErrVersion         = errors.New("bridge: protocol version mismatch")
	ErrUnknownDevice   = errors.New("bridge: device name not in the inventory")
	ErrBadMAC          = errors.New("bridge: the device's proof does not verify")
	ErrUnauthenticated = errors.New("bridge: frame before auth")
	// ErrChallengeRefused is a device that hung up on the host's proof: a
	// satellite whose key is not the inventory's does exactly that.
	ErrChallengeRefused = errors.New("bridge: the device hung up on the host's proof")
	// ErrHostUnproven is the device's refusal: the host does not hold its key.
	ErrHostUnproven = errors.New("bridge: the host's proof does not verify")
	// ErrWeakKey is an ephemeral public key of low order: its shared secret
	// is all zeros whatever the other side's key, so it would seal nothing.
	ErrWeakKey = errors.New("bridge: the peer's ephemeral key is a low-order point")
)

// Keys returns the PSK of the satellite a device names itself as, or false
// when the inventory has no satellite by that name.
type Keys func(name string) (psk []byte, ok bool)

// LinkKey derives the audio link's key from a satellite's API PSK, by
// HKDF-SHA256 with no salt. Both proofs are made under it, and it is mixed
// into the session keys beside the X25519 secret.
func LinkKey(psk []byte) ([]byte, error) {
	if len(psk) != PSKSize {
		return nil, fmt.Errorf("link key: psk is %d bytes, want %d", len(psk), PSKSize)
	}
	return hkdf.Key(sha256.New, psk, nil, linkKeyInfo, MACSize)
}

// NewEphemeral draws a connection's X25519 key pair. It is never stored:
// once the shared secret is taken it goes out of scope with the handshake.
func NewEphemeral() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

// PublicOf is priv's public key as the wire carries it.
func PublicOf(priv *ecdh.PrivateKey) [PublicKeySize]byte {
	var out [PublicKeySize]byte
	copy(out[:], priv.PublicKey().Bytes())
	return out
}

// SharedSecret is X25519(priv, peer). A low-order peer key, whose secret
// is all zeros, is ErrWeakKey.
func SharedSecret(priv *ecdh.PrivateKey, peer [PublicKeySize]byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(peer[:])
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWeakKey, err)
	}
	shared, err := priv.ECDH(pub)
	if err != nil {
		return nil, fmt.Errorf("%w: %x", ErrWeakKey, peer)
	}
	return shared, nil
}

// Challenge is the host's answer to the hello: its nonce, its ephemeral
// public key, and its proof that it holds the key of the satellite the
// hello names.
type Challenge struct {
	Nonce     [NonceSize]byte
	Ephemeral [PublicKeySize]byte
	Proof     [MACSize]byte
}

// NewChallenge draws a fresh host nonce and ephemeral key pair and proves
// the host under key. The private key is the caller's, for the shared secret.
func NewChallenge(key []byte, h Hello) (Challenge, *ecdh.PrivateKey, error) {
	var c Challenge
	priv, err := NewEphemeral()
	if err != nil {
		return c, nil, fmt.Errorf("challenge: %w", err)
	}
	_, _ = rand.Read(c.Nonce[:])
	c.Ephemeral = PublicOf(priv)
	c.Proof = HostProof(key, h, c)
	return c, priv, nil
}

// challengeSize is the payload: nonce, ephemeral, proof.
const challengeSize = NonceSize + PublicKeySize + MACSize

func (c Challenge) Frame() Frame {
	p := make([]byte, 0, challengeSize)
	p = append(p, c.Nonce[:]...)
	p = append(p, c.Ephemeral[:]...)
	p = append(p, c.Proof[:]...)
	return Frame{Type: TypeChallenge, Payload: p}
}

func ParseChallenge(p []byte) (Challenge, error) {
	var c Challenge
	if len(p) != challengeSize {
		return c, fmt.Errorf("challenge: payload %d bytes, want %d", len(p), challengeSize)
	}
	copy(c.Nonce[:], p[:NonceSize])
	copy(c.Ephemeral[:], p[NonceSize:NonceSize+PublicKeySize])
	copy(c.Proof[:], p[NonceSize+PublicKeySize:])
	return c, nil
}

// Verify is the device's check that the host holds key, over the hello the
// device sent and the host's own nonce and ephemeral key, in constant time.
func (c Challenge) Verify(key []byte, h Hello) bool {
	want := HostProof(key, h, c)
	return hmac.Equal(c.Proof[:], want[:])
}

// Auth is the device's proof, sent only once the host has proved itself.
type Auth struct {
	Proof [MACSize]byte
}

func (a Auth) Frame() Frame {
	return Frame{Type: TypeAuth, Payload: append([]byte(nil), a.Proof[:]...)}
}

func ParseAuth(p []byte) (Auth, error) {
	var a Auth
	if len(p) != MACSize {
		return a, fmt.Errorf("auth: payload %d bytes, want %d", len(p), MACSize)
	}
	copy(a.Proof[:], p)
	return a, nil
}

// AnswerChallenge is the device's half: what chorus_bridge.cpp sends once
// the host's proof verifies, and what Join sends.
func AnswerChallenge(key []byte, h Hello, c Challenge) Auth {
	return Auth{Proof: DeviceProof(key, h, c)}
}

// Verify reports whether a was made with key over this hello and challenge,
// in constant time.
func (a Auth) Verify(key []byte, h Hello, c Challenge) bool {
	want := DeviceProof(key, h, c)
	return hmac.Equal(a.Proof[:], want[:])
}

// HostProof is HMAC-SHA256(key, host label || hello payload || host nonce ||
// host ephemeral). The hello carries the format, the device's nonce and
// ephemeral key and the name, so the proof covers both public keys: a peer
// on the path that swaps either one breaks it. Only the name varies in
// length, and what follows it is fixed, so no length prefix is needed.
// c.Proof is not read.
func HostProof(key []byte, h Hello, c Challenge) [MACSize]byte {
	return proof(key, hostProofContext, h, c)
}

// DeviceProof is HostProof's input under the device's label.
func DeviceProof(key []byte, h Hello, c Challenge) [MACSize]byte {
	return proof(key, deviceProofContext, h, c)
}

func proof(key []byte, label string, h Hello, c Challenge) [MACSize]byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(label))
	m.Write(h.Frame().Payload)
	m.Write(c.Nonce[:])
	m.Write(c.Ephemeral[:])
	var out [MACSize]byte
	m.Sum(out[:0])
	return out
}

// SessionKeys derives this connection's two AES-256-GCM keys: HKDF-SHA256
// over the X25519 secret then the link key, salted with both nonces, one
// info per direction. The secret is what a recording cannot recover from a
// leaked PSK; the link key is what an unauthenticated exchange lacks.
func SessionKeys(key, shared []byte, h Hello, c Challenge) (deviceToHost, hostToDevice []byte, err error) {
	if len(shared) != PublicKeySize {
		return nil, nil, fmt.Errorf("session keys: shared secret is %d bytes, want %d", len(shared), PublicKeySize)
	}
	ikm := make([]byte, 0, len(shared)+len(key))
	ikm = append(ikm, shared...)
	ikm = append(ikm, key...)
	defer clear(ikm)
	salt := make([]byte, 0, 2*NonceSize)
	salt = append(salt, h.Nonce[:]...)
	salt = append(salt, c.Nonce[:]...)
	if deviceToHost, err = hkdf.Key(sha256.New, ikm, salt, deviceToHostInfo, KeySize); err != nil {
		return nil, nil, fmt.Errorf("session keys: %w", err)
	}
	if hostToDevice, err = hkdf.Key(sha256.New, ikm, salt, hostToDeviceInfo, KeySize); err != nil {
		return nil, nil, fmt.Errorf("session keys: %w", err)
	}
	return deviceToHost, hostToDevice, nil
}
