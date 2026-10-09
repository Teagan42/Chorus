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
   STT          LLM         TTS      speaker-ID     (Python sidecars, gRPC)
 Parakeet   Qwen3/vLLM    Kokoro      ECAPA
    │
┌───┴──────────────────┐   ┌─────────────────────┐
│ Postgres (JSONB log) │   │ review UI (Go+htmx) │
│ MinIO (audio blobs)  │   │ dataset export      │
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

- Two `MicrophoneSource` consumers on one `Microphone`: channel 0 (AEC'd) and
  channel 1 (raw), each with its own ring buffer. This is what stock does.
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
20 dB) — not on the base class.

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
AEC'd/raw sources must be declared by `chorus_bridge` rather than assumed
present.

Entities that matter, all usable from the orchestrator over the native API:

| Entity | Use |
|---|---|
| `light` **LED Ring** | §19 concurrency feedback — speaking *and* working |
| `select` **Wake word sensitivity** (3 levels) | **Immediate relief for cough false-accepts, no retraining** (§9.3) |
| `switch` **Capture wake-word audio** | Stock corpus capture hook — inspect before building our own |
| `binary_sensor` **Room Presence** + mmWave radar suite | Unplanned bonus: presence-gated sessions, occupancy-aware routing |
| `switch` **Mute Microphones** (HW) | Hardware mute is a user-facing privacy control; respect it |
| `button` **XMOS Flash Embedded FW** | XMOS firmware is flashable from the API |

### 3.3.2 Full duplex, measured on the device

The Phase 1 blocker (§14) is closed. `internal/bridge/hardware_test.go` runs
against the living-room Satellite1 with no Home Assistant involved: the device
dials the orchestrator's audio port, declares `{version 1, 16 kHz, 16-bit, 2 mic
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
position. Inline `content` from models whose template emits `content` +
`tool_calls` together is treated as an implicit `speak`.

### 4.2 Speech channel

Default **queue** — natural for "one sec" → tool result → "found three". A
declared field allows `queue | preempt | interject` per call; `preempt` is for
when a tool result invalidates what was about to be said.

On barge-in, the unspoken remainder is **discarded but recorded** — generated
but never spoken is a distinct event type from spoken. The model sees only what
was actually heard, and may repeat itself in its own words.

### 4.3 Barge-in

Detection gate, stacked, ~300 ms from user speech to TTS stop:

1. VAD energy
2. **Speaker-ID match** against the session's current speaker or a known
   household member — the high-value filter. The TV and the wrong housemate both
   fail it. Much stronger than energy thresholds.
3. STT partial length gate (not "uh", not a single word)

No semantic "was this addressed to me" check: it costs latency exactly where
latency is viscerally felt, and a wrong stop is cheap (keep talking) while a
slow stop feels broken. **Every rejected candidate barge-in is logged** as the
tuning corpus for this gate.

### 4.4 Interrupted-turn semantics

Record the **truth**. What was actually spoken is kept, marked interrupted, with
the truncation point. Tool results that arrived are kept. The model sees "I said
this much, then was cut off" and can reason about it.

Per-tool `on_interrupt` policy, declared in the registry:

- `cancel` (default)
- `detach` — finish, keep the result (wasted but harmless work)
- `uninterruptible` — must complete (side effects already committed)

This is unretrofittable. It is why truncation fidelity matters — and why we take
the position from `add_audio_output_callback` (§3.2.1) rather than estimating.

### 4.5 Session lifecycle

Wake word opens a session; continuous streaming while open; **server-side
semantic endpointing** (Smart Turn v2 over STT partials, not the big LLM) so
"turn off the... uh... kitchen lights" works.

Close is **model-decided** via an `end_session` tool, with a ~20 s silence
backstop. The model knows when a task is done better than a timer does.

**The conversation belongs to the person; the audio stream belongs to the
device.** Keeping those separate makes device migration fall out for free: wake
word on a different satellite within ~2 minutes for the same identified person
resumes the same logical conversation.

## 5. Identity, context, memory

Explicit enrollment (guided phrases). Per-utterance speaker embedding
(ECAPA-TDNN / TitaNet-L, cosine against enrolled centroids). The session holds a
current speaker, but a confident mismatch **flips attribution within the same
conversation** — a second person chiming in is a real case Assist cannot handle.

Unknown or low-confidence speaker → guest context, person-scoped tools gated, no
interrogation. The embedding is stored on every trace record regardless, which
gives implicit clustering for free later.

