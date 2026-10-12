# `chorus_bridge`

Full-duplex audio on an ESPHome satellite. Replaces `voice_assistant`, whose
half-duplex behaviour is a software state-machine guard rather than a hardware
limit (SPEC §3.1, ADR-0010).

```
components/chorus_bridge/   the external component (config schema + runtime)
packages/chorus-bridge.yaml the bridge, micro_wake_word and the sensitivity
                            select, consumed with !include + vars
satellite1.yaml             the FutureProofHomes Satellite1, real wiring;
                            what the firmware tasks build by default
satellite1-main.yaml        the same kit on the Chorus main board (ADR-0055)
satellite1.example.yaml     exercises the config schema; validated, not flashed
voice-pe.yaml               the Home Assistant Voice Preview Edition, real
                            wiring from the stock config (ADR-0073)
secrets.example.yaml        template; the real secrets.yaml is gitignored
```

Native API stays for control — entities, wake-word sensitivity, the mmWave
radar's "Room Presence", which the orchestrator journals as the room's
presence (ADR-0050), and the LED ring, which it drives. `satellite1.yaml`
and `voice-pe.yaml` declare the ring with a `Listening`, `Thinking` and
`Speaking` effect, and the orchestrator sets one per session state; a ring
without those three is left alone (ADR-0056). Its `Muted` and `Error` effects show the device's mute
and a failure the household should know about; a ring with only the first
three goes dark in those states instead (ADR-0069). Its `Unreachable` effect is the device's own:
it shows after 10 s without a sealed link to chorusd, which cannot show that for it
(ADR-0074). Its Speaker Volume number is how loud the satellite
is, 0 to 100, which the orchestrator sets when someone asks (ADR-0067).
Audio rides a raw TCP socket the device dials out on, because the native API
cannot carry audio without new message ids, which means forking `api.proto`.

## Wire protocol

The host end is `internal/bridge`. Both ends must change together, which is
why `external_components:` points at a local path and not a git ref.

The three handshake frames are a 4-byte big-endian header and a payload, in
the clear:

```
type:u8  flags:u8  length:u16  payload[length]
```

Every frame after them, both ways, is a sealed record (ADR-0074):

```
length:u16  AES-256-GCM(type:u8 flags:u8 payload)  tag[16]
```

The key is one per direction, derived in the handshake, and the nonce is
four zero bytes and a big-endian u64 counter per direction that never travels.
The length is the additional data. A record that does not open, because it
was altered, replayed, reordered, dropped or reflected, drops the link. A
sealed frame costs 20 bytes beyond its payload, 16 more than the plaintext
header did. `length` still makes an unknown type skippable once opened, so
newer firmware can add frames without breaking an older host.

The field allows 65,535 bytes; the device does not. Its receive buffer is
20 KB (`RX_CAPACITY`) and is never grown, because an allocation failure on
ESP-IDF is an abort, so a host record may be at most 20,478 bytes after its
length (`RX_CAPACITY - RECORD_HEADER_SIZE`). The device drops the link with
`frame too large` on a longer one rather than wait for a record it can never
hold. It seals and opens in place, so sealing adds no buffer. The host sends
TTS in 1,024-byte chunks (`internal/bridge/link.go`), so only a peer that is
not the host gets near the bound.

| Type | Dir | Payload |
|---|---|---|
| `0x01` hello | device | in the clear: version:u8, sample_rate:u32, bits:u8, mic_channels:u8, nonce:32, then the node name, UTF-8, 1-64 bytes |
| `0x02` mic | device | 16 kHz s16le PCM; `flags` is the channel: 0 the fully processed mix, 1 the XMOS's second, lighter-processed output (SPEC §3.2) |
| `0x03` wake | device | wake word name, UTF-8 |
| `0x04` played | device | cumulative DAC frames:u64, `esp_timer` micros:i64; `flags` echoes the tag of the stop this report answers, 0 otherwise |
| `0x05` mute | device | none; `flags` bit 0 hardware (the board's mute switch), bit 1 software (the `Microphone`'s own mute) |
| `0x06` auth | device | in the clear: the device's proof:32, sent only once the host's proof holds |
| `0x10` tts | host | 16 kHz s16le PCM |
| `0x11` stop | host | none — barge-in, discards the speaker buffer; `flags` is a tag, never 0 |
| `0x12` finish | host | none — drains the buffer, then stops |
| `0x13` duck | host | decibels:u8, duration_ms:u32; applied to the `ducking_speaker` source, never the voice |
| `0x14` mic_enable | host | none; `flags` bit 0 enables the uplink |
| `0x15` challenge | host | in the clear: nonce:32, fresh from a CSPRNG for every connection, then the host's proof:32 |

`played` is the truncation point and the reason this component exists in this
shape. ESPHome's `add_audio_output_callback` reports frames actually written to
the DAC with a microsecond stamp, so `frames / sample_rate` is what the user
*heard*. The best proxy available on the stock path is roughly ±128 ms — about
100x worse — and ADR-0005 makes this precision load-bearing for the whole DPO
corpus (SPEC §3.2.1).

