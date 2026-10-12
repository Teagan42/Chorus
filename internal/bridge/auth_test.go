package bridge_test

import (
	"bufio"
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/handshake-v4.hex from the inputs in it")

const goldenPath = "testdata/handshake-v4.hex"

// golden is the handshake and the first sealed frames on fixed inputs. The
// firmware's host test reads the same file, so both ends are held to the
// same bytes.
type golden struct {
	name    string
	lines   map[string][]byte
	ordered []string
}

// goldenInputs are the lines nothing derives; every other line is derived.
// The two ephemeral private keys are fixed here so the file is
// deterministic; on the wire each is fresh per connection.
var goldenInputs = []string{"psk", "device_nonce", "host_nonce", "device_ephemeral_private", "host_ephemeral_private"}

func readGolden(t *testing.T) golden {
	t.Helper()
	f, err := os.Open(goldenPath)
	if err != nil {
		t.Fatalf("open golden frames: %v", err)
	}
	defer func() { _ = f.Close() }()
	g := golden{lines: map[string][]byte{}}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("golden line %q has no value", line)
		}
		v = strings.TrimSpace(v)
		if k == "name" {
			g.name = v
			continue
		}
		b, err := hex.DecodeString(v)
		if err != nil {
			t.Fatalf("golden %s: %v", k, err)
		}
		g.lines[k] = b
		g.ordered = append(g.ordered, k)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read golden frames: %v", err)
	}
	return g
}

// goldenHello is the living-room Satellite1's: both mic channels.
func goldenHello(name string, nonce []byte, ephemeral [bridge.PublicKeySize]byte) bridge.Hello {
	h := helloOf(2)
	h.Name = name
	copy(h.Nonce[:], nonce)
	h.Ephemeral = ephemeral
	return h
}

// x25519 is a fixed private key, for vectors and for tests that need to
// know both ends of an exchange.
func x25519(t *testing.T, priv []byte) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		t.Fatalf("x25519 private key: %v", err)
	}
	return k
}

// The sealed frames the golden file fixes, in the order each side sends them.
var (
	goldenUplink = []bridge.Frame{
		{Type: bridge.TypeMic, Flags: bridge.ChannelAEC, Payload: []byte{0x01, 0x02, 0x03, 0x04}},
		bridge.Mute{Hardware: true}.Frame(),
	}
	goldenDownlink = []bridge.Frame{
		{Type: bridge.TypeTTS, Payload: []byte{0x0a, 0x0b, 0x0c, 0x0d}},
		{Type: bridge.TypeStop, Flags: 7},
	}
)

// derive computes every derived line from the golden inputs, in file order.
func (g golden) derive(t *testing.T) ([]string, map[string][]byte) {
	t.Helper()
	psk, dn, hn := g.lines["psk"], g.lines["device_nonce"], g.lines["host_nonce"]
	if len(psk) != bridge.PSKSize || len(dn) != bridge.NonceSize || len(hn) != bridge.NonceSize || g.name == "" {
		t.Fatal("golden inputs are malformed")
	}
	key, err := bridge.LinkKey(psk)
	if err != nil {
		t.Fatal(err)
	}
	devPriv := x25519(t, g.lines["device_ephemeral_private"])
	hostPriv := x25519(t, g.lines["host_ephemeral_private"])
	h := goldenHello(g.name, dn, bridge.PublicOf(devPriv))
	c := bridge.Challenge{Ephemeral: bridge.PublicOf(hostPriv)}
	copy(c.Nonce[:], hn)
	c.Proof = bridge.HostProof(key, h, c)
	a := bridge.AnswerChallenge(key, h, c)
	shared, err := bridge.SharedSecret(devPriv, c.Ephemeral)
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := bridge.SharedSecret(hostPriv, h.Ephemeral)
	if err != nil || !bytes.Equal(shared, mirror) {
		t.Fatalf("the two ends' X25519 secrets differ: %x, %x (%v)", shared, mirror, err)
	}
	up, down, err := bridge.SessionKeys(key, shared, h, c)
	if err != nil {
		t.Fatal(err)
	}
	order := []string{
		"device_ephemeral", "host_ephemeral", "shared_secret",
		"hello", "link_key", "host_proof", "challenge", "device_proof", "auth",
		"key_device_to_host", "key_host_to_device", "uplink_0", "uplink_1", "downlink_0", "downlink_1",
	}
	out := map[string][]byte{
		"device_ephemeral":   h.Ephemeral[:],
		"host_ephemeral":     c.Ephemeral[:],
		"shared_secret":      shared,
		"hello":              wire(t, h.Frame()),
		"link_key":           key,
		"host_proof":         c.Proof[:],
		"challenge":          wire(t, c.Frame()),
		"device_proof":       a.Proof[:],
		"auth":               wire(t, a.Frame()),
		"key_device_to_host": up,
		"key_host_to_device": down,
	}
	for i, rec := range sealAll(t, up, goldenUplink) {
		out[fmt.Sprintf("uplink_%d", i)] = rec
	}
	for i, rec := range sealAll(t, down, goldenDownlink) {
		out[fmt.Sprintf("downlink_%d", i)] = rec
	}
	return order, out
}

