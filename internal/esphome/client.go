package esphome

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"
	"google.golang.org/protobuf/proto"

	"github.com/teagan42/chorus/internal/msgid"
	"github.com/teagan42/chorus/internal/pb"
)

// Client is a connection to one satellite. Send is safe for concurrent use;
// Recv must be driven by a single goroutine (Noise cipher states carry nonces
// and tolerate no reordering).
type Client struct {
	conn     net.Conn
	frames   *frameConn
	nodeName string

	sendMu sync.Mutex
	send   *noise.CipherState
	recv   *noise.CipherState

	APIVersion    [2]uint32
	DeviceInfoMsg *pb.DeviceInfoResponse
}

// Dial connects, completes the Noise handshake, and performs the
// Hello/Connect/DeviceInfo opening sequence.
func Dial(ctx context.Context, address, psk string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", address, err)
	}
	// Audio is latency-critical; Nagle would coalesce 32 ms chunks.
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	c := &Client{conn: conn, frames: &frameConn{rw: conn}}
	sendCS, recvCS, nodeName, err := handshake(c.frames, psk)
	if err != nil {
		conn.Close()
		return nil, err
	}
	c.send, c.recv, c.nodeName = sendCS, recvCS, nodeName

	if err := c.open(); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return c, nil
}

// open runs Hello then DeviceInfo. There is no ConnectRequest: password auth
// was removed in ESPHome 2026.1.0 and the Noise handshake is the authentication.
func (c *Client) open() error {
	if err := c.Send(&pb.HelloRequest{
		ClientInfo:      "chorus",
		ApiVersionMajor: 1,
		ApiVersionMinor: 10,
	}); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	hello, err := expect[*pb.HelloResponse](c)
	if err != nil {
		return fmt.Errorf("hello response: %w", err)
	}
	c.APIVersion = [2]uint32{hello.GetApiVersionMajor(), hello.GetApiVersionMinor()}
	if n := hello.GetName(); n != "" {
		c.nodeName = n
	}

	if err := c.Send(&pb.DeviceInfoRequest{}); err != nil {
		return fmt.Errorf("device info: %w", err)
	}
	info, err := expect[*pb.DeviceInfoResponse](c)
	if err != nil {
		return fmt.Errorf("device info response: %w", err)
	}
	c.DeviceInfoMsg = info
	return nil
}

// Name reports the device's node name.
func (c *Client) Name() string { return c.nodeName }

// Send encrypts and writes one API message.
func (c *Client) Send(m proto.Message) error {
	id, err := msgid.For(m)
	if err != nil {
		return err
	}
	payload, err := proto.Marshal(m)
	if err != nil {
		return err
	}

	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	ct, err := c.send.Encrypt(nil, nil, encodeInner(id, payload))
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	return c.frames.writeFrame(ct)
}

// Recv reads and decrypts the next API message. Frames whose id is unknown to
// our generated descriptors are returned as errUnknownMessage rather than
// desyncing the stream, since the cipher state has already advanced.
func (c *Client) Recv() (proto.Message, error) {
	frame, err := c.frames.readFrame()
	if err != nil {
		return nil, err
	}
	pt, err := c.recv.Decrypt(nil, nil, frame)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	id, payload, err := decodeInner(pt)
	if err != nil {
		return nil, err
	}
	m, err := msgid.New(id)
	if err != nil {
		return nil, unknownMessage{id: id, err: err}
	}
	if err := proto.Unmarshal(payload, m); err != nil {
		return nil, fmt.Errorf("unmarshal id %d: %w", id, err)
	}
	return m, nil
}

type unknownMessage struct {
	id  uint32
	err error
}

func (u unknownMessage) Error() string { return u.err.Error() }

func (u unknownMessage) Unwrap() error { return u.err }

// IsUnknownMessage reports whether an error is a decodable-frame-but-unknown-id,
// which is recoverable: the stream is still in sync.
func IsUnknownMessage(err error) bool {
	var u unknownMessage
	return errors.As(err, &u)
}

// Close shuts the connection down.
func (c *Client) Close() error { return c.conn.Close() }

// expect reads until a message of type T arrives, tolerating the state spam the
// device emits unprompted.
func expect[T proto.Message](c *Client) (T, error) {
	var zero T
	for range 64 {
		m, err := c.Recv()
		if err != nil {
			if IsUnknownMessage(err) {
				continue
			}
			return zero, err
		}
		if typed, ok := m.(T); ok {
			return typed, nil
		}
		if d, ok := m.(*pb.DisconnectRequest); ok {
			_ = d
			return zero, fmt.Errorf("device requested disconnect")
		}
	}
	return zero, fmt.Errorf("expected message never arrived")
}