Every `stop` is answered. The device gates its frame counter, discards the
buffer, and then emits exactly one `played` whose `flags` echo the stop's tag,
whether or not the position moved since its last report. That report is the
truncation point: a routine `played` can already be in flight when the host
sends the stop, and it predates the stop by up to a round trip, so the host
waits for the echoed tag rather than for the next position to arrive
(ADR-0033). Protocol version 2 is this change; a host at version 2 refuses a
device still announcing 1.

`mute` has no host-to-device counterpart. Hardware mute is authoritative.
The device sends one when its link is sealed and again on every change; the
host drops the room's audio while either bit is set.

## Handshake

A connection is not a satellite until it has proved it is one, and a host is
not the satellite's audience until it has proved it is chorusd (SPEC §3.2.2,
ADR-0074). Protocol version 4 opens every connection like this:

```
device → host  0x01 hello      04 00003e80 10 02 Nd[32] Ed[32] name   v4, 16 kHz, 16-bit, 2 channels, e.g. satellite1-4b2c10
host → device  0x15 challenge  Nh[32] Eh[32] host_proof[32]
device → host  0x06 auth       device_proof[32]
               ... every frame after this is sealed, both ways
```

```
k            = HKDF-SHA256(ikm = psk, salt = none, info = "chorus-bridge link v4")
host_proof   = HMAC-SHA256(k, "chorus-bridge v4 host proof"   || hello payload || Nh || Eh)
device_proof = HMAC-SHA256(k, "chorus-bridge v4 device proof" || hello payload || Nh || Eh)
s            = X25519(ed, Eh) = X25519(eh, Ed)
d2h, h2d     = HKDF-SHA256(ikm = s || k, salt = Nd || Nh, info = "chorus-bridge v4 device to host" | "... host to device")
```

`psk` is the satellite's `api_encryption_key`, decoded: the same 32 bytes
`devices.yaml` holds, passed to the component as `psk:` by the package. The
derivation keeps the link and the native API's Noise handshake from ever
sharing a key. `Ed` and `Eh` are each side's X25519 public key, drawn fresh per
connection; the private halves are wiped once `s` is taken. Both proofs cover
the whole hello as sent and the host's nonce and key, so the format, both
nonces, both public keys and the name are covered, and a peer on the path
cannot swap in its own keys. Each proof opens with its own role, so neither
can be reflected as the other. Because the session keys need `s`, a recorded
session stays sealed even if the PSK leaks later. A low-order public key,
whose `s` would be all zeros, is refused by either side before it sends
anything more; the host logs `weak key`. Each side does two X25519 scalar
multiplications per connection, which the device logs the time of.
`internal/bridge/testdata/handshake-v4.hex` holds the whole exchange and the
first sealed records each way for fixed keys, nonces and name, with RFC 7748's
test keys as the ephemeral ones; `go test ./internal/bridge` checks the host
against it and `task firmware:test` checks `chorus_auth.h`, `chorus_seal.h`
and `chorus_x25519.h`, the firmware's arithmetic, built for the host against
mbedTLS.