// sealAll seals frames in order under key and returns each record.
func sealAll(t *testing.T, key []byte, frames []bridge.Frame) [][]byte {
	t.Helper()
	var recs [][]byte
	var b bytes.Buffer
	w, err := bridge.NewSealedWriter(&b, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frames {
		b.Reset()
		if err := w.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, bytes.Clone(b.Bytes()))
	}
	return recs
}

func wire(t *testing.T, f bridge.Frame) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := bridge.NewWriter(&b).WriteFrame(f); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func writeGolden(t *testing.T, g golden, order []string, derived map[string][]byte) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`# The chorus_bridge v4 handshake and its first sealed frames on fixed
# inputs (ADR-0074). The first six lines are inputs, the two ephemeral
# private keys among them fixed only so this file is deterministic; they are
# RFC 7748 section 6.1's, so its public keys and shared secret check the
# three lines after them. Every line after the inputs is derived. Frames are
# whole, header included; uplink_N and downlink_N are sealed records,
# counter N, carrying the frames auth_test.go names.
# Checked by internal/bridge/auth_test.go and, on the firmware's side, by
# esphome/test/chorus_auth_test.cpp.
# Regenerate with: go test ./internal/bridge -run Golden -update
`)
	for _, k := range goldenInputs {
		fmt.Fprintf(&b, "%s %x\n", k, g.lines[k])
	}
	fmt.Fprintf(&b, "name %s\n", g.name)
	for _, k := range order {
		fmt.Fprintf(&b, "%s %x\n", k, derived[k])
	}
	if err := os.WriteFile(goldenPath, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The handshake's and the first sealed frames' bytes are fixed by their
// inputs, so a change to the key schedule, either proof, the record layout
// or the counter nonce is a diff here, and the firmware's host test fails on
// the same file.
//
// verifies SPEC §3.2.2
func TestHandshakeGoldenFrames(t *testing.T) {
	g := readGolden(t)
	order, got := g.derive(t)
	if *updateGolden {
		writeGolden(t, g, order, got)
		return
	}
	for _, k := range order {
		want, ok := g.lines[k]
		if !ok {
			t.Errorf("golden file has no %s line", k)
			continue
		}
		if !bytes.Equal(got[k], want) {
			t.Errorf("%s = %x\nwant  %x", k, got[k], want)
		}
	}
	if bytes.Equal(got["link_key"], g.lines["psk"]) {
		t.Error("the link key is the API's psk: the two protocols share a key")
	}
	if bytes.Equal(got["key_device_to_host"], got["key_host_to_device"]) {
		t.Error("both directions seal under one key")
	}
	if bytes.Equal(got["host_proof"], got["device_proof"]) {
		t.Error("the host's proof is the device's: one could be reflected as the other")
	}
	for name, secret := range map[string][]byte{
		"the device's private key": g.lines["device_ephemeral_private"],
		"the host's private key":   g.lines["host_ephemeral_private"],
		"the shared secret":        got["shared_secret"],
	} {
		if bytes.Contains(got["hello"], secret) || bytes.Contains(got["challenge"], secret) {
			t.Errorf("%s crosses the wire", name)
		}
	}
}

// A device that holds the inventory's key for the name it claims gets a link
// that knows who it is, and the device's own check of the host passes.
//
// verifies SPEC §3.2.2
func TestNewLinkAuthenticatesTheDevice(t *testing.T) {
	l, _, _ := joined(t, 2)
	if l.Name() != kitchen {
		t.Errorf("Name() = %q, want %q", l.Name(), kitchen)
	}
}

// A host nonce reused across connections would let a recorded answer be
// replayed; each challenge draws a fresh one.
//
// verifies SPEC §3.2.2
func TestEachChallengeIsFresh(t *testing.T) {
	seen := map[[bridge.NonceSize]byte]bool{}
	for range 8 {
		host, device := newPipe(t)
		nonce := make(chan bridge.Challenge, 1)
		go func() {
			h := named(kitchen, 1)
			_ = bridge.NewWriter(device).WriteFrame(h.Frame())
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
		_ = dev.Close()
	}()
	l, err := bridge.NewLink(host, keys)
	_ = host.Close()
	<-done
	if err == nil {
		t.Fatalf("NewLink accepted the device as %q", l.Name())
	}
	return err
}

// officePSK is a 32-byte key that is not the kitchen's.
func officePSK() []byte { return []byte("the office satellite's own key!!") }

// A satellite flashed with a key that is not the inventory's: the host's
// proof fails on it, so it hangs up before proving anything, and the host
// says that is what a wrong key looks like.
//
// verifies SPEC §3.2.2
func TestADeviceWithAnotherKeyRefusesTheHost(t *testing.T) {
	var joinErr error
	err := refusal(t, func(c net.Conn) error {
		_, joinErr = handshake(c, helloOf(2), kitchen, officePSK())
		return joinErr
	})
	if !errors.Is(joinErr, bridge.ErrHostUnproven) {
		t.Errorf("device err = %v, want ErrHostUnproven", joinErr)
	}
	if !errors.Is(err, bridge.ErrChallengeRefused) || !strings.Contains(err.Error(), kitchen) {
		t.Errorf("host err = %v, want ErrChallengeRefused naming %s", err, kitchen)
	}
}

// impostor sends hello as name and answers whatever the host sends with a
// proof under psk, without checking the host: someone on the LAN who knows
// what the satellite is called, but not its key.
func impostor(c net.Conn, h bridge.Hello, psk []byte, answer func(key []byte, h bridge.Hello, ch bridge.Challenge) bridge.Auth) error {
	if err := bridge.NewWriter(c).WriteFrame(h.Frame()); err != nil {
		return err
	}
	ch, err := readChallenge(c)
	if err != nil {
		return err
	}
	key, err := bridge.LinkKey(psk)
	if err != nil {
		return err
	}
	return bridge.NewWriter(c).WriteFrame(answer(key, h, ch).Frame())
}

// named is a hello as name, with a fresh ephemeral key whose private half
// is thrown away: enough for a host to answer, not to finish an exchange.
func named(name string, channels uint8) bridge.Hello {
	h := helloOf(channels)
	h.Name = name
	priv, err := bridge.NewEphemeral()
	if err != nil {
		panic(err)
	}
	h.Ephemeral = bridge.PublicOf(priv)
	return h
}

// verifies SPEC §3.2.2
func TestNewLinkRefusesAProofUnderTheWrongKey(t *testing.T) {
	err := refusal(t, func(c net.Conn) error {
		return impostor(c, named(kitchen, 2), officePSK(), bridge.AnswerChallenge)
	})
	if !errors.Is(err, bridge.ErrBadMAC) {
		t.Errorf("err = %v, want ErrBadMAC", err)
	}
}

// The host's own proof sent back as the device's: the labels differ, so a
// device that holds no key cannot answer with what the host just proved.
//
// verifies SPEC §3.2.2
func TestNewLinkRefusesItsOwnProofReflected(t *testing.T) {
	err := refusal(t, func(c net.Conn) error {
		return impostor(c, named(kitchen, 2), officePSK(), func(_ []byte, _ bridge.Hello, ch bridge.Challenge) bridge.Auth {
			return bridge.Auth{Proof: ch.Proof}
		})
	})
	if !errors.Is(err, bridge.ErrBadMAC) {
		t.Errorf("err = %v, want ErrBadMAC", err)
	}
}

// A name the inventory does not list gets no challenge at all: the host
// proves itself only to a satellite it has a key for.
//
// verifies SPEC §3.2.2
func TestNewLinkRefusesANameTheInventoryDoesNotList(t *testing.T) {
	sent := make(chan error, 1)
	err := refusal(t, func(c net.Conn) error {
		if err := bridge.NewWriter(c).WriteFrame(named("garage-sat", 2).Frame()); err != nil {
			return err
		}
		f, err := bridge.NewReader(c).ReadFrame()
		if err == nil {
			sent <- fmt.Errorf("the host sent an unknown name %s", f.Type)
		} else {
			sent <- nil
		}
		return nil
	})
	if !errors.Is(err, bridge.ErrUnknownDevice) || !strings.Contains(err.Error(), "garage-sat") {
		t.Errorf("err = %v, want ErrUnknownDevice naming garage-sat", err)
	}
	if e := <-sent; e != nil {
		t.Error(e)
	}
}

// The device's proof covers the hello, so an on-path peer cannot rewrite the
// declared format: a proof over a one-channel hello does not verify for two.
//
// verifies SPEC §3.2.2
func TestTheDevicesProofIsBoundToTheHelloItFollows(t *testing.T) {
	err := refusal(t, func(c net.Conn) error {
		return impostor(c, named(kitchen, 2), kitchenPSK(), func(key []byte, h bridge.Hello, ch bridge.Challenge) bridge.Auth {
			h.MicChannels = 1
			return bridge.AnswerChallenge(key, h, ch)
		})
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
		if err := w.WriteFrame(named(kitchen, 1).Frame()); err != nil {
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

// A version 3 satellite sends no nonce and cannot open a sealed frame, so it
// is refused at its hello, before the host sends it anything, and the error
// names both versions.
//
// verifies SPEC §3.2.2
func TestNewLinkRefusesAVersion3DeviceBeforeChallengingIt(t *testing.T) {
	// A version 3 hello, as ADR-0066's golden file has it.
	v3 := bridge.Frame{Type: bridge.TypeHello, Payload: []byte{0x03, 0x00, 0x00, 0x3e, 0x80, 0x10, 0x02}}
	got := make(chan error, 1)
	err := refusal(t, func(c net.Conn) error {
		if err := bridge.NewWriter(c).WriteFrame(v3); err != nil {
			return err
		}
		f, err := bridge.NewReader(c).ReadFrame()
		if err == nil {
			got <- fmt.Errorf("the host sent a v3 device %s", f.Type)
		} else {
			got <- nil
		}
		return nil
	})
	if !errors.Is(err, bridge.ErrVersion) || !strings.Contains(err.Error(), "protocol 3") ||
		!strings.Contains(err.Error(), "speaks 4") {
		t.Errorf("err = %v, want ErrVersion naming protocol 3 and 4", err)
	}
	if e := <-got; e != nil {
		t.Error(e)
	}
}

// challengeFor is a fresh challenge whose private key the caller discards.
func challengeFor(key []byte, h bridge.Hello) bridge.Challenge {
	c, _, err := bridge.NewChallenge(key, h)
	if err != nil {
		panic(err)
	}
	return c
}

// fakeHost reads the device's hello and answers with whatever challenge
// answer builds from it, then reports whether the device sent anything more.
func fakeHost(t *testing.T, psk []byte, answer func(key []byte, h bridge.Hello) bridge.Challenge) (joinErr, after error) {
	t.Helper()
	host, device := newPipe(t)
	result := make(chan error, 1)
	go func() {
		_, err := joins(device, 2)
		result <- err
		_ = device.Close()
	}()
	f, err := bridge.NewReader(host).ReadFrame()
	if err != nil {
		t.Fatalf("read hello: %v", err)
	}
	h, err := bridge.ParseHello(f.Payload)
	if err != nil {
		t.Fatalf("parse hello: %v", err)
	}
	key, err := bridge.LinkKey(psk)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.NewWriter(host).WriteFrame(answer(key, h).Frame()); err != nil {
		t.Fatalf("send challenge: %v", err)
	}
	joinErr = <-result
	if f, err := bridge.NewReader(host).ReadFrame(); err == nil {
		after = fmt.Errorf("the device sent %s to a host it should have refused", f.Type)
	}
	return joinErr, after
}

// A host that does not hold the satellite's key, such as one that has taken
// orchestrator_host's address, gets no proof and no microphone: the device
// hangs up before it proves anything.
//
// verifies SPEC §3.2.2
func TestJoinRefusesAHostWithoutTheKey(t *testing.T) {
	joinErr, after := fakeHost(t, officePSK(), func(key []byte, h bridge.Hello) bridge.Challenge {
		return challengeFor(key, h)
	})
	if !errors.Is(joinErr, bridge.ErrHostUnproven) {
		t.Errorf("Join err = %v, want ErrHostUnproven", joinErr)
	}
	if after != nil {
		t.Error(after)
	}
}

// Reflection: a host answering with the proof the device itself would send
// for this very handshake. The roles' labels differ, so it does not verify.
//
// verifies SPEC §3.2.2
func TestJoinRefusesItsOwnProofReflected(t *testing.T) {
	joinErr, after := fakeHost(t, kitchenPSK(), func(key []byte, h bridge.Hello) bridge.Challenge {
		c := bridge.Challenge{Nonce: h.Nonce, Ephemeral: h.Ephemeral}
		c.Proof = bridge.DeviceProof(key, h, c)
		return c
	})
	if !errors.Is(joinErr, bridge.ErrHostUnproven) {
		t.Errorf("Join err = %v, want ErrHostUnproven", joinErr)
	}
	if after != nil {
		t.Error(after)
	}
}

// The host's proof covers the hello, so a peer on the path that rewrites the
// device's declared format, its nonce, its ephemeral key or its name breaks
// the proof, and the device refuses rather than stream under a format it did
// not declare or a key exchange it did not make.
//
// verifies SPEC §3.2.2
func TestJoinRefusesAProofOverARewrittenHello(t *testing.T) {
	for name, rewrite := range map[string]func(*bridge.Hello){
		"channels":  func(h *bridge.Hello) { h.MicChannels = 1 },
		"nonce":     func(h *bridge.Hello) { h.Nonce[0] ^= 1 },
		"ephemeral": func(h *bridge.Hello) { h.Ephemeral[0] ^= 1 },
		"name":      func(h *bridge.Hello) { h.Name = "office-sat" },
	} {
		t.Run(name, func(t *testing.T) {
			joinErr, after := fakeHost(t, kitchenPSK(), func(key []byte, h bridge.Hello) bridge.Challenge {
				rewrite(&h)
				return challengeFor(key, h)
			})
			if !errors.Is(joinErr, bridge.ErrHostUnproven) {
				t.Errorf("Join err = %v, want ErrHostUnproven", joinErr)
			}
			if after != nil {
				t.Error(after)
			}
		})
	}
}

// recorded is one genuine session as a LAN observer captures it: each side's
// bytes, in order, handshake and sealed frames alike.
type recorded struct {
	uplink, downlink []byte
	challenge        bridge.Challenge
	auth             bridge.Auth
}

// record runs a real session, the kitchen sending one mic chunk, and keeps
// what crossed the wire.
func record(t *testing.T) recorded {
	t.Helper()
	host, device := newPipe(t)
	upTap := &wireCopy{Conn: device}
	downTap := &wireCopy{Conn: host}
	joinedc := make(chan error, 1)
	go func() {
		dc, err := joins(upTap, 1)
		if err == nil {
			err = dc.WriteFrame(bridge.Frame{Type: bridge.TypeMic, Payload: []byte("alan, in the kitchen")})
		}
		joinedc <- err
	}()
	l, err := bridge.NewLink(downTap, keys)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}
	c := newCollector()
	serve(t, l, c)
	if err := <-joinedc; err != nil {
		t.Fatalf("device side: %v", err)
	}
	c.await(t, "the recorded mic chunk", func() bool { return len(c.mic) == 1 })
	_ = l.Close()

	var r recorded
	r.uplink, r.downlink = upTap.bytes(), downTap.bytes()
	// The challenge is the whole of the downlink before any sealed frame.
	rd := bridge.NewReader(bytes.NewReader(r.downlink))
	f, err := rd.ReadFrame()
	if err != nil || f.Type != bridge.TypeChallenge {
		t.Fatalf("recorded downlink opens with %v, %v", f.Type, err)
	}
	if r.challenge, err = bridge.ParseChallenge(f.Payload); err != nil {
		t.Fatal(err)
	}
	ru := bridge.NewReader(bytes.NewReader(r.uplink))
	for _, want := range []bridge.Type{bridge.TypeHello, bridge.TypeAuth} {
		f, err := ru.ReadFrame()
		if err != nil || f.Type != want {
			t.Fatalf("recorded uplink has %v, %v where %s belongs", f.Type, err, want)
		}
		if want == bridge.TypeAuth {
			r.auth, _ = bridge.ParseAuth(f.Payload)
		}
	}
	return r
}

// wireCopy keeps a copy of everything written through it.
type wireCopy struct {
	net.Conn
	buf lockedBuffer
}

func (r *wireCopy) Write(b []byte) (int, error) {
	n, err := r.Conn.Write(b)
	r.buf.write(b[:n])
	return n, err
}

func (r *wireCopy) bytes() []byte { return r.buf.bytes() }

// Nonce reuse across sessions, the device's half: a whole recorded uplink,
// hello, proof and sealed audio, replayed byte for byte into a new
// connection. The host's fresh nonce makes the old proof worthless, and
// nothing of the recording is delivered.
//
// verifies SPEC §3.2.2
func TestARecordedUplinkReplayedIsRefused(t *testing.T) {
	r := record(t)
	hello, err := bridge.NewReader(bytes.NewReader(r.uplink)).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	rest := r.uplink[len(wire(t, hello)):]
	host, device := newPipe(t)
	go func() {
		_ = bridge.NewWriter(device).WriteFrame(hello)
		_, _ = readChallenge(device)
		_, _ = device.Write(rest)
	}()
	_, err = bridge.NewLink(host, keys)
	if !errors.Is(err, bridge.ErrBadMAC) {
		t.Errorf("err = %v, want ErrBadMAC", err)
	}
}

// Nonce reuse across sessions, the host's half: a recorded challenge, proof
// and all, replayed to the kitchen by something holding orchestrator_host.
// The device's fresh nonce makes the old proof worthless.
//
// verifies SPEC §3.2.2
func TestARecordedChallengeReplayedIsRefused(t *testing.T) {
	r := record(t)
	joinErr, after := fakeHost(t, kitchenPSK(), func([]byte, bridge.Hello) bridge.Challenge { return r.challenge })
	if !errors.Is(joinErr, bridge.ErrHostUnproven) {
		t.Errorf("Join err = %v, want ErrHostUnproven", joinErr)
	}
	if after != nil {
		t.Error(after)
	}
}

// Reflection across sessions: the device's proof from a recorded session,
// sent back to it as a host's proof with that session's host nonce.
//
// verifies SPEC §3.2.2
func TestARecordedDeviceProofReflectedIsRefused(t *testing.T) {
	r := record(t)
	joinErr, after := fakeHost(t, kitchenPSK(), func([]byte, bridge.Hello) bridge.Challenge {
		return bridge.Challenge{Nonce: r.challenge.Nonce, Ephemeral: r.challenge.Ephemeral, Proof: r.auth.Proof}
	})
	if !errors.Is(joinErr, bridge.ErrHostUnproven) {
		t.Errorf("Join err = %v, want ErrHostUnproven", joinErr)
	}
	if after != nil {
		t.Error(after)
	}
}

// Fixed X25519 private keys: the kitchen's ephemeral, a second one of its,
// the host's, and a peer on the path's.
var (
	kitchenEphemeral = bytes.Repeat([]byte{0x11}, 32)
	kitchenAgain     = bytes.Repeat([]byte{0x22}, 32)
	hostEphemeral    = bytes.Repeat([]byte{0x33}, 32)
	mitmEphemeral    = bytes.Repeat([]byte{0x44}, 32)
)

// exchange is one handshake's inputs with every secret known to the test.
func exchange(t *testing.T, devPriv, hostPriv []byte, nonce byte) (key, shared []byte, h bridge.Hello, c bridge.Challenge) {
	t.Helper()
	key, err := bridge.LinkKey(kitchenPSK())
	if err != nil {
		t.Fatal(err)
	}
	d, hp := x25519(t, devPriv), x25519(t, hostPriv)
	h = helloOf(2)
	h.Name = kitchen
	h.Nonce[0] = nonce
	h.Ephemeral = bridge.PublicOf(d)
	c = bridge.Challenge{Ephemeral: bridge.PublicOf(hp)}
	c.Nonce[31] = nonce
	if shared, err = bridge.SharedSecret(d, c.Ephemeral); err != nil {
		t.Fatal(err)
	}
	return key, shared, h, c
}

// Either side's fresh nonce makes the session keys new, and the two
// directions never share one, so a record cannot be replayed into another
// session or reflected back the way it came.
//
// verifies SPEC §3.2.2
func TestSessionKeysAreFreshPerNonceAndDirection(t *testing.T) {
	key, shared, h, c := exchange(t, kitchenEphemeral, hostEphemeral, 0)
	up, down, err := bridge.SessionKeys(key, shared, h, c)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(up, down) {
		t.Error("both directions share a key")
	}
	c2 := c
	c2.Nonce[0] = 1
	h2 := h
	h2.Nonce[1] = 1
	for name, keys := range map[string][2][]byte{
		"a new host nonce":   must2(bridge.SessionKeys(key, shared, h, c2)),
		"a new device nonce": must2(bridge.SessionKeys(key, shared, h2, c)),
	} {
		if bytes.Equal(keys[0], up) || bytes.Equal(keys[1], down) {
			t.Errorf("%s left a session key as it was", name)
		}
	}
}

// Two sessions under one PSK with the same nonces, as a broken RNG or a
// replay would give them, still seal under different keys when either
// side's ephemeral differs: the X25519 secret is in the key schedule.
//
// verifies SPEC §3.2.2
func TestSessionKeysAreFreshPerEphemeral(t *testing.T) {
	key, shared, h, c := exchange(t, kitchenEphemeral, hostEphemeral, 7)
	first := must2(bridge.SessionKeys(key, shared, h, c))
	up, down := first[0], first[1]
	for name, ends := range map[string][2][]byte{
		"a new device ephemeral": {kitchenAgain, hostEphemeral},
		"a new host ephemeral":   {kitchenEphemeral, mitmEphemeral},
	} {
		key2, shared2, h2, c2 := exchange(t, ends[0], ends[1], 7)
		if h2.Nonce != h.Nonce || c2.Nonce != c.Nonce {
			t.Fatal("the two sessions' nonces differ; the test proves nothing")
		}
		keys := must2(bridge.SessionKeys(key2, shared2, h2, c2))
		if bytes.Equal(keys[0], up) || bytes.Equal(keys[1], down) {
			t.Errorf("%s left a session key as it was", name)
		}
	}
}

// Forward secrecy, as far as a test can show it: a recorded session, with the
// satellite's PSK leaked afterwards. Everything that crossed the wire and the
// PSK give the hello, the challenge and both public keys, but not the X25519
// secret, so no key a recorder can derive opens the first sealed frame.
//
// verifies SPEC §3.2.2
func TestARecordedSessionStaysSealedWhenThePSKLeaks(t *testing.T) {
	r := record(t)
	ru := bridge.NewReader(bytes.NewReader(r.uplink))
	hf, _ := ru.ReadFrame()
	h, err := bridge.ParseHello(hf.Payload)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = ru.ReadFrame() // the auth
	firstSealed := r.uplink[len(wire(t, hf))+bridge.HeaderSize+bridge.MACSize:]
	key, err := bridge.LinkKey(kitchenPSK())
	if err != nil {
		t.Fatal(err)
	}
	// The recorder's guesses: protocol 3's lack of a secret, and an exchange
	// of its own with each public key it saw.
	mitm := x25519(t, mitmEphemeral)
	guesses := map[string][]byte{"an all-zero secret": make([]byte, 32)}
	for name, pub := range map[string][32]byte{"the device's key": h.Ephemeral, "the host's key": r.challenge.Ephemeral} {
		s, err := bridge.SharedSecret(mitm, pub)
		if err != nil {
			t.Fatal(err)
		}
		guesses["its own exchange with "+name] = s
	}
	for name, shared := range guesses {
		up, _, err := bridge.SessionKeys(key, shared, h, r.challenge)
		if err != nil {
			t.Fatal(err)
		}
		sr, err := bridge.NewSealedReader(bytes.NewReader(firstSealed), up)
		if err != nil {
			t.Fatal(err)
		}
		if f, err := sr.ReadFrame(); !errors.Is(err, bridge.ErrBadRecord) {
			t.Errorf("with the PSK and %s, the recording opened: %s, %v", name, f.Type, err)
		}
	}
}

// mitm relays the kitchen's handshake to a real host, rewriting the hello on
// the way up and the challenge on the way down, and returns both ends'
// errors.
func mitm(t *testing.T, up func(*bridge.Hello), down func(*bridge.Challenge)) (joinErr, linkErr error) {
	t.Helper()
	devHost, devDev := newPipe(t)
	hostHost, hostDev := newPipe(t)
	joinc := make(chan error, 1)
	go func() {
		_, err := joins(devDev, 2)
		joinc <- err
		_ = devDev.Close()
	}()
	linkc := make(chan error, 1)
	go func() {
		_, err := bridge.NewLink(hostHost, keys)
		linkc <- err
		_ = hostHost.Close()
	}()
	go func() {
		defer func() { _ = hostDev.Close() }()
		f, err := bridge.NewReader(devHost).ReadFrame()
		if err != nil {
			return
		}
		h, err := bridge.ParseHello(f.Payload)
		if err != nil {
			return
		}
		up(&h)
		if bridge.NewWriter(hostDev).WriteFrame(h.Frame()) != nil {
			return
		}
		c, err := readChallenge(hostDev)
		if err != nil {
			return
		}
		down(&c)
		if bridge.NewWriter(devHost).WriteFrame(c.Frame()) != nil {
			return
		}
		_, _ = io.Copy(hostDev, devHost)
	}()
	return <-joinc, <-linkc
}

// A peer on the path that holds no PSK swaps in its own ephemeral key each
// way, to sit between two X25519 exchanges. Both proofs cover both public
// keys, so the device sees a host proof over a hello it did not send, and
// refuses before it proves anything.
//
// verifies SPEC §3.2.2
func TestAPeerSwappingEphemeralKeysIsRefused(t *testing.T) {
	theirs := bridge.PublicOf(x25519(t, mitmEphemeral))
	for name, swap := range map[string]struct {
		up   func(*bridge.Hello)
		down func(*bridge.Challenge)
	}{
		"both ways":       {func(h *bridge.Hello) { h.Ephemeral = theirs }, func(c *bridge.Challenge) { c.Ephemeral = theirs }},
		"the device's":    {func(h *bridge.Hello) { h.Ephemeral = theirs }, func(*bridge.Challenge) {}},
		"the host's only": {func(*bridge.Hello) {}, func(c *bridge.Challenge) { c.Ephemeral = theirs }},
	} {
		t.Run(name, func(t *testing.T) {
			joinErr, linkErr := mitm(t, swap.up, swap.down)
			if !errors.Is(joinErr, bridge.ErrHostUnproven) {
				t.Errorf("Join err = %v, want ErrHostUnproven", joinErr)
			}
			if !errors.Is(linkErr, bridge.ErrChallengeRefused) {
				t.Errorf("NewLink err = %v, want ErrChallengeRefused", linkErr)
			}
		})
	}
}

// lowOrder are the Curve25519 u-coordinates whose X25519 output is all zeros
// for every clamped scalar, non-canonical encodings included.
var lowOrder = map[string]string{
	"zero":     "0000000000000000000000000000000000000000000000000000000000000000",
	"one":      "0100000000000000000000000000000000000000000000000000000000000000",
	"order 8":  "e0eb7a7c3b41b8ae1656e3faf19fc46ada098deb9c32b1fd866205165f49b800",
	"order 8'": "5f9c95bca3508c24b1d0b1559c83ef5b04445cc4581c8e86d8224eddd09f1157",
	"p-1":      "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	"p":        "edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	"p+1":      "eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
}

func lowOrderPoint(t *testing.T, s string) [bridge.PublicKeySize]byte {
	t.Helper()
	var out [bridge.PublicKeySize]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(out) {
		t.Fatalf("low-order point %q: %v", s, err)
	}
	copy(out[:], b)
	return out
}

// A device whose ephemeral key is a low-order point would have the link
// sealed under an all-zero secret anyone can compute. The host refuses it
// before it sends a challenge, so it proves nothing to such a hello.
//
// verifies SPEC §3.2.2
func TestTheHostRefusesALowOrderEphemeral(t *testing.T) {
	for name, u := range lowOrder {
		t.Run(name, func(t *testing.T) {
			sent := make(chan error, 1)
			err := refusal(t, func(c net.Conn) error {
				h := named(kitchen, 2)
				h.Ephemeral = lowOrderPoint(t, u)
				if err := bridge.NewWriter(c).WriteFrame(h.Frame()); err != nil {
					return err
				}
				if f, err := bridge.NewReader(c).ReadFrame(); err == nil {
					sent <- fmt.Errorf("the host sent %s to a low-order key", f.Type)
				} else {
					sent <- nil
				}
				return nil
			})
			if !errors.Is(err, bridge.ErrWeakKey) {
				t.Errorf("err = %v, want ErrWeakKey", err)
			}
			if e := <-sent; e != nil {
				t.Error(e)
			}
		})
	}
}

// The device's half: even a host that proves the key, over a low-order
// ephemeral of its own, gets no answer, since the link would be sealed
// under a secret anyone on the LAN can compute.
//
// verifies SPEC §3.2.2
func TestTheDeviceRefusesALowOrderEphemeral(t *testing.T) {
	for name, u := range lowOrder {
		t.Run(name, func(t *testing.T) {
			joinErr, after := fakeHost(t, kitchenPSK(), func(key []byte, h bridge.Hello) bridge.Challenge {
				c := bridge.Challenge{Ephemeral: lowOrderPoint(t, u)}
				c.Nonce[0] = 1
				c.Proof = bridge.HostProof(key, h, c)
				return c
			})
			if !errors.Is(joinErr, bridge.ErrWeakKey) {
				t.Errorf("Join err = %v, want ErrWeakKey", joinErr)
			}
			if after != nil {
				t.Error(after)
			}
		})
	}
}

func must2(a, b []byte, err error) [2][]byte {
	if err != nil {
		panic(err)
	}
	return [2][]byte{a, b}
}

func TestParseHandshakeFramesRejectsMalformedOnes(t *testing.T) {
	for name, p := range map[string][]byte{
		"empty":     nil,
		"short":     make([]byte, bridge.MACSize-1),
		"long":      make([]byte, bridge.MACSize+1),
		"challenge": make([]byte, bridge.NonceSize+bridge.MACSize),
	} {
		if _, err := bridge.ParseAuth(p); err == nil {
			t.Errorf("%s: ParseAuth accepted it", name)
		}
	}
	if _, err := bridge.ParseChallenge(make([]byte, bridge.NonceSize)); err == nil {
		t.Error("ParseChallenge accepted a nonce with no proof")
	}
	if _, err := bridge.LinkKey(make([]byte, 16)); err == nil {
		t.Error("LinkKey accepted a 16-byte psk")
	}
	if _, err := bridge.Join(new(bytes.Buffer), helloOf(1), kitchenPSK()); err == nil {
		t.Error("Join accepted a hello that names no satellite")
	}
}
