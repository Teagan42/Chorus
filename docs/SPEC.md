# Chorus — a concurrent voice assistant for ESPHome satellites

Chorus replaces Home Assistant's Assist pipeline for ESP32 voice satellites
(FutureProofHomes Satellite1, HA Voice Preview Edition). It exists because
Assist's lifecycle is rigid in three specific ways: the assistant cannot speak
and act at the same time, it cannot be interrupted without losing the
conversation, and its trace data is too thin to learn from.

## 1. Non-goals

- Not a Home Assistant integration. HA is one tool backend among several.
- Not a media player. Playback is delegated to HA media players.
- Not multi-tenant. Single household, trusted network.
- Not a fleet product. 2–10 satellites, 1–2 concurrent conversations.

## 2. Architecture

```
┌─ satellite ────────────┐
│ micro_wake_word (ESP32)│  wake word opens a session
│ XMOS XU316: AEC/NS/AGC │  mic never stops, even during playback
│ chorus_bridge (custom) │  raw TCP out ──┐
└────────────────────────┘                │
         ▲ native API (control: LED, state)│
         │                                 ▼
┌────────┴─────────────────────────────────────────────┐
│ orchestrator (Go)                                    │
│  device gateway · session supervisor · tool registry  │
│  event journal (source of truth)                      │
└───┬────────────┬───────────┬───────────┬──────────────┘
    │            │           │           │
   STT          LLM         TTS      speaker-ID     (model sidecars, HTTP)
 Parakeet  Qwen3/Ollama   Kokoro    TitaNet-L
    │
┌───┴──────────────────┐   ┌─────────────────────┐
│ Postgres (JSONB log) │   │ review UI (Go+htmx) │
│ audio blobs (files)  │   │ dataset export      │
└──────────────────────┘   └─────────────────────┘
```

Polyglot by seam: Go where it's a concurrent socket server, Python where the
ecosystem is Python. No shoehorning either direction.

## 3. Device layer

### 3.1 Why a custom component

Both devices run ESPHome and speak only the native API (protobuf over TCP 6053,
Noise `NNpsk0_25519_ChaChaPoly_SHA256`, PSK = `api.encryption.key`). **The device
is the TCP server; the controller is the client.** Replacing HA means being an
API client that dials out — `aioesphomeapi` is the reference implementation.

Stock `voice_assistant` is half-duplex, which kills barge-in. The limit is a
pure software guard: three `set_state_(State::STOP_MICROPHONE, ...)` calls in
`voice_assistant.cpp` (:919, :981, :1087). `MicrophoneSource` is a per-consumer
gate over a listener-refcounted device, and `micro_wake_word` is also a
non-passive consumer running with `stop_after_detection: false` — so the I2S mic
and the XMOS AEC pipeline never actually stop during TTS. Only
`voice_assistant`'s own `enabled_` flag flips.

Audio cannot ride the native API without forking: new message ids require
editing `api.proto` and re-running ESPHome's codegen. `VoiceAssistantAudio`
(106) dispatches to the `voice_assistant` singleton; `SerialProxy` is bound to a
physical UART and marked experimental; user-service args would encode PCM as
unpacked sint32 or base64 at 32 kB/s.

**Resolution: `chorus_bridge`, an `external_components:` package with its own
socket.** No fork of ESPHome, no fork of either device's firmware. Audio over
raw TCP on `components/socket/socket.h` — the device dials out, so no inbound
port, but non-blocking connect and reconnect are ours to write: ESPHome ships
no managed outbound-link helper. Native API retained for control only.
`voice_assistant` is removed from the YAML entirely.

### 3.2 Component contract

