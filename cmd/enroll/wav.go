package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/teagan42/chorus/internal/bridge"
)

// readTake returns the data chunk of a canonical PCM WAV in the satellite's
// own format, which is the only format the embed contract takes
// (sidecars/speakerid/README.md). Anything else is refused, naming what it
// is, rather than resampled: the thresholds in internal/identity were
// measured on the audio the device produces.
func readTake(path string) ([]byte, error) {
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
				return nil, fmt.Errorf("is %s, %d ch, %d Hz, %d bit; want PCM mono %d Hz %d bit, and nothing is resampled here",
					formatName(format), channels, rate, bits, bridge.SampleRate, bridge.BitsPerSample)
			}
			fmtSeen = true
		case "data":
			if !fmtSeen {
				return nil, errors.New("data chunk before fmt chunk")
			}
			if len(body) == 0 {
				return nil, errors.New("data chunk is empty")
			}
			return body, nil
		}
		// Chunks are word-aligned; an odd size carries a pad byte.
		off += 8 + size + size%2
	}
	return nil, io.ErrUnexpectedEOF
}

func formatName(format uint16) string {
	switch format {
	case 1:
		return "PCM"
	case 3:
		return "float"
	default:
		return fmt.Sprintf("format %d", format)
	}
}
