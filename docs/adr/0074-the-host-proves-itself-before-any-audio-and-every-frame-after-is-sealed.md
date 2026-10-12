# 0074. The host proves itself before any audio, and every frame after is sealed

- **Status:** accepted · refines [ADR-0066](0066-the-device-proves-which-satellite-it-is-before-any-audio.md) and [ADR-0069](0069-say-a-failure-the-household-would-meet-as-silence-and-show-what-cannot-be-said.md)
- **Source:** SPEC §3.2.2, §13, §14 · ADR-0009, ADR-0017, ADR-0033, ADR-0066, ADR-0069

Protocol 3 authenticated the device and nothing else (ADR-0066, Forecloses).
A satellite proved its name to whoever answered on `orchestrator_host`, then
streamed its microphone to it, and every frame both ways was plaintext. So
anything that took the host's address heard the room. Anyone who could
capture LAN traffic heard the room and the replies. A peer on the path that
could inject TCP segments could speak into a verified link. SPEC §14 listed
the fix, and the device's half of it, a satellite that cannot reach chorusd,
was still dark on the ring.

Protocol 4 makes the host prove the satellite's key over both sides' nonces
before the device sends anything but its hello. Every frame after the
handshake is sealed with AES-256-GCM under a key per direction, derived from
the key and both nonces.

```go
import (
	"errors"

	"github.com/teagan42/chorus/internal/bridge"
)

// Something on orchestrator_host's address without the kitchen's key: no proof, no microphone.
func hostWithoutTheKey(err error) bool { return errors.Is(err, bridge.ErrHostUnproven) }

// A frame altered, replayed or reordered on the wire: it does not open, and the link drops.
func tamperedInFlight(err error) bool { return errors.Is(err, bridge.ErrBadRecord) }
```

## The handshake

The link key is ADR-0066's, under a new label, so a v3 key and a v4 key
never coincide. The handshake is three frames, all in the clear under the
4-byte header.

```
k    = HKDF-SHA256(ikm = psk, salt = none, info = "chorus-bridge link v4")

device → host  0x01 hello      04 00003e80 10 02 · Nd[32] · name
host → device  0x15 challenge  Nh[32] · HMAC-SHA256(k, "chorus-bridge v4 host proof"   || hello payload || Nh)
device → host  0x06 auth               HMAC-SHA256(k, "chorus-bridge v4 device proof" || hello payload || Nh)

d2h  = HKDF-SHA256(ikm = k, salt = Nd || Nh, info = "chorus-bridge v4 device to host")
h2d  = HKDF-SHA256(ikm = k, salt = Nd || Nh, info = "chorus-bridge v4 host to device")
```

1. The device draws *Nd* from `esp_fill_random` (`random_bytes`) and sends the
   hello: version, format, *Nd* and its node name.
2. The host checks the version and format and looks the name up. An unknown
   name is dropped with nothing sent: the host proves itself only to a
   satellite it holds a key for. It draws *Nh* and sends it with its proof.
3. The device recomputes the host's proof over the hello it sent and compares
   in constant time. On a mismatch it hangs up without proving anything,
   starts no microphone, and backs off before the next dial, doubling from
   the reconnect interval to 60 s. It resets on the next proof that holds.
4. Only then does the device send its own proof, and derive both session
   keys. Its microphones start once the answer is queued.
5. The host verifies the device's proof in constant time, derives the same
   keys, and checks the address as ADR-0066 does.

Both proofs cover the whole hello, so a peer on the path cannot rewrite the
format, the nonce or the name. Only the name varies in length, and *Nh* after
it is fixed, so no input needs a length prefix. Each proof opens with its
role, and the two labels differ at the same byte, so neither side's proof can
be reflected as the other's. The device's proof is made only for a host that
has already proved the key, so the device is never an oracle for a host that
does not hold it.

`internal/bridge/testdata/handshake-v4.hex` fixes every byte for fixed keys,
nonces and name, through the first two sealed frames each way.
`internal/bridge/auth_test.go` checks the Go side against it. On the
firmware's side, `chorus_auth.h` and `chorus_seal.h` are built for the host
against mbedTLS and checked against the same file by `task firmware:test`.
That test also covers a tampered, replayed, reordered, reflected and
truncated record. Python's `hmac` and `cryptography`'s AESGCM agree with both.

## Sealed frames

```
length:u16 · AES-256-GCM(key, nonce = 0x00000000 || seq:u64, aad = length)(type:u8 · flags:u8 · payload) · tag[16]
```

