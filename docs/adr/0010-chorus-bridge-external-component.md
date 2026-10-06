# 0010. Ship audio over a raw socket from an external component, not the native API

- **Status:** accepted
- **Source:** SPEC §3.1, §3.2

Stock `voice_assistant` is half-duplex, which kills barge-in — and the limit is a
pure software guard, three `set_state_(State::STOP_MICROPHONE, …)` calls in
`voice_assistant.cpp`. The I2S mic and XMOS AEC pipeline never stop during TTS;
only `voice_assistant`'s own `enabled_` flag flips. So `chorus_bridge` is an
`external_components:` package with its own outbound TCP socket
(`components/socket/tcp_client_link.h`): no inbound port, auto reconnect, native
API retained for control only, and `voice_assistant` removed from the YAML.

## Alternatives rejected

- **Fork ESPHome.** Audio cannot ride the native API without new message ids,
  which means editing `api.proto` and rerunning ESPHome's codegen.
- **Reuse `VoiceAssistantAudio` (106).** It dispatches to the `voice_assistant`
  singleton.
- **`SerialProxy`.** Bound to a physical UART and marked experimental.
- **User-service args.** PCM as unpacked sint32 or base64 at 32 kB/s.

## Consequences

Two `MicrophoneSource` consumers (AEC'd channel 0, raw channel 1) register
**non-passive**: a `MicrophoneSource` is a per-consumer gate, not a hardware
refcount, so a `passive` source receives audio only while some non-passive
consumer started the mic. Registering passive ships a silently dead bridge
(§3.2). The `weak_ptr` handoff copied from `voice_assistant.cpp:38-52` is
deliberate and must not be simplified — the mic task may fire once more after
the ring buffer is dropped.
