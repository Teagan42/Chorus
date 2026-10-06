package bridge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// ttsChunkBytes is 32 ms of 16 kHz mono, matching the firmware's own send
// cadence (SPEC §3.2). It also keeps every chunk under the uint16 length field
// and on a sample boundary, since a half sample desynchronises the device.
const ttsChunkBytes = 32 * SampleRate / 1000 * (BitsPerSample / 8)

// helloTimeout bounds the opening handshake so a TCP connection from something
// that is not a satellite cannot occupy the accept path forever.
const helloTimeout = 10 * time.Second

// Handler receives decoded device frames. Calls come from Serve's read loop in
// arrival order, so a slow handler stalls the uplink; hand audio off rather
// than process it here. A returned error tears the link down.
type Handler interface {
	OnMic(channel uint8, pcm []byte) error
	OnWake(word string) error
	OnPlayed(Played) error
	OnMute(Mute) error
}

// Link is one satellite's audio connection. Reads and writes are independent:
// Serve owns the read side while any number of goroutines call the send
// methods, which is what makes playback-during-capture possible (SPEC §3.1).
type Link struct {
	conn  net.Conn
	hello Hello
	r     *Reader

	mu sync.Mutex // serializes writes; a torn frame is unparseable
	bw *bufio.Writer
	w  *Writer
}

// NewLink completes the opening handshake. The device states its audio format
// in Hello rather than letting the host assume it, so a YAML change on the
// device surfaces here instead of as misparsed audio.
func NewLink(conn net.Conn) (*Link, error) {
	bw := bufio.NewWriterSize(conn, HeaderSize+ttsChunkBytes)
	l := &Link{conn: conn, r: NewReader(conn), bw: bw, w: NewWriter(bw)}

	f, err := l.r.ReadFrame()
	if err != nil {
		return nil, fmt.Errorf("read hello: %w", err)
	}
	if f.Type != TypeHello {
		return nil, fmt.Errorf("first frame is %s, want hello", f.Type)
	}
	if l.hello, err = ParseHello(f.Payload); err != nil {
		return nil, err
	}
	// Checked, not trusted. Everything downstream -- the chunk size, every
	// Position() -- is hardcoded to 16 kHz/16-bit, so a device declaring
	// anything else would exchange audio and playback positions that both sides
	// silently misread. Refusing the link is what makes a device YAML change
	// surface here, which is the whole reason the format is on the wire.
	if l.hello.SampleRate != SampleRate || l.hello.BitsPerSample != BitsPerSample {
		return nil, fmt.Errorf("device declared %d Hz/%d-bit audio, want %d Hz/%d-bit",
			l.hello.SampleRate, l.hello.BitsPerSample, SampleRate, BitsPerSample)
	}
	return l, nil
}

// Hello is the format the device declared.
func (l *Link) Hello() Hello { return l.hello }

// Serve runs the read side until the device hangs up, the context is cancelled,
// or the handler fails. It returns nil on a clean disconnect so a supervisor
// can reconnect (SPEC §7).
func (l *Link) Serve(ctx context.Context, h Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		<-ctx.Done()
		// A net.Conn read is not cancellable; closing it is the only way to
		// unblock one.
		_ = l.conn.Close()
	}()

	for {
		f, err := l.r.ReadFrame()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := dispatch(h, f); err != nil {
			return err
		}
	}
}

// dispatch decodes one device frame. An unrecognised type is skipped, not
// fatal: the length field makes newer firmware forward-compatible.
func dispatch(h Handler, f Frame) error {
	switch f.Type {
	case TypeMic:
		return h.OnMic(f.Flags, f.Payload)
	case TypeWake:
		return h.OnWake(string(f.Payload))
	case TypePlayed:
		p, err := ParsePlayed(f.Payload)
		if err != nil {
			return err
		}
		return h.OnPlayed(p)
	case TypeMute:
		// Reported, never negotiated: hardware mute is authoritative and this
		// package deliberately offers no frame that could clear it.
		return h.OnMute(ParseMute(f.Flags))
	default:
		return nil
	}
}

// send writes one whole frame. Buffered so the header and payload leave in a
// single segment, then flushed because barge-in latency is the point.
func (l *Link) send(f Frame) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.w.WriteFrame(f); err != nil {
		return err
	}
	return l.bw.Flush()
}

