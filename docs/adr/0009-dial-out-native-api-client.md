# 0009. Be a native API client that dials the device

- **Status:** accepted
- **Source:** SPEC §3.1, §3.3.1 · commits `be99914`, `8212e3b`

The satellite is the TCP server on port 6053 and the controller is the client,
so replacing Home Assistant means dialing out to the device. Transport is
protobuf over Noise `NNpsk0_25519_ChaChaPoly_SHA256` with the PSK from
`api.encryption.key`; `aioesphomeapi` is the reference implementation.

There is no `ConnectRequest`: password auth was removed in ESPHome 2026.1.0, so
the Noise handshake **is** the authentication (`be99914`). Wire message ids are
read from the protobuf descriptors' ESPHome `MessageOption` rather than a
220-entry table that would drift every release (`eee20f4`).

`cmd/probe` confirms this against real hardware with no Home Assistant present
(§3.3.1).

## Consequences

The device advertises neither `FEATURE_SPEAKER` nor
`FEATURE_MULTI_CHANNEL_AUDIO` in its stock build, so `chorus_bridge` must
declare its own audio sources rather than assume the dual-channel wiring
described in §3.2 is active.