Memory: explicit `remember` / `forget` tools plus an auto rolling summary per
person, both injected by relevance. Person context is global across satellites;
the satellite contributes **location as turn metadata, not identity**.
Cross-person visibility off by default, with an explicit per-memory `shareable`
flag — this is a household, not a tenancy, but surprising disclosure is how a
voice assistant loses trust.

## 6. Tool registry

Native Go registry; MCP is one provider among several; HA is an adapter
exposing entities, areas, and scripts. The registry owns what MCP has no
vocabulary for:

- `on_interrupt` policy (§4.4)
- `timeout` (default ~10 s)
- person scoping
- `requires_confirmation` (optional, per-tool, default off)
- latency hints

MCP-sourced tools get conservative default policies.

**Confirmation** is orchestrator-enforced but model-authored. The orchestrator
blocks execution and returns a synthetic `confirmation_required` tool result
carrying a nonce; the model phrases the confirmation itself and re-calls with
the nonce after an affirmative. Hard safety guarantee without the orchestrator
owning the dialogue — and the nonce makes "did the user actually say yes"
auditable in the trace.

## 7. Failure semantics

**Everything recoverable becomes a tool result the model reasons about; only
infrastructure failures get canned speech.** A tool timeout returns
`{error: "timed_out"}` and the model says something true and useful, instead of
the orchestrator barging in with "sorry, something went wrong."

Canned fallback reserved for LLM-unavailable and TTS-unavailable, where there is
no model left to reason with. Failures are first-class event types — they are
training data too.

Sessions do not survive an orchestrator restart in phase 1. The log replays for
debugging; resuming live audio across a restart is a lot of machinery for a case
solved by not restarting mid-sentence.

## 8. Event journal

The orchestrator is a **pure reducer over an append-only event log.** Tracing is
not instrumentation; it *is* the runtime. Get this invariant right and the
review UI, replay harness, and dataset export become readers of existing data
rather than new plumbing.

Storage: Postgres `JSONB`, partitioned by conversation, plus MinIO/filesystem
for audio blobs (raw mic PCM both channels, synthesized TTS). Keep everything;
retention configurable per satellite. 16 kHz mono is ~115 MB/day of continuous
capture.

Every event carries a monotonic sequence number, wall clock, the model /
prompt / tool-schema versions in effect, and the STT and TTS identities
(ADR-0032). Deterministic replay requires recording every nondeterministic
input: model completions (not just requests), tool results, and barge-in
timing to the millisecond relative to TTS position.

`replay(conversation_id, overrides)` is a tested function from week one. It is
what keeps the reducer honest — if it ever breaks, the system has silently
become instrumented-code-with-logs again.

OpenTelemetry emitted alongside, so Langfuse/Jaeger give free exploration
without ceding ownership of the annotation layer.

## 9. Learning loop

Primary target: **the LLM's tool-calling and speech-interleaving decisions.**
STT adaptation on household voices and vocabulary is a free byproduct. Wake word
is phase 2 (§10).

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
schema, re-run the trace, diff the outcome).

Review UI: Go + htmx over Postgres, in the monorepo. **Inline per-turn audio
playback is non-negotiable** — a voice assistant cannot be judged from
transcripts.

### 9.3 Two-stage wake confirmation (phase 1)

Observed failure in practice: the existing "Hey Eddie" model activates on
coughs. Recall is fine; precision is not. So the priority is suppressing false
accepts, not recovering false rejects.

`micro_wake_word` activation on-device is **stage one, and is not user-visible.**
The component sends a pre-roll with the activation; the orchestrator runs stage
two before any LED or chime:

1. re-score the pre-roll against the wake model at a higher threshold
2. confirm the segment contains speech at all
3. confirm the speaker embedding matches a household member

A cough fails all three. Only on confirmation does the session become
perceptible to the user. Rejections are logged with audio and auto-labeled as
hard negatives, so the retraining corpus fills itself with precisely the
negatives the model lacks — no manual labeling, no retraining needed to get
immediate relief.

Dual-channel capture means the corpus carries both AEC'd and raw streams, so
retraining can target whichever frontend ships.

### 9.4 False rejects (phase 2, lower priority)

False rejects are **uncapturable** under wake-word-gated streaming — no session,
no audio. If they become a problem, the fix is a **near-miss buffer**: a rolling
3 s pre-roll uploaded when wake confidence lands below the activation threshold
but above noise. Audio leaves the device only on a near-activation, so the
privacy story holds. Not needed today.

## 10. Model stack

Local-first, cloud escape hatch per provider. Each behind a streaming
gRPC/HTTP contract, every provider declaring capabilities (streaming? tool
calls? interruption?).

