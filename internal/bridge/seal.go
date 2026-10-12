package bridge

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// After the handshake every frame is a sealed record (ADR-0074):
//
//	length:u16  AES-256-GCM(key, nonce, type:u8 flags:u8 payload, aad = length)  tag[16]
//
// The nonce is four zero bytes and a big-endian u64 counter, one counter per
// direction from zero. It never travels: each side knows the next one, so a
// replayed, reordered, dropped or altered record fails to open.
const (
	// TagSize is GCM's tag, the whole of it.
	TagSize = 16
	// RecordHeaderSize is the length field; the type and flags are sealed.
	RecordHeaderSize = 2
	// SealedOverhead is what a sealed frame costs on the wire beyond its
	// payload: the length, the sealed type and flags, and the tag.
	SealedOverhead = RecordHeaderSize + 2 + TagSize
	// MaxSealedPayload is the largest payload whose record fits the length field.
	MaxSealedPayload = MaxPayload - 1 - 2 - TagSize
	gcmNonceSize     = 12
)

// ErrBadRecord is a sealed frame that does not open under the next counter:
// tampered, replayed, reordered, or sealed under another key. The link is
// dropped; nothing in it is delivered.
var ErrBadRecord = errors.New("bridge: sealed frame does not authenticate")

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("session key is %d bytes, want %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func counterNonce(seq uint64) []byte {
	var n [gcmNonceSize]byte
	binary.BigEndian.PutUint64(n[4:], seq)
	return n[:]
}

// SealedWriter seals frames onto one direction of a link. Not safe for
// concurrent use.
type SealedWriter struct {
	w    io.Writer
	aead cipher.AEAD
	seq  uint64
	buf  []byte
}

func NewSealedWriter(w io.Writer, key []byte) (*SealedWriter, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return &SealedWriter{w: w, aead: aead}, nil
}

// WriteFrame seals f under the next counter and writes the record in one
// Write, so a socket sends it in one segment when it fits.
func (s *SealedWriter) WriteFrame(f Frame) error {
	if len(f.Payload) > MaxSealedPayload {
		return fmt.Errorf("write %s: payload %d bytes exceeds %d", f.Type, len(f.Payload), MaxSealedPayload)
	}
	if s.seq == math.MaxUint64 {
		return fmt.Errorf("write %s: the link's counter is spent", f.Type)
	}
	n := 2 + len(f.Payload)
	s.buf = append(s.buf[:0], 0, 0, byte(f.Type), f.Flags)
	s.buf = append(s.buf, f.Payload...)
	binary.BigEndian.PutUint16(s.buf, uint16(n+TagSize))
	s.buf = s.aead.Seal(s.buf[:RecordHeaderSize], counterNonce(s.seq), s.buf[RecordHeaderSize:RecordHeaderSize+n],
		s.buf[:RecordHeaderSize])
	// Spent even if the write fails: a counter is never sealed under twice.
	s.seq++
	if _, err := s.w.Write(s.buf); err != nil {
		return fmt.Errorf("write %s: %w", f.Type, err)
	}
	return nil
}

// SealedReader opens frames from one direction of a link. Not safe for
// concurrent use.
type SealedReader struct {
	r      io.Reader
	aead   cipher.AEAD
	seq    uint64
	header [RecordHeaderSize]byte
}

func NewSealedReader(r io.Reader, key []byte) (*SealedReader, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return &SealedReader{r: r, aead: aead}, nil
}

// ReadFrame returns the next frame, or io.EOF when the stream ends cleanly
// between records. A record that does not open is ErrBadRecord, and the
// reader is spent: its counter no longer matches the peer's.
func (s *SealedReader) ReadFrame() (Frame, error) {
	if _, err := io.ReadFull(s.r, s.header[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return Frame{}, fmt.Errorf("read record header: %w", err)
		}
		return Frame{}, err
	}
	n := int(binary.BigEndian.Uint16(s.header[:]))
	if n < 2+TagSize {
		return Frame{}, fmt.Errorf("%w: record %d of %d bytes is shorter than a sealed frame", ErrBadRecord, s.seq, n)
	}
	if s.seq == math.MaxUint64 {
		return Frame{}, errors.New("read record: the link's counter is spent")
	}
	// Fresh per record: a handler may keep the payload it is handed.
	buf := make([]byte, n)
	if _, err := io.ReadFull(s.r, buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, fmt.Errorf("read record %d: %w", s.seq, err)
	}
	plain, err := s.aead.Open(buf[:0], counterNonce(s.seq), buf, s.header[:])
	if err != nil {
		return Frame{}, fmt.Errorf("%w: record %d", ErrBadRecord, s.seq)
	}
	s.seq++
	f := Frame{Type: Type(plain[0]), Flags: plain[1]}
	if len(plain) > 2 {
		f.Payload = plain[2:]
	}
	return f, nil
}