- Two `MicrophoneSource` consumers on one `Microphone`: channel 0 and
  channel 1, each with its own ring buffer. This is what stock does. Both are
  the XMOS's outputs, and by default neither is a bare mic: channel 0 is AEC,
  interference cancelling, noise suppression and AGC on both devices; channel
  1 is AEC+IC+NS on the Satellite1's firmware. On the Voice PE the ESP32 sets
  the stage each channel is taken after, over the XMOS's control interface at
  every boot (`voice_kit`'s `channel_0_stage` and `channel_1_stage`), and
  `esphome/voice-pe.yaml` sets channel 1 to AEC+IC+NS as well, which is also
  the stock component's default. It could as well be the unprocessed mic, so
  what channel 1 carries is the firmware's setting, not something the host
  can assume.
- Callbacks fire on the **mic's FreeRTOS task, not `loop()`**. Signature
  `void(const std::vector<uint8_t> &)`. Not an ISR, so blocking is legal, but
  sockets / API / `App` must not be touched there.
- Handoff is `weak_ptr` + `lock()` into a ring buffer, copied verbatim from
  `voice_assistant.cpp:38-52`. The deliberate `weak_ptr` makes teardown
  race-free; the task may fire once more after the buffer is dropped. Do not
  simplify this.
- Delivered chunk size is the mic task's choice (`READ_DURATION_MS`), not ours.
  Send chunks are sized in `loop()`. Use
  `audio::RingBufferAudioSource::create(rb, SEND_BUFFER_SIZE, sizeof(int16_t))`
  for frame-aligned reads.
- Audio format: 16 kHz, 16-bit signed mono. Stock constants: 512 ms ring buffer,
  32 ms (1024 B) send chunks, 16 KB speaker buffer.
- Software mute zero-fills but keeps firing: a silent stream is not a stopped
  stream.
- `micro_wake_word` stays on-device and is **fully independent of
  `voice_assistant`** — it declares its own `Component` and `MicrophoneSource`
  and references nothing in it. Detections via YAML
  (`on_wake_word_detected: lambda: id(bridge).on_wake_word(wake_word);`) or by
  attaching to `get_wake_word_detected_trigger()`. `get_vad_state()` gives free
  on-device VAD for gating the uplink.
- **Gotcha that ships a silently dead bridge:** `MicrophoneSource` is a
  per-consumer gate, not a hardware refcount. `start()` only touches the mic
  when `!passive_`, and the callback guard is `enabled_ || passive_`. A
  `passive` source receives audio only while some *non-passive* consumer has
  started the mic. `chorus_bridge` registers **non-passive**.

### 3.2.1 Speaker output and the truncation point

`speaker.h`: `play()` returns **bytes actually buffered**, which is the
backpressure signal — loop on the remainder. `stop()` is barge-in (immediate,
discards buffer); `finish()` is natural end of utterance. `has_buffered_data()`
is boolean only; there is no queue-depth accessor.

Feed the stock `announcement_resampling_speaker` at 16 kHz / 16-bit / mono via
`set_audio_stream_info()`; the `resampler → mixer → i2s_audio` stack converts up.
Ducking is `SourceSpeaker::apply_ducking(db, duration)` (both stock YAMLs use
20 dB) — not on the base class. The `duck` frame applies it to the mixer
source the YAML names as `ducking_speaker`, which on the Satellite1 is the
media source, not the voice. Nothing sends one yet (ADR-0056).

**Playback position is DAC-accurate.** `add_audio_output_callback` reports the
frames written to the DAC *since the last callback* — a per-DMA-buffer delta,
not a running total — plus an `esp_timer` microsecond timestamp. The component
accumulates it; `frames_played / sample_rate` is then the exact position the
user has *heard*. Correlated with our own byte-offset-to-text map, truncation
error is bounded by the DAC FIFO plus analog amp delay — sub-millisecond
(Voice PE documents `fixed_delay: 480 us`).

Use this callback. Do not use a proxy: the best proxy on the stock path is
`bytes_sent_duration − [0.384, 0.512] s` (HA deliberately keeps its ring buffer
75% full), i.e. ~±128 ms, about 100× worse. The callback fires on the speaker's
own task — copy and hand off, do no work in it.

### 3.2.2 Both ends prove the satellite's key before any audio, and the link is sealed

The audio link carries a room's microphone one way and speech into it the
other, so a connection is not a satellite until it has proved it is one, and
a host is not the satellite's audience until it has proved it is chorusd.
Protocol version 4 authenticates both ends and seals every frame after the
handshake (ADR-0074, refining ADR-0066).

- *k* = HKDF-SHA256(PSK, no salt, info `"chorus-bridge link v4"`), where the
  PSK is the satellite's `api.encryption.key`, the inventory's `psk`. The
  derivation keeps the link and the native API's Noise handshake from ever
  sharing a key.
- Each side draws an X25519 key pair fresh for the connection: *ed*, *Ed* on
  the device and *eh*, *Eh* on the host. The private halves never leave their
  side and are wiped, or on the host dropped, once the shared secret
  *s* = X25519(*ed*, *Eh*) = X25519(*eh*, *Ed*) is taken.
- The device sends its hello: version, format, 32 fresh bytes of its own
  from a CSPRNG (*Nd*), *Ed*, and its ESPHome node name.
- The host checks the version and format, looks the name up in the
  inventory, and answers with a challenge: its own 32 fresh bytes (*Nh*),
  *Eh*, and HMAC-SHA256(*k*, `"chorus-bridge v4 host proof"` ‖ hello payload ‖
  *Nh* ‖ *Eh*). An unknown name gets nothing.
- The device checks the host's proof in constant time before it proves
  anything, starts a microphone, or sends a frame. A host that fails it is
  hung up on, and redialled only after a backoff that doubles to 60 s. Both
  proofs cover both public keys, so a peer on the path that swaps either one
  fails here.
- A public key of low order, whose *s* is all zeros, is refused: by the host
  before it sends its challenge, by the device before it sends its proof.
- The device answers with HMAC-SHA256(*k*, `"chorus-bridge v4 device proof"` ‖
  hello payload ‖ *Nh* ‖ *Eh*). The host verifies it in constant time and
  requires the connection's source address to be that satellite's. Name, key
  and address must all hold (§13).
- Every frame after the answer, both ways, is sealed with AES-256-GCM under a
  key per direction, HKDF-SHA256(*s* ‖ *k*, salt *Nd* ‖ *Nh*, info
  `"chorus-bridge v4 device to host"` or `"… host to device"`). A recorded
  session therefore stays sealed if the PSK leaks later. The nonce is a
  counter per direction from zero that never travels, so a frame that is
  altered, replayed, reordered, dropped, or reflected back does not open, and
  the side that reads it drops the link. Type and flags are sealed with the
  payload; a frame costs 16 bytes more than a version 3 one.
- Nothing else is read or sent before the device's proof verifies. Audio
  first, an unknown name, a low-order key, a bad proof, a device that hangs
  up on the host's proof, the wrong address, a version other than 4, or no
  answer within the handshake bound is a disconnect, logged with which.
- One live link per satellite. A verified connection for a satellite that
  already has one closes the old link and waits for its supervisor to end
  before starting its own; a connection that fails to verify leaves the live
  link alone.
- The device accepts no frame but a challenge until the host has proved the
  key. Its connection state drives `on_connected` and `on_disconnected`, and
  the ring shows Unreachable while it cannot reach chorusd (ADR-0074).

### 3.3 Device facts worth remembering

Both devices use the **same XU316** doing full AEC/NS/AGC — they are acoustic
peers, with no degraded tier. Neither does wake word on the XMOS. Beamforming
is claimed by neither vendor.

Playback position is available and DAC-accurate (§3.2.1), so the barge-in
truncation point is effectively exact. No open device-layer questions remain;
§3.3.2 records what the hardware actually measured.

### 3.3.1 Verified against hardware

`cmd/probe` against the living-room Satellite1 (ESPHome 2026.7.2,
`FutureProofHomes.Satellite1` @ `dev`, API 1.14) confirms the transport works
and no Home Assistant is involved. `voice_assistant_feature_flags = 61` =
`VOICE_ASSISTANT | API_AUDIO | TIMERS | ANNOUNCE | START_CONVERSATION`.

**Not set: `FEATURE_SPEAKER` (2) and `FEATURE_MULTI_CHANNEL_AUDIO` (64).** So
this build wires a *single* mic channel into stock `voice_assistant` and does
not advertise streamed-speaker TTS — the dual-channel claim in §3.2 describes
the Voice PE config and the hardware capability, not this device's current stock
wiring. Irrelevant to us (our component chooses its own sources), but the two
sources must be declared by `chorus_bridge` rather than assumed present.

Entities that matter, all usable from the orchestrator over the native API:

| Entity | Use |
|---|---|
| `light` **LED Ring** | Shows the session listening, working or speaking, and goes dark when it closes. Shows the device muted over everything, and an error when no session is open and something the satellite needs last failed: the ear, the voice, the model or the journal, until it next answers. Stock firmware animates it from `voice_assistant`'s state, so `esphome/satellite1.yaml` (24 LEDs) and `esphome/voice-pe.yaml` (12) declare it as a plain light with a `Listening`, `Thinking` and `Speaking` effect, plus `Muted` and `Error`, and chorusd sets one per state over the native API. A ring without the first three is left to its firmware (ADR-0056); one without `Muted` or `Error` goes dark in that state (ADR-0069) |
| `select` **Wake word sensitivity** (3 levels) | **Immediate relief for cough false-accepts, no retraining** (§9.3) |
| `switch` **Capture wake-word audio** | Stock corpus capture hook — inspect before building our own |
| `binary_sensor` **Room Presence** + mmWave radar suite | Journalled to the device's log as `presence_changed` and drawn on Browse (ADR-0050). Presence-gated sessions and occupancy-aware routing are not built. The Voice PE has no radar, so it lists none and its room journals no presence |
| `binary_sensor` **Mute Switch** (HW) | The HAT's slide switch, on the XMOS's button port, which the stock firmware mirrors as a `switch` Mute Microphones; on the Voice PE, the side switch on GPIO3. Hardware mute is a user-facing privacy control; respect it. `esphome/satellite1.yaml` and `esphome/voice-pe.yaml` read the switch themselves and `chorus_bridge` reports it as the `mute` frame's hardware bit; the software bit is the `Microphone`'s own mute. The host drops the room's audio while either is set (ADR-0067) |
| `number` **Speaker Volume** | 0 to 100, kept across reboots, 0 mutes the amplifier. Both stock firmwares keep their volume on a media player, so `esphome/satellite1.yaml` and `esphome/voice-pe.yaml` declare this number instead; the Voice PE's dial turns it, 5 a detent. chorusd finds it by name, reads its level and sets it over the native API when `set_volume` runs; a satellite without it answers `not_implemented` (ADR-0067) |
| `button` **XMOS Flash Embedded FW** | XMOS firmware is flashable from the API |

`esphome/voice-pe.yaml` gives the Voice PE the same LED Ring, Mute Switch,
Speaker Volume and wake-word sensitivity, and chorusd finds them by the same
names and effects, so it needs nothing per board. That config has been
validated with `esphome config`, not run on a device; nothing in this section
has been measured on a Voice PE (ADR-0073).

### 3.3.2 Full duplex, measured on the device

The Phase 1 blocker (§14) is closed. `internal/bridge/hardware_test.go` runs
against the living-room Satellite1 with no Home Assistant involved: the device
dials the orchestrator's audio port, declares `{version 2, 16 kHz, 16-bit, 2 mic
channels}`, and the three claims below are asserted on every run of
`task test:hardware`.

| Claim | Measured |
|---|---|
| Capture continues during playback (§3.3.1) | 448-628 mic frames across a 6 s utterance; longest uplink gap 137-393 ms against an idle baseline of 94-296 ms on the same link. Holds whenever the radio has the airtime; see the congestion note below |
| Playback position is the DAC's own (§3.2.1) | Final position within 40 ms of a 3 s utterance; frame advance tracks `esp_timer` to under 1 ms when playback does not stall |
| Barge-in discards the buffer (§3.2) | Playback stops 20-60 ms in, and capture survives it |

Hardware notes that cost real time to find:

- The XU316 is the **I2S master**, so there is no audio at all -- in either
  direction -- until it answers over SPI. A 20 V USB-PD contract is what selects
  the amplifier's full power mode. Both complete before a log client can attach,
  so the config reports them on an interval instead.
- Uplink and downlink **share one radio**. Writing TTS faster than real time
  starves the uplink badly enough to look exactly like a half-duplex failure.
  Pace the downlink.
- The i2s speaker holds its stream open across an underrun and emits silence
  that the DAC genuinely plays and therefore counts. The reported position can
  exceed the audio supplied; it cannot fall short of what was emitted.
- The uplink's **idle** jitter is set by the radio, not by this component: with
  the speaker switched off the device still goes 120-210 ms between frames, and
  on a bad evening over a second. A duplex claim measured against a fixed gap
  budget therefore tests the 2.4 GHz band, not the component. The test takes a
  baseline on the same connection seconds earlier and asserts the excess over
  it, plus a hard ceiling no baseline can excuse.
- **Airtime, not bandwidth, is the limit.** 512 kbps is nothing for WiFi, but a
  32 ms chunk is a short 802.11 frame and a short frame costs nearly a full
  frame's airtime, so flushing per chunk spends the band on headers. Batching
  the uplink to an MSS halved the device's EAGAIN rate for the same bytes.
- **When the band is congested the test fails and the firmware is not at
  fault.** The measurement that settles this uses no project code: with the
  device idle and no audio at all, plain ICMP at ~28 KB/s loses 3% of packets
  and peaks at 717 ms RTT; under duplex audio, 8.3% loss and 1357 ms. Signal is
  a strong -49 dBm, so it is ambient congestion, not range. Through all of it
  the device held capture at a steady 32256 B/s with `loop()` at 17-23 ms: it
  buffers its one second of TX and then correctly drops, and the host -- which
  cannot see that drop counter -- reads it as a capture stall. No firmware
  change recovers capacity the air does not have. Sustained duplex on this
  satellite wants 5 GHz or a quieter channel, and a red `TestHardwareFullDuplex`
  should be checked against the ICMP control before it is believed.
- ESPHome's `RingBuffer::write()` **overwrites the oldest data rather than
  blocking**, so a full mic ring discards silently and never backs pressure up
  into the capture task. A starved channel is therefore invisible from the host
  unless something counts it.
- ESP-IDF builds with exceptions off, so an allocation failure is an `abort()`,
  not an error path. Every buffer in the audio path is fixed-capacity and
  reserved once.

## 4. Session model: actor/supervisor

There is no pipeline and no stage enum. A session is a **supervisor** owning
concurrent child activities: `Listening`, `Thinking`, `Speaking`, and one child
per in-flight tool call. Children start, complete, and cancel independently.
The "lifecycle" is simply which children are alive. Go goroutines +
`context.Context` cancellation map directly onto this.

This is the load-bearing choice. In Assist, interruption is an exceptional path;
here it is ordinary cancellation, which the model handles everywhere. Every hard
requirement becomes structural rather than a special case:

| Requirement | Mechanism |
|---|---|
| Speak while tooling | Two live children |
| Barge-in w/ context | Cancel `Speaking`, apply per-tool interrupt policy, session persists |
| Multi-turn interleave | Children churn under a persistent supervisor |
| Proactive speech | A session with no wake word |

### 4.1 Model output is a stream of concurrent actions

Not a sequence of messages. The model opens and closes actions at will: speak,
don't speak, speak twice, call three tools in parallel, speak again after
results. The orchestrator **never imposes an order**.

Two consequences:
- Tool calls are **parsed incrementally and dispatched the instant their JSON
  closes** — not at end-of-message.
- Speech deltas stream to TTS as emitted.

`speak` is a declared tool so it is schema-addressable, but holds no privileged
position. Inline `content` is not speech: beside a tool call it is never
spoken, and with no thinking field it is taken for reasoning. A turn that
called nothing, ended on its own, and kept its reasoning in the thinking field
has its content spoken once it ends, as an implicit `speak` (ADR-0046).

### 4.2 Speech channel

Default **queue** — natural for "one sec" → tool result → "found three". A
declared field allows `queue | preempt | interject` per call; `preempt` is for
when a tool result invalidates what was about to be said. `interject` is for
something that cannot wait for the end of the sentence: it pauses what is
playing where the DAC stopped, says the interjection, then resumes the rest
under the same call, queue intact. A satellite plays one voice, so the pause
is a stop, not a duck under the interjection; it is journalled as
`interjected`, never as a barge-in (ADR-0056).

On barge-in, the unspoken remainder is **discarded but recorded** — generated
but never spoken is a distinct event type from spoken. The model sees only what
was actually heard, and may repeat itself in its own words.

### 4.3 Barge-in

Detection gate, stacked, ~300 ms from user speech to TTS stop:

1. VAD energy
2. **Echo**: the candidate's newest audio is the device's own voice when it
   matches what the DAC was playing under it (§3.2.1) — the host keeps what
   it sent, on the count PLAYED reports, and compares the shape of the mic's
   spectrum against it over the lags the uplink can trail by. Before the
   speaker stage and whatever it says: the device's voice is no reason to stop
   it, and with no speaker ID it is the only stage that can tell. A refused
   echo costs no embedding. Audio with too little voice to compare, or a
   reference silent at every lag, passes (ADR-0096).
3. **Speaker-ID match** against the session's current speaker or a known
   household member — the high-value filter. The TV and the wrong housemate both
   fail it. Much stronger than energy thresholds. A candidate the embedder
   failed on is refused too, as `speaker_id_failed`: only a recognised voice
   stops speech, and an outage is kept apart from the television in the
   tuning corpus (ADR-0097).
4. STT partial length gate (not "uh", not a single word)

No semantic "was this addressed to me" check: it costs latency exactly where
latency is viscerally felt, and a wrong stop is cheap (keep talking) while a
slow stop feels broken. **Every rejected candidate barge-in is logged** as the
tuning corpus for this gate, and every candidate the echo stage compared,
admitted or not, carries its score and lag, which is what tunes the
satellite's `echo_threshold`. Each candidate names its utterance's one blob and
how many frames of it were judged, so a long overlap keeps its audio once
(ADR-0060).

The speaker stage's refusal stands once the voice pauses: an utterance that
talked over speech, never stopped it, and is a voice the household does not
know is not answered as a turn (ADR-0049). A household voice the other stages
refused is still heard.

**Hot phrases** skip the partial-length stage and only it. A partial that is wholly
one of a small closed set (*stop*, *never mind*, *say that again* and a few
variants of each) stops speech on one word, because "stop" is what a
household says most to a voice assistant. Energy, the echo and the speaker
stages still apply, so the television saying "stop" stops nothing. A stop or never
mind that stopped something is not answered; never mind also reaches a turn
working in silence, and a stop silences a timer going off. A repeat says
again what was last heard, without the model. The set is closed because
each entry bypasses a filter, and English-only because the default ear is (§10,
ADR-0064).

### 4.4 Interrupted-turn semantics

Record the **truth**. What was actually spoken is kept, marked interrupted, with
the truncation point. Tool results that arrived are kept. The model sees "I said
this much, then was cut off" and can reason about it.

Per-tool `on_interrupt` policy, declared in the registry:

- `cancel` (default)
- `detach` — finish, keep the result (wasted but harmless work)
- `uninterruptible` — must complete (side effects already committed)

A tool's timeout (§7) is not an interruption either. The model is told
`timed_out` and the turn goes on, but `detach` and `uninterruptible` work runs
on past it, and its eventual result is kept as `detached` (ADR-0060).

This is unretrofittable. It is why truncation fidelity matters — and why we take
the position from `add_audio_output_callback` (§3.2.1) rather than estimating.

### 4.5 Session lifecycle

Wake word opens a session; continuous streaming while open; **server-side
semantic endpointing** (Smart Turn v3.2 on the audio, not the big LLM, ADR-0036) so
"turn off the... uh... kitchen lights" works. Its probability is read in bands:
a turn it is sure of ends at the pause, one it doubts is held, and one it is
unsure of ends where silence alone would end it. Every utterance records the
rule that ended it and the probability, so the bands are tuned from the
household's own voices (ADR-0094).

Close is **model-decided** via an `end_session` tool, with a ~20 s silence
backstop. The model knows when a task is done better than a timer does. The
backstop waits while the model thinks, so the model has a deadline of its
own: an ask that emits nothing for 90 s is given up on (§7, ADR-0051).

**The conversation belongs to the person; the audio stream belongs to the
device.** Keeping those separate makes device migration fall out for free: wake
word on a different satellite within ~2 minutes for the same identified person
resumes the same logical conversation.

**One wake heard by two satellites is answered by one.** A wake frame is held
for a short window, 200 ms by default (`CHORUS_WAKE_ARBITRATION_MS`), while
the house's other linked satellites may report the same wake. The one whose
processed channel was loudest over the 600 ms before its frame answers; a tie
goes to the earlier arrival on the host's clock, then to the inventory's
order, and a push-to-talk press beats any spoken wake. The others open
nothing, light nothing, and take what follows as nobody's turn; each records
`wake_rejected` with reason `arbitrated`, naming the winner. With no other
satellite linked, or every one already in, nothing waits. The audio held
through the window is replayed to the winner, so the window is spent while
the person is still talking rather than on §11's budget. Arbitration covers
only simultaneous wakes: a wake after the window opens a contest of its own,
and a deliberate re-wake elsewhere is the migration above. A satellite with a
session already live still enters, so a fainter one down the hall cannot take
the conversation from it (ADR-0072).

## 5. Identity, context, memory

Explicit enrollment (guided phrases). Per-utterance speaker embedding
(ECAPA-TDNN / TitaNet-L, cosine against enrolled centroids). The session holds a
current speaker, but a confident mismatch **flips attribution within the same
conversation** — a second person chiming in is a real case Assist cannot handle.

Unknown or low-confidence speaker → guest context, person-scoped tools gated, no
interrogation, but cannot open a spoken wake session. A spoken wake opens only
when it is attributed to an enrolled household voice; with nobody enrolled, or
no working speaker resolver, chorusd fails closed because it cannot distinguish
the television from a person. Inside an already-open session, a guest who
chimes in is a guest for that turn, told none of the other person's memories,
while a voice nothing could judge stays with the current speaker (ADR-0049).
An embedder that fails is recorded as `failed`, never as a stranger
(ADR-0097).
Guest context is reached only in a session already open, one a household member
woke or an announcement that asked for an answer. The embedding is stored on
every trace record regardless, which gives implicit clustering for free later.

Memory: explicit `remember` / `forget` tools plus a summary of each
conversation for the people in it (ADR-0043). Past what a turn is told, both
are chosen by relevance and recency, or newest first without an embedding
model (ADR-0044). Person context is global across satellites;
the satellite contributes **location as turn metadata, not identity**: the
inventory's room, recorded when a session opens there and told to every turn
from the log (ADR-0050).
Cross-person visibility off by default, with an explicit per-memory `shareable`
flag — this is a household, not a tenancy, but surprising disclosure is how a
voice assistant loses trust. Everything kept about a person, facts and
summaries alike, can be read and forgotten from the review UI (ADR-0070).

## 6. Tool registry

Native Go registry; MCP is one provider among several; HA is an adapter
exposing entities, areas, and scripts. The registry owns what MCP has no
vocabulary for:

- `on_interrupt` policy (§4.4)
- `timeout` (default ~10 s)
- person scoping
- `requires_confirmation` (optional, per-tool, default off)
- latency hints

MCP-sourced tools get conservative default policies (ADR-0075). The servers
`CHORUS_MCP_CONFIG` names, over stdio or Streamable HTTP, offer each tool
as `mcp_<server>_<tool>`, `person`-scoped with an unidentified voice
denied, every call held for the asker's yes, `cancel` on a barge-in, and a
5 s timeout. A household relaxes any of these per server or per tool, and
can leave a tool unoffered. A provided tool is held to the same rules as a
declared one, checked when it is listed rather than at build time.

Person scoping is one of three. A `household` tool runs for anyone, a guest
included. A `person` tool needs an identified speaker, unless it declares that
a guest falls back to guest context. A `guest` tool always runs in guest
context: its executor is told nobody's identity, whoever spoke, so it can reach
no one's memories (§5, ADR-0060).

The model is offered every tool the daemon has an executor for, whether or not
that executor's backend is configured: Home Assistant switched off answers
`not_implemented`, which the model reasons about (§7). So the offered schema,
and the tool-schema version every event records, belong to the build, and a
re-run offers what the build offers, which is what the turn was offered
unless the build has changed since. A tool declared `deferred`, as
`media_search` is while media is deferred (§14), keeps its policy and its docs
but is not offered (ADR-0060). The MCP servers' tools are the exception: they
belong to the house, so they join the offer, and the version, as the servers
list them when `chorusd` starts, and stay fixed until it restarts. A server
that drops is reconnected and its tools answer `mcp_unavailable` meanwhile;
a tool it stops listing answers `mcp_tool_gone` (ADR-0075).

A tool can act through a satellite rather than a backend. `set_volume` sets
a satellite's speaker over its native API connection (§3.3.1), in the room
named or the one the turn came from (§5). The level it reports is the one
the device reports back, marked `unconfirmed` when none comes within a
second. A satellite whose firmware lists no volume control answers
`not_implemented`, and one whose connection is down `not_connected`
(ADR-0067).

**Confirmation** is orchestrator-enforced but model-authored. The orchestrator
blocks execution and returns a synthetic `confirmation_required` tool result
carrying a nonce; the model phrases the confirmation itself and re-calls with
the nonce after an affirmative. Hard safety guarantee without the orchestrator
owning the dialogue — and the nonce makes "did the user actually say yes"
auditable in the trace. The yes is the asker's: the nonce is redeemed only by
the person whose turn made the held call, when speaker identification named
them, and never by a voice that matched nobody in the household (§5). A
household nothing identifies is answered by the next thing said, as before
(ADR-0063).

## 7. Failure semantics

**Everything recoverable becomes a tool result the model reasons about; only
infrastructure failures get canned speech.** A tool timeout returns
`{error: "timed_out"}` and the model says something true and useful, instead of
the orchestrator barging in with "sorry, something went wrong."

Canned fallback reserved for LLM-unavailable, TTS-unavailable and
STT-unavailable, where there is no model left to reason with, or nothing it
could reason over. Failures are first-class event types — they are training
data too. What a household would otherwise meet as silence is said, or, where
nothing can be said, shown on the LED ring (§3.3.1, ADR-0069).

- A model that is unreachable, breaks mid-stream, or emits nothing past its
  deadline is `model_failed`. The turn ends with one canned line.
- A voice that fails mid-utterance is `speech_failed`, and the truncation or
  discard it caused names it, never `barge_in`: a dead synthesiser is not a
  preference (§9.1).
- An ear that fails on what someone said in a session is answered with one
  canned line, marked `unheard`. No event holds words nothing decoded, so
  nothing is transcribed. A wake whose first words it fails on is rejected
  as `transcription_failed` (§9.3), and when the voice would have passed
  stage three the canned line follows the rejection in the device's log,
  which names it; the television's wake stays silent.
- A speaker embedder that fails is shown on the ring until it next answers,
  never said: it cannot tell the household from the television, so a spoken
  wake it failed on is rejected as `speaker_id_failed` in silence (§9.3,
  ADR-0097).
- A journal that refuses a write is shown, never said. The log is the runtime
  (ADR-0007), so a word it cannot record is not spoken: a wake whose session
  the journal will not open, or whose rejection it will not take, gets the
  ring's error state and no speech.
- An utterance whose audio the blob store fails to write is still heard; its
  clip is recorded as `audio_dropped` with `write_failed` straight after. A
  disk below its floor writes nothing and fails nothing (ADR-0065).
- Every canned line is rendered at startup, and until that succeeds is said
  from audio built into the binary, rendered from Kokoro by `task
  canned:voice`, so it exists before the synthesiser has ever answered.
- A canned line is a speak call marked `canned`. The model is not shown it as
  its own words, and no pair is cut from it (ADR-0051).

Sessions do not survive an orchestrator restart in phase 1. The log replays for
debugging; resuming live audio across a restart is a lot of machinery for a case
solved by not restarting mid-sentence.

## 8. Event journal

The orchestrator is a **pure reducer over an append-only event log.** Tracing is
not instrumentation; it *is* the runtime. Get this invariant right and the
review UI, replay harness, and dataset export become readers of existing data
rather than new plumbing.

Storage: Postgres `JSONB`, partitioned by conversation, plus a filesystem
directory for audio blobs (mic PCM, both channels, the second named by
`second_audio_ref`; synthesized TTS). 16 kHz 16-bit mono is ~115 MB an hour of
continuous capture. Everything is kept unless `devices.yaml` sets a retention:
an audio and a journal horizon in days, house-wide and per satellite
(ADR-0065). A clip past its satellite's audio horizon is removed, and the log
that named it records `audio_dropped`; a conversation is deleted whole once
its journal horizon has passed since its last activity, and the device and
house logs lose the events older than theirs. What a reviewer curated is kept
unless the house opts in to pruning it, and a live session's conversation is
never touched. While the blob disk is below its free-space floor audio is not
written, the log says so, and the turn goes on.

A reviewer may also delete a conversation, or drop one event's audio, from the
review UI, by the path retention takes: never a live session's conversation,
one its person may still resume, or a satellite's or the house's own log. A
dropped clip is recorded as `audio_dropped` with reason `redacted`, and each
deletion leaves a line beside the journal naming what went and how much, never
what it held (ADR-0070).

Every event carries a monotonic sequence number, wall clock, the model /
prompt / tool-schema versions in effect, and the STT and TTS identities
(ADR-0032). Deterministic replay requires recording every nondeterministic
input: model completions (not just requests), tool results, and barge-in
timing to the millisecond relative to TTS position.

`replay(conversation_id, overrides)` is a tested function from week one. It is
what keeps the reducer honest — if it ever breaks, the system has silently
become instrumented-code-with-logs again.

Operational metrics are read off the log as it is written: an observer on
the journal's append path counts what each event records, and `chorusd`
serves the counts in Prometheus's text format beside its health and
readiness (§13, ADR-0068). They are derived, never a second record, and a
write that fails is counted by the kind it would have been. OpenTelemetry
export is deferred (§14); the annotation layer stays in the journal either
way.

## 9. Learning loop

Primary target: **the LLM's tool-calling and speech-interleaving decisions.**
STT adaptation on household voices and vocabulary is a free byproduct. Wake-word
retraining is built and has trained no model yet (§14, ADR-0078). What a reviewer accepts exports as a versioned dataset: DPO
pairs and SFT exemplars, each row with its context, provenance and a split by
conversation, optionally zipped with its audio (ADR-0071).

### 9.1 Barge-in as a free DPO pair

The automatic rule that matters: **an interruption generates a preference pair**
— *rejected* = what it was saying, *chosen* = what it said after the correction.
Zero effort required. This is the highest-value artifact in the project, no one
else produces it, and it only works because the truncation point is captured
exactly (§4.4).

Other implicit signals: a completed turn with no correction is a weak positive;
a repeated request is a failure.

### 9.2 Annotation vocabulary

Explicit labels for the ambiguous cases: *transcript wrong*, *misunderstood
intent*, *wrong tool / wrong args*, *should have spoken and didn't*, *spoke when
it shouldn't*, *too slow*, *wrong person attributed*, *good — exemplar*. Plus
free-text "what it should have done" and edit-and-replay (change prompt or tool
schema, re-run the trace, diff the outcome). Every re-run is kept beside the
journal, never in it, with what it ran under, so any of its takes can be
promoted to a pair later (ADR-0059).

Review UI: Go + htmx over Postgres, in the monorepo. **Inline per-turn audio
playback is non-negotiable** — a voice assistant cannot be judged from
transcripts.

### 9.3 Two-stage wake confirmation (phase 1)

Observed failure in practice: the existing "Hey Eddie" model activates on
coughs. Recall is fine; precision is not. So the priority is suppressing false
accepts, not recovering false rejects.

`micro_wake_word` activation on-device is **stage one, and is not user-visible.**
The component is meant to send a pre-roll with the activation, and the
orchestrator to run stage two on it before any LED or chime. The wire carries
no pre-roll yet (`bridge.TypeWake` is the word alone), so check 1 below is not
run, and checks 2 and 3 run on the first utterance after the wake, which is
when the session opens (ADR-0030):

1. re-score the pre-roll against the wake model at a higher threshold
2. confirm the segment contains speech at all
3. confirm the speaker embedding matches a household member

A cough fails all three. A guest fails the third: a voice below the accept
threshold, too close to two people, or otherwise unmatched is rejected as
`unknown_speaker`. With nobody enrolled, or no speaker ID configured, stage
three cannot verify a household member and rejects the spoken wake the same
way. A configured speaker ID that fails on the utterance rejects it too, as
`speaker_id_failed`: nothing judged the voice, so it is neither held as a
guest's nor a hard negative (ADR-0097).
Guests are heard only in a session already open (§5). Only on confirmation
does the session become perceptible to the user. Rejections
are logged with audio and auto-labeled as hard negatives, so the retraining
corpus fills itself with precisely the negatives the model lacks — no manual
labeling, no retraining needed to get immediate relief. A first utterance STT
could not decode at all is rejected too, as `transcription_failed`: nothing
confirmed the wake, but nothing judged the audio either, so it is no hard
negative. It is the one rejection the user may perceive: a voice stage three
would have let in is told the ear failed (§7), rather than left in silence.

The one exception is a wake that failed only the speaker check: that is likely
the wake word from a guest, so it is held until a reviewer confirms it
(ADR-0058).

Two rejections are no negative at all, and never enter the corpus. A wake
another satellite heard louder is arbitrated before stage two (§4.5): the
loser's `arbitrated` rejection is the wake word said farther away. And a
push-to-talk press, whose frame names the wake `button`, never fired the wake
model, so whatever becomes of it says nothing about the model. Each rejection,
and each session a wake opens, records the name its wake frame carried as
`wake_word` (ADR-0072).

Dual-channel capture means the corpus carries the fully processed stream and
the XMOS's lighter-processed second output, so retraining can target either.
Neither is the bare mic on stock firmware (§3.2); a truly raw stream needs the
Voice PE's stage switch or a change to the Satellite1's XMOS firmware.

### 9.4 False rejects (phase 2, lower priority)

False rejects are **uncapturable** under wake-word-gated streaming — no session,
no audio. If they become a problem, the fix is a **near-miss buffer**: a rolling
3 s pre-roll uploaded when wake confidence lands below the activation threshold
but above noise. Audio leaves the device only on a near-activation, so the
privacy story holds. Not needed today.

## 10. Model stack

Local-first, cloud escape hatch per provider. Each behind an HTTP contract,
or Wyoming's over TCP (ADR-0081),
every provider declaring capabilities (streaming? tool calls? interruption?)
so a build-time check can reject a pipeline that cannot work. Those
declarations and the check are not built: nothing in `schema/` describes a
provider.

| Role | Choice | Rationale |
|---|---|---|
| STT | Parakeet TDT 0.6B v2 | Streaming, far faster than Whisper large at better accuracy. English-only; swap to faster-whisper if multilingual is needed. |
| LLM | Qwen3 32B (or 30B-A3B) on **vLLM** | vLLM streams tool-call parsing, a hard requirement of §4.1. Ollama/llama.cpp are weaker at exactly this seam. |
| TTS | Kokoro-82M | Sentence-level streaming, sub-200 ms first chunk. Orpheus if more expression is wanted. Piper as degraded fallback. |
| Speaker ID | ECAPA-TDNN / TitaNet-L | Embeddings + cosine. |
| Endpointing | Smart Turn v3.2 | Purpose-built; avoids prompting the big model. |

`chorusd` runs the LLM on Ollama by default, or on vLLM with
`CHORUS_LLM_PROVIDER=vllm`, whose server needs `--enable-auto-tool-choice
--tool-call-parser hermes` for Qwen3. Either engine sends the same prompt
and tools. On vLLM a call dispatches when its arguments close, and a speak
call's words stream as they arrive. Qwen3's thinking is off there unless
asked for, for §11's budget. Embeddings stay on Ollama (ADR-0077). The vLLM
engine has not yet run against a real server.

The voice and the ear default to Kokoro and Parakeet on speaches.
`CHORUS_TTS_PROVIDER` and `CHORUS_STT_PROVIDER` choose instead any server
with OpenAI's audio API (`internal/provider/openai`) or Home Assistant's
Wyoming (`internal/provider/wyoming`), so a household's piper and whisper
serve as they are; their audio is resampled to the device's rate by
`internal/provider/pcm`. Every model and voice is named in the environment
and recorded on every event (§8). Neither has yet run against a real server
(ADR-0081).

Biggest standing risk: **local tool-calling quality.** The provider abstraction
exists so a frontier model can be dropped into the LLM seam without touching
the orchestrator.

A degraded local profile for "lights off" when everything else is down is
not built, and local-model limits do not get to dictate the architecture.

## 11. Latency

Target **700 ms wake-word-to-first-audio.** Assist on local models is 1.5–3 s
and feels bad; realtime S2S is ~300 ms and feels alive.

Phase 1 is the straightforward streaming cascade with honest measurements —
expect TTS first-chunk and LLM prefill to dominate, not STT.

**Speculative execution is built, off by default — ADR-0095.** With
`CHORUS_SPECULATE=on`, the endpointer's pause (200 ms of quiet, short of the
end) is decoded, attributed and asked of the model before the utterance has
ended. The answer is buffered: nothing is dispatched and nothing is played.
When the turn is confirmed it is kept only if the log would ask the model the
same thing (the same words and speaker, the same dialogue, memories and room,
the same minute); its buffered stream then plays as if just asked, and its
`model_completed` records `asked_early_ms`. Otherwise it is thrown away as one
`speculation_discarded`: the person went on speaking, the turn changed, the
utterance was never a turn, or the session closed. Only a turn in a session at
rest is guessed at; the wake's own utterance is not, and speech is not rendered
ahead.

**Speculative events are a recorded distinction from day one.** A
`speculation_discarded` is journalled with the flag, and `replay()` skips it:
a log with one replays to the state it would have without it.

## 12. Turn engine abstraction

The turn engine is an interface emitting a stream of actions (`SpeechDelta`,
`ToolCall`, `TurnEnd`); tool results are fed back to it, not emitted by it. Phase 1 implements it as the cascade. A
realtime speech-to-speech provider (OpenAI Realtime, Gemini Live) later becomes
an adapter mapping their event stream onto ours.

This is why `speak` is a tool rather than a model-template quirk (§4.1) — it is
the only framing that survives the S2S adapter.

Do not build the S2S path until the cascade works end to end.

## 13. Deployment

- **Monorepo.** The device component, orchestrator, and sidecars share a
  protocol contract that must version together. Split repos mean a coordinated
  release dance for a one-person project.
- **docker-compose** runs `chorusd`, the review UI, Postgres and the four model
  sidecars, with a one-shot enrollment helper,
  on CPU images; a household may run the sidecars on a GPU box instead, and
  `chorusd` reaches them by URL. Ollama is not in it.
- **Satellite inventory:** static YAML for each satellite's address, Noise
  PSK, room assignment, and capability profile, and the house's and each
  satellite's retention horizons (§8). Secrets stay out of a database
  that would need separate backup. On the audio link a satellite is its name,
  its PSK and its address together (§3.2.2): the device proves its ESPHome
  node name with a MAC under the PSK, and must dial from the address the
  inventory gives that name. So `name` is the node name, MAC suffix included,
  and the address is an IP literal: a connection from an address the
  inventory does not name is refused before a frame is read, and `chorusd`
  refuses a hostname, or a host two satellites share, at startup. mDNS
  discovery (`_esphomelib._tcp`) is not built. The profile, `satellite1` or
  `voice-pe`, is informational: chorusd reads what a satellite has (its ring,
  its volume, a presence sensor) from the entities it lists over the native
  API, and both boards stream the same two channels, so nothing in the host
  branches on it (ADR-0073).
- **Addressing is asymmetric.** Discovery, were it built, would run one way
  only: the orchestrator could resolve satellites, but the orchestrator's own
  address must be a literal IP in the device YAML. ESPHome's `set_sockaddr`
  does not resolve hostnames, so the device cannot dial an mDNS name. The
  orchestrator needs a static lease; `chorus_bridge` enforces this at
  `esphome config` time via `cv.ipaddress`.
- **Watching it run.** `chorusd` serves `/healthz`, `/readyz` and
  `/metrics` on `CHORUS_OPS_LISTEN`, loopback by default, since nothing
  there asks who is asking (ADR-0068). Readiness fails while a dependency
  `chorusd` requires at startup is down (Postgres, STT, TTS, the LLM), and
  only degrades for the optional sidecars, Home Assistant, and each
  inventory satellite whose audio link or native API is not connected.
  Dependencies are probed on an interval, never on a request. A sidecar that
  answers but serves another model than its client is built for, or a voice
  server without the configured voice, is down (ADR-0097).
- Wake word: existing trained "Hey Eddie" model, gated by two-stage
  confirmation (§9.3). Swappable without touching anything else.
- Persona lives in the system prompt as a **versioned artifact** — because
  prompt versions are recorded per event, persona changes are A/B-testable
  against replayed traces. Every version's text, and every native tool
  schema's, is kept in the repository by the hash an event records, so the
  dataset export ships the prompt and tools each turn ran under (ADR-0071).
  An MCP server's tools are the exception: their schemas come from the
  server at startup and are not kept, so a turn that offered them exports
  as `context_missing` for its tools, and replay runs it without them
  (ADR-0075).

## 14. Phase plan

Struck through is merged on main and covered at the tiers that can see it.
Phase 1's pieces run together in `chorusd` against the in-process satellite
(`bridgetest`), and run a Satellite1 in the house on protocol 4 firmware
under Docker Compose. Running it there is what found the television opening
sessions, so a session now opens only for an enrolled household voice (§5).

### Phase 1

1. ~~**Device bridge spike — throw-away-able.** Go orchestrator dials a satellite
   over the native API (Noise handshake); `chorus_bridge` streams mic audio out
   over TCP and plays audio back. **Prove full-duplex on real hardware.** This
   is the riskiest unknown and every downstream decision depends on it, so it is
   validated before anything is built on it.~~ **Done — §3.3.2.**
2. ~~**Event journal + replay.** Postgres, `replay()`, before any intelligence
   exists.~~ **Done — ADR-0021, ADR-0022.**
3. ~~**Cascade.** Parakeet → Qwen3/vLLM with streaming tool-call dispatch →
   Kokoro, under the supervisor/actor model.~~ **Done — ADR-0023, ADR-0024,
   ADR-0037.** The LLM seam runs Qwen3 on Ollama by default, or on §10's
   vLLM with `CHORUS_LLM_PROVIDER=vllm` (ADR-0077); the vLLM engine is
   tested against recorded-shape streams and has not yet run against a real
   server. The voice and the ear may instead run on an OpenAI-compatible or
   Wyoming server (ADR-0081), likewise not yet run against a real one.
4. ~~**Speaker fingerprinting**, pulled early — both barge-in quality (§4.3) and
   two-stage wake confirmation (§9.3) are gated on it.~~ **Done — ADR-0025,
   ADR-0029.** Stage two of §9.3 checks the first utterance for speech and a
   known speaker; re-scoring the wake word is not built, as the wire carries
   no pre-roll.
5. ~~**The two signature features.** Speak-while-tooling with one real tool;
   barge-in with context retention.~~ **Done — ADR-0027, ADR-0033, ADR-0037,
   ADR-0039.** The real tools are Home Assistant's.
6. ~~**DPO harvester** from barge-in corrections (§9.1).~~ **Done — ADR-0026.**

### Phase 2

- ~~Review UI~~ **Done — ADR-0034, ADR-0070, ADR-0071, [the review UI
  guide](reviewui/README.md).** A reviewer can delete a conversation, drop a
  clip and forget a memory, and the export is format 2, each row with the
  prompt and tools its turn ran under.
- ~~Memory~~ **Done — ADR-0040, ADR-0043, ADR-0044, ADR-0049.** Summaries are
  kept per conversation, not as one rolling summary per person.
- ~~MCP provider~~ **Done — ADR-0075.** Servers' tools are offered under
  conservative defaults a household relaxes per server or tool, fixed at
  startup. Resources, prompts and OAuth are not built, a re-run offers
  only the build's tools, and an MCP turn's export says `context_missing`
  for its tools.
- ~~Confirmation gates~~ **Done — ADR-0038, ADR-0041, ADR-0063.** Garage, gate
  and door covers are held by the class Home Assistant gives them, and a yes
  counts only from the person who asked, when they were identified, and
  never from a guest.
- ~~Timers~~ **Done — ADR-0045.** A timer belongs to the house, outlives the
  conversation and the process, and goes off where it was set.
- ~~Announcements with `start_conversation`~~ **Done — ADR-0045.**
- ~~Semantic endpointing~~ **Done — ADR-0036.**
- ~~Hot phrases~~ **Done — ADR-0064.** Stop, never mind and say that again
  skip the barge-in gate's word count, never its speaker check; English
  only.
- ~~Retention~~ **Done — ADR-0065.** Audio and journal horizons per
  satellite in `devices.yaml`; everything is kept until they are set.
- ~~Authenticate the audio link~~ **Done — ADR-0066.** Protocol 3: the
  device proves its node name with its PSK before any audio. Superseded on
  the wire by protocol 4 (ADR-0074).
- ~~Speaker volume and the hardware mute switch~~ **Done — ADR-0067.**
- ~~Health, readiness and metrics~~ **Done — ADR-0068.**
- ~~Say failures the household would meet as silence~~ **Done — ADR-0069.**
  What cannot be said is shown on the ring.
- ~~Wake arbitration across satellites~~ **Done — ADR-0072.** A wake several
  satellites hear within a 200 ms window is answered by the one whose
  processed channel was loudest before it; the others journal that they lost.
  A button press beats a spoken wake. `CHORUS_WAKE_ARBITRATION_MS=0` turns it
  off.
- ~~A Voice PE configuration~~ **Done — ADR-0073.** `esphome/voice-pe.yaml`
  gives the Voice PE the Satellite1's entities, under the same names; its dial
  turns the Speaker Volume and its centre button wakes it. Validated, not yet
  compiled or flashed.
- ~~Record the wake word on `session_opened`~~ **Done.** A session names
  the wake that opened it, as a rejection does; the review UI marks one a
  button press opened.
- ~~Keep `transcription_failed` rejections out of the wake corpus~~
  **Done.** A wake nothing could decode no longer ships as a hard negative
  (§9.3, ADR-0060); the review UI says why it has no verdict.
- ~~Drop a clip from a satellite's own log, such as a guest's rejected
  wake, from the review UI~~ **Done — ADR-0076.** A device log's clips drop
  behind the same second press as a conversation's, recorded as
  `redacted` in that log; a journal append whose seq another process took
  writes after it, so chorusd loses nothing to the review UI.
- ~~Show arbitration's margins in the review UI~~ **Done.** `/arbitration`
  counts each pair's contests from the journalled levels, with the median
  margin, how the margins fall and the closest calls, each linked to the
  lost wake and the conversation the winner opened.
- ~~Wake-word retraining from the harvested negative corpus~~ **Built —
  ADR-0078, [the guide](wake-retraining.md).** `task wake:corpus` lays the
  wake corpus export out for microWakeWord with each clip's journal row,
  `task wake:positives` speaks the wake word in Kokoro's voices,
  `task wake:train` trains in a pinned container, and `task wake:evaluate`
  ships a model only if it beats the deployed one on held-out clips. No
  model has been trained by it yet, and its container has not run in CI.
  The clips are the audio after the wake, since the wire carries no
  pre-roll (§9.3).
- Near-miss buffer if false rejects surface
- ~~Encrypt the audio link, and authenticate the host to the device~~
  **Done — ADR-0074.** Protocol 4: the host proves the satellite's key over
  both nonces and both sides' ephemeral X25519 keys before the device sends
  any audio, and every frame after the handshake is sealed with AES-256-GCM
  under a key per direction, forward-secret against a PSK that leaks later.
  Flashed onto a Satellite1, which completes the handshake with `chorusd`.
- Stop the uplink in firmware while the mute switch is on; today the host
  drops the frames (ADR-0067)
- ~~Confirm a volume level~~ **Done.** `set_volume` answers with the level
  the device reports back after the command; with no report within a second
  it gives the level sent and says it is unconfirmed (ADR-0067).
- ~~A ring effect for a satellite that cannot reach `chorusd`, driven by the
  bridge's connection state~~ **Done — ADR-0074.** `chorus_bridge` fires
  `on_connected` and `on_disconnected`, and the Satellite1 and Voice PE rings
  show Unreachable after 10 s without a sealed link.
- ~~Teach the harvester the ear's `unheard` canned line (ADR-0069)~~
  **Done.** The apology for words the ear could not decode is nobody's
  turn: it joins neither the turn before it nor either side of a pair,
  and cutting it is no preference.
- Check arbitration's choice against the journalled levels once a
  household runs it: channel 0 is AGC'd, which narrows the gap between a
  near and a far satellite (ADR-0072). The review UI's arbitration report
  shows the margins and the closest calls to listen to
- ~~Refuse a wake no enrolled voice made~~ **Done.** Found running a
  Satellite1: the television opened sessions. With nobody enrolled, or no
  speaker ID, every wake, a button press included, is rejected as
  `unknown_speaker` (§5, §9.3).
- ~~Enroll the household from the review UI~~ **Done — ADR-0080.** Voices
  (`/voices`) lists, enrolls and removes people in the `identities.yaml`
  chorusd reads; a browser take is made the satellite's format before it is
  sent.
- ~~Choose the voice and the ear~~ **Built — ADR-0081.**
  `CHORUS_TTS_PROVIDER` and `CHORUS_STT_PROVIDER` add OpenAI-compatible and
  Wyoming servers beside Kokoro and speaches, the defaults. Not yet run
  against a real server.
- ~~A satellite's own speaker accept level and click ceiling~~ **Done —
  ADR-0098.** `speaker_accept` and `max_crest` in `devices.yaml` override
  the house's for one satellite, as `speech_energy` does. A person still has
  one voiceprint, averaged across every satellite's takes (ADR-0082).

### Phase 3

~~Speculative execution~~ · S2S adapter · ~~eval replay suites~~.

- ~~Speculative execution~~ **Done in part — ADR-0095.** A turn is asked in
  its pause and kept only when the confirmed turn would ask the same, behind
  `CHORUS_SPECULATE`. Not yet: the wake's own utterance, speech rendered
  ahead, and one decode shared with the Dangling judge. Its saving is not yet
  measured on a real model.

- ~~Eval replay suites~~ **Done in part — ADR-0079, [the eval
  guide](eval.md).** A checked-in suite names journalled turns and what a
  good answer does; `chorus-eval` asks each again under a candidate
  prompt, model and tools beside the recorded versions, N times a side,
  and exits 1 when the candidate regresses. Each case is its turn's first
  ask, as Replay's are: follow-ups fed the recorded results, and a model
  judging the speech, are not built.

### Explicitly deferred

Media playback on satellites. Session survival across restart. Multi-tenancy.
OpenTelemetry export (§8, ADR-0068).

## 15. Decisions that cannot be retrofitted

Get these right in phase 1 or pay for them forever.

1. Interrupted-turn truth + truncation point (§4.4) — the DPO corpus depends on it.
2. Event log as source of truth, not instrumentation (§8).
3. `speak` as a declared tool, not a template quirk (§4.1).
4. Conversation keyed on person, audio on device (§4.5).
5. Speculative-work distinction present in the log from the start (§11).
6. Per-tool interrupt policy in the registry (§6).