| Role | Choice | Rationale |
|---|---|---|
| STT | Parakeet TDT 0.6B v2 | Streaming, far faster than Whisper large at better accuracy. English-only; swap to faster-whisper if multilingual is needed. |
| LLM | Qwen3 32B (or 30B-A3B) on **vLLM** | Template emits `content` + `tool_calls` together; vLLM streams tool-call parsing. Both are hard requirements of §4.1. Ollama/llama.cpp are weaker at exactly this seam. |
| TTS | Kokoro-82M | Sentence-level streaming, sub-200 ms first chunk. Orpheus if more expression is wanted. Piper as degraded fallback. |
| Speaker ID | ECAPA-TDNN / TitaNet-L | Embeddings + cosine. |
| Endpointing | Smart Turn v2 | Purpose-built; avoids prompting the big model. |

Biggest standing risk: **local tool-calling quality.** The provider abstraction
exists so a frontier model can be dropped into the LLM seam without touching
the orchestrator.

A degraded local profile exists for "lights off" when everything else is down,
but local-model limits do not get to dictate the architecture.

## 11. Latency

Target **700 ms wake-word-to-first-audio.** Assist on local models is 1.5–3 s
and feels bad; realtime S2S is ~300 ms and feels alive.

Phase 1 is the straightforward streaming cascade with honest measurements —
expect TTS first-chunk and LLM prefill to dominate, not STT. Speculative
execution (LLM prefill on STT partials before endpointing, discarded if the user
keeps talking) is a later measured optimization.

**Speculative events are a recorded distinction from day one**, even though
phase 1 generates none. Adding an event type later is cheap; retrofitting the
distinction into replay is not.

## 12. Turn engine abstraction

The turn engine is an interface emitting a stream of events (`SpeechDelta`,
`ToolCall`, `ToolResult`, `TurnEnd`). Phase 1 implements it as the cascade. A
realtime speech-to-speech provider (OpenAI Realtime, Gemini Live) later becomes
an adapter mapping their event stream onto ours.

This is why `speak` is a tool rather than a model-template quirk (§4.1) — it is
the only framing that survives the S2S adapter.

Do not build the S2S path until the cascade works end to end.

## 13. Deployment

- **Monorepo.** The device component, orchestrator, and sidecars share a
  protocol contract that must version together. Split repos mean a coordinated
  release dance for a one-person project.
- **docker-compose**, model sidecars pinned to GPU.
- **Satellite inventory:** mDNS discovery (`_esphomelib._tcp`) for addresses;
  static YAML for Noise PSKs, room assignment, and capability profile. Secrets
  stay out of a database that would need separate backup.
- **Addressing is asymmetric.** Discovery runs one way only: the orchestrator
  resolves satellites, but the orchestrator's own address must be a literal IP
  in the device YAML. ESPHome's `set_sockaddr` does not resolve hostnames, so
  the device cannot dial an mDNS name. The orchestrator needs a static lease;
  `chorus_bridge` enforces this at `esphome config` time via `cv.ipaddress`.
- Wake word: existing trained "Hey Eddie" model, gated by two-stage
  confirmation (§9.3). Swappable without touching anything else.
- Persona lives in the system prompt as a **versioned artifact** — because
  prompt versions are recorded per event, persona changes are A/B-testable
  against replayed traces.

## 14. Phase plan

Struck through is merged on main and covered at the tiers that can see it.
Phase 1's pieces run together in `chorusd` against the in-process satellite
(`bridgetest`); the daemon has not yet run a household on real hardware.

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
   ADR-0037.** The LLM seam runs Qwen3 on Ollama today; §10's vLLM is not yet
   wired up.
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

- ~~Review UI~~ **Done — ADR-0034, [the review UI guide](reviewui/README.md).**
- Memory
- MCP provider
- ~~Confirmation gates~~ **Done — ADR-0038.** Garage covers are not held yet.
- Timers
- Announcements with `start_conversation`
- ~~Semantic endpointing~~ **Done — ADR-0036.**
- Wake-word retraining from the harvested negative corpus
- Near-miss buffer if false rejects surface

### Phase 3

Speculative execution · S2S adapter · eval replay suites.

### Explicitly deferred

Media playback on satellites. Session survival across restart. Multi-tenancy.

## 15. Decisions that cannot be retrofitted

Get these right in phase 1 or pay for them forever.

1. Interrupted-turn truth + truncation point (§4.4) — the DPO corpus depends on it.
2. Event log as source of truth, not instrumentation (§8).
3. `speak` as a declared tool, not a template quirk (§4.1).
4. Conversation keyed on person, audio on device (§4.5).
5. Speculative-work distinction present in the log from the start (§11).
6. Per-tool interrupt policy in the registry (§6).
