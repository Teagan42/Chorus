# 0020. Supersede 0010: the socket and playback-position APIs as they exist

- **Status:** accepted · supersedes [ADR-0010](0010-chorus-bridge-external-component.md)
- **Source:** SPEC §3.1, §3.2.1 · ESPHome 2026.9.1 (`ESPHOME_VERSION` in `Taskfile.yml`)

The decision in [ADR-0010](0010-chorus-bridge-external-component.md) is
unchanged — audio leaves the device over the component's own outbound TCP
socket, the native API carries control only, and ESPHome is not forked. Two
supporting facts in it were wrong against real ESPHome source, both in the
direction of assuming a helper exists that does not.

**There is no managed outbound-link helper.** `components/socket/tcp_client_link.h`
does not exist; `tcp_client_link` and `TcpClientLink` have zero occurrences in
the tree. `components/socket/` offers only the raw abstraction: take a socket
from `socket::socket_loop_monitored(AF_INET, SOCK_STREAM, 0)`, `setblocking(false)`,
fill the address with `socket::set_sockaddr(...)`, then `connect()`. Reconnect
is a retry timer the component owns, and completion of a non-blocking connect
is an `SO_ERROR` poll the component owns. `set_sockaddr` takes an IP string and
does **not** resolve hostnames, so the orchestrator address cannot be an mDNS
name on the device side.

**`add_audio_output_callback` reports a delta, not a cumulative count.** The
signature is `CallbackManager<void(uint32_t, int64_t)>` and every caller passes
frames from one completion event: `i2s_audio_speaker_standard.cpp` passes the
real (non-padding) frames of a single DMA buffer, and `mixer_speaker.cpp` and
`resampler_speaker.cpp` forward per-call increments. The consumer accumulates.
The resampler rescales increments into the source rate, so feeding 16 kHz
yields 16 kHz frames back.

§3.2.1's conclusion survives intact: position is still DAC-accurate, still
bounded by the DAC FIFO and amp delay, and still ~100× better than the
byte-count proxy. Only the word "cumulative" was wrong about the callback.

## Forecloses

Reading `frames_played` straight off the callback. A consumer that treats the
first argument as a running total silently reports a position near zero, which
would corrupt every truncation point and therefore the DPO corpus
([ADR-0005](0005-interrupted-turn-truth.md)) — with no symptom louder than
slightly-wrong training data.