- **The counter never travels.** `seq` starts at zero in each direction and
  each side knows the next one. A replayed, reordered or dropped record opens
  under the wrong nonce and fails, and so does one reflected back the way it
  came, under the other direction's key. TCP is ordered, so an explicit
  sequence number would buy nothing but 8 bytes a frame.
- **Any failure drops the link.** On the host, `Serve` returns
  `ErrBadRecord`, and the connection closes with it. On the device,
  `pump_downlink_` disconnects. Nothing in a record that does not open is
  acted on, and a truncated record is an error, not a clean hang-up.
- **Type and flags are sealed.** A capture shows record lengths and timing,
  not which frame is a wake, a stop or a mute. Lengths still tell audio from
  control (see Forecloses).
- **Overhead.** A v3 frame's header was 4 bytes; a sealed frame costs 20: the
  length, the sealed type and flags, and the tag. That is 16 bytes more a
  frame. A 32 ms mic chunk goes from 1,028 to 1,044 bytes on the wire, 1.6%.
  Both mic channels add about 1 kB/s uplink, and TTS adds 0.5 kB/s down. The
  handshake is three frames of 60, 68 and 36 bytes for a 17-byte name.
- **CPU.** On the host, sealing one 32 ms chunk takes about 0.45 µs
  (`BenchmarkSeal32ms`, AES-NI). On the ESP32-S3 it is estimated, not
  measured, at 0.1 to 0.3 ms a chunk. ESP-IDF routes these calls to
  `esp_aes_gcm.c` (`MBEDTLS_GCM_ALT` under `MBEDTLS_HARDWARE_AES`), which runs
  AES-CTR on the AES peripheral and GHASH in software, since the S3 has no
  GCM hardware. Two mic chunks sealed and one TTS chunk opened per 32 ms is
  then 1 to 3% of a core. The handshake's six HMACs run once per connection.
  The hardware tier is where this gets measured.

## The firmware

- **AES-GCM over mbedTLS, not ChaCha20-Poly1305.** ESP-IDF 5.5, which ESPHome
  2026.7 builds against, defaults `MBEDTLS_GCM_C` on and
  `MBEDTLS_CHACHAPOLY_C`, `MBEDTLS_CHACHA20_C` and `MBEDTLS_POLY1305_C` off
  (`components/mbedtls/Kconfig`). The S3 has AES in hardware, and ESPHome
  does not touch either option. HKDF stays spelled out as an extract and one
  expand block, since `MBEDTLS_HKDF_C` is still off by default.
- **No new buffers.** A record is sealed in place in `tx_`, after a length
  placeholder and before room for the tag, within the capacity `setup()`
  reserves. It is opened in place in `rx_`. A TTS record opened while the
  speaker queue is full stays at the head of `rx_`, marked open, and is never
  opened twice. In-place decryption is how mbedTLS's own TLS record layer
  uses GCM. `gcm.h`'s note against it is stricter than `gcm.c` and
  `esp_aes_gcm.c`, which refuse only an output that starts ahead of its
  input. The two GCM contexts are the only new state, under 1 kB.
- **A dropped mic chunk spends no counter.** `queue_frame_` checks the queue's
  room before it seals, so a chunk left in the ring for the next `loop()` is
  sealed only once, when it is queued.
- **A connect that hangs ends.** lwIP retries a SYN for over a minute, so the
  device abandons a connect after the same 15 s it gives the handshake. A
  host that is down then reads as down, and the ring can say so.
- **Keys are wiped.** The PSK is wiped in `setup()` as before. Session keys
  are wiped on the stack once set, and `mbedtls_gcm_free` drops each key
  schedule when the link drops.

## The ring when chorusd is out of reach

chorusd drives the ring over the native API (ADR-0056, ADR-0069), so a
satellite that cannot reach it has nobody to show that. The device shows it
itself. `chorus_bridge` fires `on_connected` when a link is sealed and
`on_disconnected` when it stops being connected. Each fires on a change, never
on each redial. `on_disconnected` also fires when the first attempt after boot
fails, so a satellite that never reaches chorusd still says so. A refused host
proof is one such failure: to the household, a host without the key is a host
the satellite cannot reach.

`satellite1.yaml` and `voice-pe.yaml` run a script on `on_disconnected` that
waits 10 s, then shows `Unreachable` if the link is still down: four violet
points turning once in 6 s, unlike any effect chorusd sets. The wait keeps a
redial, or chorusd restarting, off the ring. `on_connected` stops the script
and turns the ring off only if it still shows `Unreachable`, so a state chorusd
set in the meantime is left alone. `satellite1.example.yaml` wires the two
triggers straight to its ring, as a schema example. chorusd's ring contract
is unchanged: it never sets `Unreachable`, and a ring without it works as it
did.

