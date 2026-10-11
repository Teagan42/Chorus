package bridge_test

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/handshake-v3.hex from the inputs in it")

const goldenPath = "testdata/handshake-v3.hex"

// golden is the handshake on fixed inputs. The firmware's host test reads the
// same file, so the two ends are held to the same bytes.
type golden struct {
	psk, nonce                          []byte
	name                                string
	hello, challenge, key, mac, authHex []byte
}

func readGolden(t *testing.T) golden {
	t.Helper()
	f, err := os.Open(goldenPath)
	if err != nil {
		t.Fatalf("open golden frames: %v", err)
	}
	defer func() { _ = f.Close() }()
	var g golden
	fields := map[string]*[]byte{
		"psk": &g.psk, "nonce": &g.nonce, "hello": &g.hello, "challenge": &g.challenge,
		"link_key": &g.key, "mac": &g.mac, "auth": &g.authHex,
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, " ")
		v = strings.TrimSpace(v)
		if !ok {
			t.Fatalf("golden line %q has no value", line)
		}
		if k == "name" {
			g.name = v
			continue
		}
		dst, known := fields[k]
		if !known {
			t.Fatalf("golden key %q is not one this test checks", k)
		}
		if *dst, err = hex.DecodeString(v); err != nil {
			t.Fatalf("golden %s: %v", k, err)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read golden frames: %v", err)
	}
	return g
}

// derive computes every derived line from the golden inputs.
func (g golden) derive(t *testing.T) golden {
	t.Helper()
	key, err := bridge.LinkKey(g.psk)
	if err != nil {
		t.Fatal(err)
	}
	c, err := bridge.ParseChallenge(g.nonce)
	if err != nil {
		t.Fatal(err)
	}
	a := bridge.AnswerChallenge(key, c, g.name, goldenHello)
	out := g
	out.hello = wire(t, goldenHello.Frame())
	out.challenge = wire(t, c.Frame())
	out.key = key
	out.mac = a.MAC[:]
	out.authHex = wire(t, a.Frame())
	return out
}

// goldenHello is the living-room Satellite1's: both mic channels.
var goldenHello = bridge.Hello{
	Version: bridge.ProtocolVersion, SampleRate: bridge.SampleRate,
	BitsPerSample: bridge.BitsPerSample, MicChannels: 2,
}

func wire(t *testing.T, f bridge.Frame) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := bridge.NewWriter(&b).WriteFrame(f); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func writeGolden(t *testing.T, g golden) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`# The chorus_bridge v3 handshake on fixed inputs (ADR-0066). The first
# three lines are inputs; every line after them is derived. Frames are whole,
# header included. Checked by internal/bridge/auth_test.go and, on the
# firmware's side, by esphome/test/chorus_auth_test.cpp.
# Regenerate with: go test ./internal/bridge -run Golden -update
`)
	fmt.Fprintf(&b, "psk %x\n", g.psk)
	fmt.Fprintf(&b, "nonce %x\n", g.nonce)
	fmt.Fprintf(&b, "name %s\n", g.name)
	fmt.Fprintf(&b, "hello %x\n", g.hello)
	fmt.Fprintf(&b, "challenge %x\n", g.challenge)
	fmt.Fprintf(&b, "link_key %x\n", g.key)
	fmt.Fprintf(&b, "mac %x\n", g.mac)
	fmt.Fprintf(&b, "auth %x\n", g.authHex)
	if err := os.WriteFile(goldenPath, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The bytes of the handshake are fixed by its inputs, so a change to the key
// derivation, the MAC's input or either frame's layout is a diff here, and
// the firmware's host test fails on the same file.
//
// verifies SPEC §3.2.2
func TestHandshakeGoldenFrames(t *testing.T) {
	want := readGolden(t)
	got := want.derive(t)
	if *updateGolden {
		writeGolden(t, got)
		return
	}
	for _, c := range []struct {
		name      string
		got, want []byte
	}{
		{"hello", got.hello, want.hello},
		{"challenge", got.challenge, want.challenge},
		{"link_key", got.key, want.key},
		{"mac", got.mac, want.mac},
		{"auth", got.authHex, want.authHex},
	} {
		if !bytes.Equal(c.got, c.want) {
			t.Errorf("%s = %x\nwant  %x", c.name, c.got, c.want)
		}
	}
	if bytes.Equal(got.key, want.psk) {
		t.Error("the link key is the API's psk: the two protocols share a key")
	}
}

// A device that answers the challenge as an inventoried satellite, under that
// satellite's key, gets a link that knows who it is.
//
// verifies SPEC §3.2.2
func TestNewLinkAuthenticatesTheDevice(t *testing.T) {
	host, device := newPipe(t)
	answered := make(chan error, 1)
	go func() { answered <- joins(device, 2) }()

	l, err := bridge.NewLink(host, keys)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}
	if err := <-answered; err != nil {
		t.Fatalf("device side: %v", err)
	}
	if l.Name() != kitchen {
		t.Errorf("Name() = %q, want %q", l.Name(), kitchen)
	}
}

