# 0047. The satellite board keeps the reference voice frontend and adds a wire

- **Status:** proposed
- **Source:** SPEC §3.2.1, §3.3, §3.3.2, §5, §1 · ADR-0010, ADR-0033

Chorus's own satellite PCB ([the design](../hardware/README.md)) keeps
everything the Satellite1 proved and changes only what SPEC §3.3.2 measured as
the limit. It is an ESP32-S3-WROOM-1-N16R8 beside an XMOS XU316 running the
Satellite1's XMOS firmware, with every XMOS-facing pin where
`esphome/satellite1.yaml` already drives it, so `chorus_bridge` and
FutureProofHomes' components port unchanged. What it adds is a W5500 with
802.3af PoE, because uplink and downlink sharing one 2.4 GHz radio is the
failure no firmware recovers; the TAS2780's sensed speaker current wired back
to the ESP32, so the truncation point's fixed delay is measured per board
rather than taken from a datasheet (§3.2.1); and a mute switch that cuts the
mics in hardware. `internal/board` checks the pin map against the module and
against the Satellite1 config on every `task test`.

The frontend is kept rather than improved because speaker identity is
compared across rooms (§5): a voiceprint enrolled at one satellite has to match
at the next, and SPEC §3.3 records the reference devices as acoustic peers
with no degraded tier. A board that hears differently would make the
conversation that follows a person less sure who it is following.

The status stays proposed until a rev A board passes `task test:hardware`
over its cable.

## Alternatives rejected

- **ESP-SR's AFE on the ESP32-S3, no XMOS.** One chip and a cheaper board, but
  AEC would share the S3's cores with `micro_wake_word`, the bridge and the
  network stack, ESPHome has no component for it, and it is a new frontend,
  which is what §5 cannot afford.
- **XMOS XVF3800.** Four-mic beamforming and direction of arrival are real
  gains for attribution, but ESPHome support is a community fork with AEC
  reported not working, and it too is a new frontend. Rev A places four mic
  footprints so a later revision can move to it without a new enclosure.
- **ESP32-P4 with its own Ethernet MAC, or an ESP32-C5 for 5 GHz Wi-Fi.** Both
  answer the airtime problem; both leave the S3 platform that
  `micro_wake_word`, the duplex I2S driver and the Satellite1 components are
  proven on. The S3 plus an SPI Ethernet chip answers it without moving.
- **A 20 V USB-PD supply for amplifier headroom, as the Satellite1 does.**
  Chorus is not a media player (§1); a 12 V rail from PoE puts the TAS2780 in
  its full-power mode without a PD charger on every wall.

## Forecloses

Running Ethernet and Wi-Fi at once: ESPHome builds one or the other, so the
board ships as two firmware builds. A frontend change later means a new
voiceprint enrolment story, not just a new board.