// SendTTS streams downlink PCM as 16 kHz signed 16-bit mono. Chunks from
// concurrent calls may interleave, but each frame is written whole; serialise
// at the call site if one utterance must stay contiguous.
func (l *Link) SendTTS(pcm []byte) error {
	if len(pcm)%(BitsPerSample/8) != 0 {
		return fmt.Errorf("send tts: %d bytes is not a whole number of samples", len(pcm))
	}
	for len(pcm) > 0 {
		n := min(len(pcm), ttsChunkBytes)
		if err := l.send(Frame{Type: TypeTTS, Payload: pcm[:n]}); err != nil {
			return err
		}
		pcm = pcm[n:]
	}
	return nil
}

// Stop is barge-in: the device discards its speaker buffer immediately.
func (l *Link) Stop() error { return l.send(Frame{Type: TypeStop}) }

// Finish ends an utterance naturally, draining what is already buffered.
func (l *Link) Finish() error { return l.send(Frame{Type: TypeFinish}) }

// SendDuck attenuates the mixer's other sources.
func (l *Link) SendDuck(d Duck) error { return l.send(d.Frame()) }

// SetMicEnabled starts or stops the uplink. Software mute only; it cannot
// clear a hardware mute.
func (l *Link) SetMicEnabled(on bool) error {
	var flags uint8
	if on {
		flags = 1
	}
	return l.send(Frame{Type: TypeMicEnable, Flags: flags})
}

// Close drops the connection. The device reconnects on its own interval.
func (l *Link) Close() error { return l.conn.Close() }

// Listener accepts satellites dialing in. Addressing is asymmetric: the device
// dials out, so the orchestrator is the TCP server for audio while remaining
// the client for the native API (SPEC §13).
// One accept loop owns the listener for its whole life, and Accept only
// receives from it. Starting a fresh net.Listener.Accept per call cannot be
// cancelled: an abandoned one stays queued, and the next device to dial in is
// then delivered to a caller that has already given up -- which closes the
// connection while the current caller still waits for it.
type Listener struct {
	ln    net.Listener
	conns chan net.Conn
	errs  chan error

	closeOnce sync.Once
	done      chan struct{}
}

// Listen binds the audio port. The address must be a literal IP the device
// YAML can name, because ESPHome's set_sockaddr does not resolve hostnames.
func Listen(addr string) (*Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	l := &Listener{
		ln:    ln,
		conns: make(chan net.Conn),
		errs:  make(chan error, 1),
		done:  make(chan struct{}),
	}
	go l.acceptLoop()
	return l, nil
}

// acceptLoop hands each connection to whichever Accept is waiting. Unbuffered,
// so a device that dials with no caller waiting stays in the kernel backlog
// where the next Accept finds it, rather than being held open by this process.
func (l *Listener) acceptLoop() {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			select {
			case l.errs <- err:
			case <-l.done:
			}
			return
		}
		select {
		case l.conns <- c:
		case <-l.done:
			_ = c.Close()
			return
		}
	}
}

func (l *Listener) Addr() net.Addr { return l.ln.Addr() }

// Close stops the accept loop and unblocks every waiting Accept.
func (l *Listener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		close(l.done)
		err = l.ln.Close()
	})
	return err
}

// Accept returns the next satellite's link, handshake already complete.
// Cancelling the context abandons only this call; a device that dials in later
// is still delivered to the next one.
func (l *Listener) Accept(ctx context.Context) (*Link, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.done:
		return nil, net.ErrClosed
	case err := <-l.errs:
		// Put back: the listener is dead for good, so every later Accept has to
		// see this too rather than block forever.
		l.errs <- err
		return nil, fmt.Errorf("accept: %w", err)
	case c := <-l.conns:
		return newAcceptedLink(ctx, c)
	}
}

// newAcceptedLink tunes the socket and bounds the handshake read.
func newAcceptedLink(ctx context.Context, conn net.Conn) (*Link, error) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		// Nagle would coalesce the 32 ms chunks barge-in timing depends on.
		// The firmware sets TCP_NODELAY on its end for the same reason.
		_ = tcp.SetNoDelay(true)
	}

	deadline := time.Now().Add(helloTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)

	l, err := NewLink(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	// Cleared: the read side is long-lived and must not inherit a deadline.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return l, nil
}