// A nonce reused across connections would let a recorded answer be replayed.
//
// verifies SPEC §3.2.2
func TestEachChallengeIsFresh(t *testing.T) {
	seen := map[[bridge.NonceSize]byte]bool{}
	for range 8 {
		host, device := newPipe(t)
		nonce := make(chan bridge.Challenge, 1)
		go func() {
			_ = bridge.NewWriter(device).WriteFrame(helloOf(1).Frame())
			c, _ := readChallenge(device)
			nonce <- c
			_ = device.Close()
		}()
		_, _ = bridge.NewLink(host, keys)
		c := <-nonce
		if c.Nonce == ([bridge.NonceSize]byte{}) || seen[c.Nonce] {
			t.Fatalf("challenge %x is zero or was sent before", c.Nonce)
		}
		seen[c.Nonce] = true
	}
}

// refusal runs a device-side script against NewLink and returns its error.
func refusal(t *testing.T, device func(net.Conn) error) error {
	t.Helper()
	host, dev := newPipe(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = device(dev)
	}()
	l, err := bridge.NewLink(host, keys)
	_ = host.Close()
	<-done
	if err == nil {
		t.Fatalf("NewLink accepted the device as %q", l.Name())
	}
	return err
}

// The kitchen's name with a key that is not the kitchen's: someone on the
// LAN who knows what the satellite is called, but not its psk.
//
// verifies SPEC §3.2.2
func TestNewLinkRefusesAWrongPSK(t *testing.T) {
	err := refusal(t, func(c net.Conn) error {
		return handshake(c, helloOf(2), kitchen, []byte("the office satellite's own key!!"))
	})
	if !errors.Is(err, bridge.ErrBadMAC) {
		t.Errorf("err = %v, want ErrBadMAC", err)
	}
}

// verifies SPEC §3.2.2
func TestNewLinkRefusesANameTheInventoryDoesNotList(t *testing.T) {
	err := refusal(t, func(c net.Conn) error {
		return handshake(c, helloOf(2), "garage-sat", kitchenPSK())
	})
	if !errors.Is(err, bridge.ErrUnknownDevice) || !strings.Contains(err.Error(), "garage-sat") {
		t.Errorf("err = %v, want ErrUnknownDevice naming garage-sat", err)
	}
}

// The MAC covers the hello, so an on-path peer cannot rewrite the declared
// format: an answer made over a one-channel hello does not verify for two.
//
// verifies SPEC §3.2.2
func TestTheAnswerIsBoundToTheHelloItFollows(t *testing.T) {
	err := refusal(t, func(c net.Conn) error {
		w := bridge.NewWriter(c)
		if err := w.WriteFrame(helloOf(2).Frame()); err != nil {
			return err
		}
		ch, err := readChallenge(c)
		if err != nil {
			return err
		}
		key, _ := bridge.LinkKey(kitchenPSK())
		return w.WriteFrame(bridge.AnswerChallenge(key, ch, kitchen, helloOf(1)).Frame())
	})
	if !errors.Is(err, bridge.ErrBadMAC) {
		t.Errorf("err = %v, want ErrBadMAC", err)
	}
}

// Audio before the answer is refused, not buffered: nothing a device says
// counts until it has proved who it is.
//
// verifies SPEC §3.2.2
func TestNewLinkRefusesAudioBeforeAuth(t *testing.T) {
	err := refusal(t, func(c net.Conn) error {
		w := bridge.NewWriter(c)
		if err := w.WriteFrame(helloOf(1).Frame()); err != nil {
			return err
		}
		if _, err := readChallenge(c); err != nil {
			return err
		}
		return w.WriteFrame(bridge.Frame{Type: bridge.TypeMic, Payload: make([]byte, 64)})
	})
	if !errors.Is(err, bridge.ErrUnauthenticated) {
		t.Errorf("err = %v, want ErrUnauthenticated", err)
	}
}

// A version 2 satellite has no answer to give, so it is refused at its hello,
// before the host sends it anything, and the error names both versions.
//
// verifies SPEC §3.2.2
func TestNewLinkRefusesAVersion2DeviceBeforeChallengingIt(t *testing.T) {
	old := helloOf(2)
	old.Version = 2
	got := make(chan error, 1)
	err := refusal(t, func(c net.Conn) error {
		if err := bridge.NewWriter(c).WriteFrame(old.Frame()); err != nil {
			return err
		}
		f, err := bridge.NewReader(c).ReadFrame()
		if err == nil {
			got <- fmt.Errorf("the host sent a v2 device %s", f.Type)
		} else {
			got <- nil
		}
		return nil
	})
	if !errors.Is(err, bridge.ErrVersion) || !strings.Contains(err.Error(), "protocol 2") ||
		!strings.Contains(err.Error(), "speaks 3") {
		t.Errorf("err = %v, want ErrVersion naming protocol 2 and 3", err)
	}
	if e := <-got; e != nil {
		t.Error(e)
	}
}

func TestParseAuthRejectsMalformedAnswers(t *testing.T) {
	mac := make([]byte, bridge.MACSize)
	for name, p := range map[string][]byte{
		"empty":         nil,
		"mac only":      mac,
		"name too long": append(append([]byte(nil), mac...), bytes.Repeat([]byte("a"), bridge.MaxNameLen+1)...),
		"not utf-8":     append(append([]byte(nil), mac...), 0xff, 0xfe),
	} {
		if _, err := bridge.ParseAuth(p); err == nil {
			t.Errorf("%s: ParseAuth accepted it", name)
		}
	}
	if _, err := bridge.ParseChallenge(make([]byte, bridge.NonceSize-1)); err == nil {
		t.Error("ParseChallenge accepted a short nonce")
	}
	if _, err := bridge.LinkKey(make([]byte, 16)); err == nil {
		t.Error("LinkKey accepted a 16-byte psk")
	}
}