The host looks the name up in its inventory and proves itself only to a name
it holds a key for. It checks the device's proof under that satellite's key in
constant time, and requires the connection to come from that satellite's
address. Anything else is a disconnect, logged with its reason: `unknown
name`, `weak key`, `bad MAC`, `challenge refused` (the device hung up on the
host's proof, which is what a satellite with another key does), `wrong address`, `frame
before auth`, `protocol version`, or `timeout` (10 s, on chorusd's injected
clock). A verified connection for a satellite that already has a live link
closes the old one first; an unverified one cannot.

The device sends nothing but its hello until the host has proved the key. A
host that fails is hung up on with `host proof refused`, and the next dial
waits out a backoff that doubles from the reconnect interval to 60 s. Its
microphones start, and `mute`, `wake` and `played` flow, only once its own
answer is queued. It accepts no frame but a challenge until then, and redials
if a connect or the host's proof takes more than 15 s.

`on_connected` fires when a link is sealed and `on_disconnected` when the
satellite stops being connected, including when its first attempt after boot
fails or the host fails its proof. Each fires on a change, not on every
redial. `satellite1.yaml` and `voice-pe.yaml` show `Unreachable` on the ring
after 10 s down, and clear it on `on_connected`.

A host at version 4 refuses a device announcing 3 at its hello, before it
sends the device anything, with `device speaks chorus_bridge protocol 3, this
host speaks 4`. A version 3 host refuses a version 4 device the same way, so
there is no mixed-version mode: **reflash every satellite in step with the
host.**

## Hardware mute, passive sources, and other sharp edges

- **The hardware bit is the switch, not the `Microphone`.**
  `Microphone::get_mute_state()` is the flag `microphone.mute` sets, which
  zero-fills the audio in software, so it is the software bit. The hardware
  bit is the binary sensor `hardware_mute:` names: on the Satellite1, the
  HAT's slide switch on the XMOS's button port; on the Voice PE, the side
  switch on GPIO3. A board that names none never sets it, and a switch not
  yet read counts as on (ADR-0067).
- **Both microphone sources are non-passive.** `MicrophoneSource` is a
  per-consumer gate, not a hardware refcount: a `passive` source receives audio
  only while some non-passive consumer has started the mic. Registering passive
  ships a bridge that logs nothing and delivers no audio (SPEC §3.2).
- **The mic callback runs on the mic's FreeRTOS task**, not `loop()`. Blocking
  is legal there; touching sockets, the API, or `App` is not.
- **`add_audio_output_callback` reports a delta**, not a running total, and
  fires on the speaker's own task. The component accumulates into an atomic and
  emits the frame from `loop()`.
- **`host:` must be an IP.** ESPHome's `set_sockaddr` does not resolve names.
  This is validated at config time.
- **`voice_assistant:` must not also be present.** Its state machine would stop
  the mic during TTS, which is the limit being escaped.
- **`micro_wake_word` does not start itself.** The stock firmware starts it
  from `voice_assistant`, so the package starts it from `on_boot` instead, and
  runs it with `stop_after_detection: false`: the host opens a session only on
  a `wake` frame, and a detector that stopped after the first one would make
  the second wake of the day silent. The package's model is a stock one, pinned
  to a commit; the household's trained model replaces it under `models:`.

## Configuring a satellite

`satellite1.yaml` is the real Satellite1 wiring, tracked in git, and it is what
the firmware tasks build by default. Do not copy `satellite1.example.yaml`
over it: the example's I²S pins are generic ESP32-S3 placeholders, there so
the file validates standalone, and a board flashed with them is silent.

```sh
cp esphome/secrets.example.yaml esphome/secrets.yaml   # then fill it in
$EDITOR esphome/satellite1.yaml                        # orchestrator_host
task firmware:config
task firmware:compile      # downloads ESP-IDF on first run; this is slow
task firmware:upload
```

`orchestrator_host` is a literal IP on a static lease: ESPHome cannot resolve
a hostname for an outbound socket. `api_encryption_key` in `secrets.yaml` must
equal the `psk` for this satellite in the repo-root `devices.yaml`: the
orchestrator dials the device with it, and the device proves who it is on the
audio link with it. The `name` in `devices.yaml` is the device's node name as
it reports it, MAC suffix included (`satellite1-4b2c10`), because that is the
name the audio link's auth frame carries. `ota_password` is what `esphome upload`
presents and what the device demands before it accepts a flash; without one
anyone on the LAN can reflash a device that carries a microphone.

A kit on the Chorus main board builds `satellite1-main.yaml` instead, with
`task firmware:config -- satellite1-main.yaml`. The example validates the same
way, and CI validates all four configs on every push.

## Configuring a Voice PE

`voice-pe.yaml` is the Home Assistant Voice Preview Edition, its pins, XMOS,
DAC, ring, dial and buttons taken from the stock
`esphome/home-assistant-voice-pe` config at the commit its header names. It
uses the same package, secrets and steps:

```sh
$EDITOR esphome/voice-pe.yaml                          # orchestrator_host
task firmware:config -- voice-pe.yaml
task firmware:compile -- voice-pe.yaml
task firmware:upload -- voice-pe.yaml
```

Stock firmware's OTA has no password, so the first upload over it is not
refused for one, and USB is the fallback; from then on the device demands
`ota_password`. The node
name stays `home-assistant-voice` with its MAC suffix, so the inventory entry
is `home-assistant-voice-xxxxxx` with `profile: voice-pe`. Every config here
reads the one `secrets.yaml`, so two satellites built from it share its
`api_encryption_key` unless it is changed between builds; each `psk` in
`devices.yaml` is whatever its device was built with.

What it has that the host reads: the LED Ring with Chorus's five effects,
the Speaker Volume number, which the dial turns 5 a detent, the side switch
as the `mute` frame's hardware bit, and the wake-word sensitivity select. It
has no radar, so no presence. The centre button is a wake: a short press
calls the bridge's wake hook with the name `button`, and the host confirms
it on the next utterance as it does any wake. The stock config's media
player, timers, sounds, Sendspin, BLE setup and factory-reset hold are left
out. Channel 1 is AEC+IC+NS, set by `voice_kit` at boot; `channel_1_stage:
NONE` would make it the bare mic.

It has been validated with `esphome config`, not compiled against a device or
flashed.

## Proving full duplex

The point of this component is that the microphone keeps delivering audio while
the speaker is playing. That cannot be proven without a device.

1. `task firmware:upload && task firmware:logs`
2. `task probe` — confirms the native API still answers, independent of the
   audio socket.
3. Accept the bridge connection on port 6055, completing the handshake with
   the satellite's `psk` (`bridge.Listen` does, given the inventory), send
   `0x10` TTS frames, and check that `0x02` mic frames keep arriving *during*
   playback. Half duplex shows up
   as a gap in mic frames for the length of the utterance.
4. Speak over the playback, send `0x11` stop with a non-zero `flags` tag, and
   compare the `0x04` played report that echoes it against the text offset
   you stopped at. No echo means firmware older than the host. SPEC §3.2.1
   expects sub-millisecond error; anything near ±100 ms means the position is
   being estimated from bytes sent rather than read from the DAC callback.