## What an attacker on the LAN can do now

- **Cannot** take `orchestrator_host`'s address and receive a microphone, or
  speak into the room. A host without the key fails its proof, and the
  device sends it nothing but the hello.
- **Cannot** hear the room or the replies from a capture, or tell a wake from
  a stop by its type.
- **Cannot** inject, alter, replay, reorder, drop or reflect a frame inside
  a link without the link dropping.
- **Cannot** replay a recorded handshake to either end: the other side's
  fresh nonce makes the old proof worthless, and the session keys new.
- **Can** see each satellite's name, its nonces and the proofs, which reveal
  nothing about the key beyond what an HMAC does. The name is on mDNS anyway.
- **Can** see record lengths and timing. Those show when someone is
  speaking, when TTS plays, and roughly how long a wake word's name is.
- **Can** cut a link, by RST or by dropping packets. That is a denial of
  service, as it was, and the ring now shows it after 10 s.
- **Can**, with the PSK in hand later, decrypt any recorded session. See
  Forecloses.

## Alternatives rejected

**The device proves first, then the host.** A challenge-response each way in
the old order makes the device answer every host that dials, so anything
holding `orchestrator_host` collects device proofs for nonces of its choosing.
It also costs a round trip before audio. With the name in the hello, the host
can prove first. The host is then an oracle only for proofs over its own fresh
nonce, which serve nobody but a relay that ends up forwarding sealed bytes
between the real ends.

**Noise, as the native API runs it** (ADR-0009). ESPHome links `noise-c`
only for `api:` encryption, so the bridge would build on another component's
library dependency. Noise's ChaCha20-Poly1305 also runs in software on the
hot path, where AES has hardware. A Noise transport is a second length-framed
layer under ours, and its NNpsk0 pattern would still need the name carried in
a prologue. Its one real gain, forward secrecy, is weighed under Forecloses.

**TLS with a PSK ciphersuite.** Go's `crypto/tls` has no PSK ciphersuites, so
the host would need a third-party TLS stack. mbedTLS's SSL layer adds tens of
kilobytes of RAM per connection on a device whose buffers are already sized
by hand.

**ChaCha20-Poly1305 via mbedTLS.** It would need `sdkconfig_options` in every
board config to turn on three modules that ESP-IDF leaves off, and it runs in
software.

**Explicit sequence numbers, or rekeying.** TCP delivers in order, so a
counter both sides keep is enough. At about 100 records a second a 64-bit
counter is never spent, and a link does not outlive its TCP connection. Both
sides refuse to wrap it anyway.

**Keep version 3 alongside.** A version 3 device would stream plaintext to an
unproven host, which is the hole. As in ADR-0066, both halves version
together (ADR-0017), and the refusal names the firmware as the half to flash.

## Forecloses

- **No forward secrecy.** Session keys come from the PSK and two nonces
  that travel in the clear. Anyone who records a session and later gets the
  PSK, from `devices.yaml`, `secrets.yaml` or a flash dump, can decrypt it.
  The native API's Noise does not have this weakness. Closing it means an
  ephemeral X25519 exchange in the handshake, mbedTLS ECDH on the device and
  `crypto/ecdh` on the host, at the cost of a scalar multiplication per
  connection and a longer handshake.
- **Lengths and timing leak.** Records are not padded. Padding mic and TTS
  records to one size would hide little that timing does not show anyway,
  and control frames are rare.
- **A key mismatch reads on the host as a hang-up.** The device refuses
  first, so chorusd logs `challenge refused` and says the satellite may hold
  another key, rather than `bad MAC`. The device's log names the failed
  proof.
- **The host proves itself before the address check.** Anything that dials
  from an inventoried address and names an inventoried satellite gets a host
  proof over its hello and the host's nonce. That buys it nothing, but the
  address check could move ahead of the proof if it ever needs to.
- **Mixed-version deployments.** A host built from this checkout refuses a
  satellite announcing 3 at its hello, and a version 3 host refuses a version 4
  satellite the same way. Every satellite is reflashed in step with the host.
- **Validated short of a flash.** The handshake and sealing arithmetic is
  checked on the host against mbedTLS 2.28, and the headers compile against
  mbedTLS 3.6.4's. The whole component has been syntax-checked against
  ESPHome 2026.7.2's headers, and the configs pass `esphome config`. Nothing
  has been compiled for the ESP32-S3 or flashed, and the CPU figures above are
  an estimate until the hardware tier runs.
