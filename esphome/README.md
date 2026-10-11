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
secrets.example.yaml        template; the real secrets.yaml is gitignored
```

Native API stays for control — entities, wake-word sensitivity, the mmWave
radar's "Room Presence", which the orchestrator journals as the room's
presence (ADR-0050), and the LED ring, which it drives. `satellite1.yaml`
declares the ring with a `Listening`, `Thinking` and `Speaking` effect, and
the orchestrator sets one per session state; a ring without those three is
left alone (ADR-0056).
Audio rides a raw TCP socket the device dials out on, because the native API
cannot carry audio without new message ids, which means forking `api.proto`.

## Wire protocol

The host end is `internal/bridge`. Both ends must change together, which is
why `external_components:` points at a local path and not a git ref.

Every frame is a 4-byte big-endian header and a payload:

```
type:u8  flags:u8  length:u16  payload[length]
```

`length` makes an unknown type skippable, so newer firmware can add frames
without breaking an older host.

The field allows 65,535 bytes; the device does not. Its receive buffer is
20 KB (`RX_CAPACITY`) and is never grown, because an allocation failure on
ESP-IDF is an abort, so a host frame's payload may be at most 20,476 bytes
(`RX_CAPACITY - HEADER_SIZE`). The device drops the link with `frame too
large` on a longer one rather than wait for a frame it can never hold. The
host sends TTS in 1,024-byte chunks (`internal/bridge/link.go`), so only a
peer that is not the host gets near the bound.

| Type | Dir | Payload |
|---|---|---|
| `0x01` hello | device | version:u8, sample_rate:u32, bits:u8, mic_channels:u8 |
| `0x02` mic | device | 16 kHz s16le PCM; `flags` is the channel: 0 the fully processed mix, 1 the XMOS's second, lighter-processed output (SPEC §3.2) |
| `0x03` wake | device | wake word name, UTF-8 |
| `0x04` played | device | cumulative DAC frames:u64, `esp_timer` micros:i64; `flags` echoes the tag of the stop this report answers, 0 otherwise |
| `0x05` mute | device | none; `flags` bit 0 hardware, bit 1 software |
| `0x10` tts | host | 16 kHz s16le PCM |
| `0x11` stop | host | none — barge-in, discards the speaker buffer; `flags` is a tag, never 0 |
| `0x12` finish | host | none — drains the buffer, then stops |
| `0x13` duck | host | decibels:u8, duration_ms:u32; applied to the `ducking_speaker` source, never the voice |
| `0x14` mic_enable | host | none; `flags` bit 0 enables the uplink |

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

## Hardware mute, passive sources, and other sharp edges

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
orchestrator dials the device with it. `ota_password` is what `esphome upload`
presents and what the device demands before it accepts a flash; without one
anyone on the LAN can reflash a device that carries a microphone.

A kit on the Chorus main board builds `satellite1-main.yaml` instead, with
`task firmware:config -- satellite1-main.yaml`. The example validates the same
way, and CI validates all three on every push.

## Proving full duplex

The point of this component is that the microphone keeps delivering audio while
the speaker is playing. That cannot be proven without a device.

1. `task firmware:upload && task firmware:logs`
2. `task probe` — confirms the native API still answers, independent of the
   audio socket.
3. Accept the bridge connection on port 6055, send `0x10` TTS frames, and check
   that `0x02` mic frames keep arriving *during* playback. Half duplex shows up
   as a gap in mic frames for the length of the utterance.
4. Speak over the playback, send `0x11` stop with a non-zero `flags` tag, and
   compare the `0x04` played report that echoes it against the text offset
   you stopped at. No echo means firmware older than the host. SPEC §3.2.1
   expects sub-millisecond error; anything near ±100 ms means the position is
   being estimated from bytes sent rather than read from the DAC callback.
