# 0066. The device proves which satellite it is before any audio

- **Status:** accepted · refines [ADR-0010](0010-chorus-bridge-external-component.md) and [ADR-0033](0033-the-stop-is-tagged-and-its-answer-is-the-cut.md)
- **Source:** SPEC §1, §3.2.2, §13 · ADR-0009, ADR-0017

Port 6055 carried a room's microphone and the speech played into it as
plaintext PCM, and the only check was that the source address was in the
inventory (`satelliteAt` in `cmd/chorusd/run.go`, before any frame). The
hello carried no identity. So anyone on the LAN who could take or spoof a
satellite's IP could listen to that room or talk into it, and a second
connection from the same IP replaced the link map entry while both
supervisors kept running. Each satellite already shares a 32-byte PSK with
the host, its `api.encryption.key`, for the native API on 6053 (ADR-0009); it
went unused on 6055. SPEC §1's "trusted network" is a scope statement, not a
reason to let any host on it be a microphone's audience.

Protocol version 3 authenticates the device with that PSK before any audio
moves. It does not encrypt.

```go
import (
	"errors"

	"github.com/teagan42/chorus/internal/bridge"
)

// The kitchen's name with someone else's key: refused, and the log says so.
func answeredUnderTheWrongKey(err error) bool { return errors.Is(err, bridge.ErrBadMAC) }
```

## The handshake

The device sends its hello, as before. The host checks the version and
format, then sends `0x15 challenge`: 32 bytes from `crypto/rand`, fresh per
connection. The device answers with `0x06 auth`: a 32-byte MAC followed by its
ESPHome node name.

```
k   = HKDF-SHA256(ikm = psk, salt = none, info = "chorus-bridge auth v3")
mac = HMAC-SHA256(k, "chorus-bridge v3" || nonce || name || hello payload)
```

The key is derived rather than the PSK used raw, so the link's MAC and the
native API's Noise handshake never run under one key. The MAC covers the
hello, so a peer on the path cannot rewrite the declared format either. The
name is the only variable-length field in the input, so no length prefix is
needed. `internal/bridge/testdata/handshake-v3.hex` fixes every byte for a
fixed key, nonce and name. The Go side is checked against it by
`internal/bridge/auth_test.go`. The firmware's arithmetic, `chorus_auth.h`,
is built for the host against mbedTLS and checked against the same file by
`task firmware:test`. Python's `hmac` agrees with both.

## A satellite is its name, its key and its address

The host looks the claimed name up in the inventory, verifies the MAC under
that satellite's PSK with `hmac.Equal`, and then requires the source address
to be the one the inventory gives that name. An address the inventory does
not name is still refused before a frame is read. Each refusal is logged with
its reason: `unknown name`, `bad MAC`, `wrong address`, `frame before auth`,
`protocol version`, or `timeout` on the injected `helloTimeout`. Nothing but
the answer is accepted before it verifies, and nothing is sent but the
challenge.

The name is the node name as the device reports it, MAC suffix included,
which is what `devices.example.yaml` already showed. The PSK reaches the
component as a required `psk:` option that the package fills from the
`api_encryption_key` secret the `api:` block already reads, so a household
configures nothing new. `__init__.py` decodes it at config time and refuses a
key that is not 32 bytes. `esphome config` still prints it as the secret's
name.

## One live link per satellite

A verified connection for a satellite that already has a live link closes the
old one, and waits for that link's supervisor to finish tearing down before
starting its own: a reboot that never dropped the old TCP connection now
leaves one supervisor, not two. A connection that fails to verify never
touches the live link, or anyone on the LAN could knock a satellite off by
dialling in.

## The firmware

`chorus_bridge` derives the link key once in `setup()` and wipes the PSK. On
connect it queues the hello and nothing else: its microphones start, and
`mute`, `wake` and `played` flow, only once the answer is queued, and
`queue_frame_` refuses every other type before then. It accepts no host frame
but a challenge until it has answered, and redials if none arrives within
15 s. Buffers are fixed: the MAC input and the auth payload are stack arrays
bounded by the 64-byte name limit. Exceptions stay off, and the `weak_ptr`
handoff is untouched. It uses `mbedtls_md_hmac` only, and spells HKDF out as
an extract and one expand block, because ESP-IDF 5.5's
`components/mbedtls/Kconfig` defaults `MBEDTLS_HKDF_C` to off.

## Alternatives rejected

**The PSK raw as the HMAC key.** It works, but it would put one key under two
protocols. A derived key costs two HMACs once per boot.

**Keep identifying by address, and only check a MAC under that address's
key.** That stops a host without the key, but leaves the name out of the
proof. A satellite that moves to another inventoried satellite's address would
then be served as that satellite until its MAC failed for an unclear reason.
Naming the satellite in the answer makes the refusal say which check failed.

**Encrypt now**, with Noise as the native API does, or an AEAD stream keyed
from the same derivation. It is the stronger fix, but it means porting a
Noise or AEAD framing into the C++ component. Every audio frame on the
device's hot path would gain a cipher, a nonce and a tag. That is a larger
change to make, and one to validate on hardware for the duplex timings SPEC
§3.3.2 measured, which this change cannot be.

**Accept both versions at the handshake.** A version 2 device has no answer,
so accepting it is accepting the hole. As in ADR-0033, both halves version
together (ADR-0017), and the refusal names the firmware as the half to flash.

## Forecloses

- **Encryption is deferred, not done.** The stream is still plaintext: anyone
  who can capture LAN traffic hears the room and the replies. There is no
  per-frame MAC either, so a peer on the path that can inject TCP segments
  can still speak into a link after it has verified. What this closes is the
  cheaper attack: taking or spoofing an address, with no key, and being
  served as a satellite.
- **The host is not authenticated.** The device proves itself to whoever
  answers on `orchestrator_host`. A host that takes that address and reads
  the device's hello learns nothing secret, but it does receive the
  microphone, which only encryption with mutual authentication would stop.
- **Mixed-version deployments.** A host built from this checkout refuses a
  satellite announcing 2, and a version 3 satellite waits for a challenge a
  version 2 host never sends, then redials. Every satellite is reflashed in
  step with the host.
- **The key is the YAML's.** It is compiled in from `api_encryption_key`. A
  key changed over the native API at runtime (`save_noise_psk`) reaches the
  API, not the bridge. Chorus never changes it, and `devices.yaml` holds the
  YAML key.
- **The firmware has been validated with `esphome config`.** Its handshake
  arithmetic has also been built on the host against the golden frames. It
  has not been flashed. The hardware tier's tests now complete the handshake
  with the inventory's key, and will say whether the device answers.
