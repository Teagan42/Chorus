// Package wyoming speaks and hears over the Wyoming protocol: the one Home
// Assistant's own speech services answer, wyoming-faster-whisper and
// wyoming-piper among them. A household leaving Assist for Chorus already
// runs these; this lets it keep them (ADR-0081).
//
// Wyoming is events over TCP. Each is a JSON header line, then optionally
// that many bytes of JSON data and that many bytes of binary payload:
//
//	{"type":"audio-chunk","data":{"rate":16000,"width":2,"channels":1},"payload_length":2048}\n<2048 bytes>
//
// Writing puts data inline in the header, which every protocol version
// reads; reading accepts it inline, after the header, or both.
package wyoming

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// DefaultTimeout bounds one exchange, as the HTTP providers bound one
// request: a hung server would otherwise park the goroutine waiting on it
// for the whole session.
const DefaultTimeout = 30 * time.Second

// Limits on what a server may make this client allocate. A header line is
// a few hundred bytes; an audio chunk is a few kilobytes, and the largest
// whole-clause payload any server sends is well under a megabyte.
const (
	maxHeader  = 64 << 10
	maxData    = 1 << 20
	maxPayload = 16 << 20
)

// event is one Wyoming message.
type event struct {
	Type    string
	Data    map[string]any
	Payload []byte
}

type header struct {
	Type          string         `json:"type"`
	Data          map[string]any `json:"data,omitempty"`
	DataLength    int            `json:"data_length,omitempty"`
	PayloadLength int            `json:"payload_length,omitempty"`
}

func writeEvent(w io.Writer, ev event) error {
	line, err := json.Marshal(header{Type: ev.Type, Data: ev.Data, PayloadLength: len(ev.Payload)})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if len(ev.Payload) > 0 {
		line = append(line, ev.Payload...)
	}
	_, err = w.Write(line)
	return err
}

func readEvent(r *bufio.Reader) (event, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return event{}, err
		}
		line = append(line, chunk...)
		if len(line) > maxHeader {
			return event{}, fmt.Errorf("header over %d bytes", maxHeader)
		}
		if !isPrefix {
			break
		}
	}
	var h header
	if err := json.Unmarshal(line, &h); err != nil {
		return event{}, fmt.Errorf("header: %w", err)
	}
	if h.Type == "" {
		return event{}, errors.New("header has no type")
	}
	if h.DataLength < 0 || h.DataLength > maxData || h.PayloadLength < 0 || h.PayloadLength > maxPayload {
		return event{}, fmt.Errorf("%s: data %d and payload %d bytes are out of bounds", h.Type, h.DataLength, h.PayloadLength)
	}
	ev := event{Type: h.Type, Data: h.Data}
	if h.DataLength > 0 {
		raw := make([]byte, h.DataLength)
		if _, err := io.ReadFull(r, raw); err != nil {
			return event{}, fmt.Errorf("%s data: %w", h.Type, err)
		}
		var more map[string]any
		if err := json.Unmarshal(raw, &more); err != nil {
			return event{}, fmt.Errorf("%s data: %w", h.Type, err)
		}
		if ev.Data == nil {
			ev.Data = more
		} else {
			for k, v := range more {
				ev.Data[k] = v
			}
		}
	}
	if h.PayloadLength > 0 {
		ev.Payload = make([]byte, h.PayloadLength)
		if _, err := io.ReadFull(r, ev.Payload); err != nil {
			return event{}, fmt.Errorf("%s payload: %w", h.Type, err)
		}
	}
	return ev, nil
}

// str and num read a data field, zero when absent or of another type.
func (ev event) str(key string) string {
	s, _ := ev.Data[key].(string)
	return s
}

func (ev event) num(key string) int {
	f, _ := ev.Data[key].(float64)
	return int(f)
}

// failed turns a server's error event into an error.
func (ev event) failed() error {
	if code := ev.str("code"); code != "" {
		return fmt.Errorf("server error %s: %s", code, ev.str("text"))
	}
	return fmt.Errorf("server error: %s", ev.str("text"))
}

// Address normalises a server address: host:port, with Home Assistant's
// tcp:// prefix tolerated since its integration settings show it that way.
func Address(addr string) (string, error) {
	addr = strings.TrimPrefix(addr, "tcp://")
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return "", fmt.Errorf("%q is not host:port: %w", addr, err)
	}
	return addr, nil
}

// conn is one exchange: a connection bounded by the context and the
// timeout, closed when the exchange is done. Wyoming servers expect a
// connection per request.
type conn struct {
	net.Conn
	r    *bufio.Reader
	stop func() bool
}

func dial(ctx context.Context, addr string, timeout time.Duration) (*conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		c.Close()
		return nil, err
	}
	// The context's own end, deadline or cancel, unblocks whatever read or
	// write is in flight. Not copied into the socket's deadline: that would
	// race the context and report an i/o timeout where the caller's deadline
	// is the cause.
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Unix(1, 0)) })
	return &conn{Conn: c, r: bufio.NewReader(c), stop: stop}, nil
}

// end closes the exchange. Its close error is nothing to act on: the
// answer, or the failure that ended it, is already in hand.
func (c *conn) end() {
	c.stop()
	_ = c.Close()
}

func (c *conn) send(evs ...event) error {
	w := bufio.NewWriter(c.Conn)
	for _, ev := range evs {
		if err := writeEvent(w, ev); err != nil {
			return err
		}
	}
	return w.Flush()
}

func (c *conn) next() (event, error) { return readEvent(c.r) }

// cause prefers the context's error to the deadline it caused.
func cause(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Probe asks a server to describe itself, for readiness: any server that
// answers describe with info is up and speaking the protocol.
func Probe(addr string) func(context.Context) error {
	return func(ctx context.Context) error {
		a, err := Address(addr)
		if err != nil {
			return err
		}
		c, err := dial(ctx, a, DefaultTimeout)
		if err != nil {
			return err
		}
		defer c.end()
		if err := c.send(event{Type: "describe"}); err != nil {
			return cause(ctx, err)
		}
		for {
			ev, err := c.next()
			if err != nil {
				return cause(ctx, err)
			}
			if ev.Type == "info" {
				return nil
			}
		}
	}
}
