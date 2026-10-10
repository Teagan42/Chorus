# 0047. The satellite board keeps the reference voice frontend and adds a wire

- **Status:** proposed
- **Source:** SPEC §3.2.1, §3.3, §3.3.2, §4.3, §5, §1 · ADR-0010, ADR-0033 ·
  FutureProofHomes/Satellite1-XMOS `bb411c7`, Satellite1-Hardware `2eb08ff`

Chorus's own satellite PCB ([the design](../hardware/README.md)) keeps
everything the Satellite1 proved and changes only what SPEC §3.3.2 measured as
the limit. It is an ESP32-S3-WROOM-1-N16R8 beside an XMOS XU316 running the
Satellite1's XMOS firmware, with every XMOS-facing pin where
`esphome/satellite1.yaml` already drives it, so `chorus_bridge` and
FutureProofHomes' components port unchanged. What it adds is a W5500 with
802.3af PoE, because uplink and downlink sharing one 2.4 GHz radio is the
failure no firmware recovers; the TAS2780's sensed speaker current wired back
to the ESP32, so the truncation point's fixed delay is measured per board
rather than taken from a datasheet (§3.2.1); eight mics on a circle instead of
four, so direction of arrival can help the barge-in gate and attribution
(§4.3, §5); and a mute switch that cuts the mics in hardware. `internal/board` checks the pin map against the module and
against the Satellite1 config on every `task test`.

The frontend is kept rather than improved because speaker identity is
compared across rooms (§5): a voiceprint enrolled at one satellite has to match
at the next, and SPEC §3.3 records the reference devices as acoustic peers
with no degraded tier. A board that hears differently would make the
conversation that follows a person less sure who it is following.

"Keeps" includes the parts of the Satellite1's XMOS sheet that are easy to
miss: the ESP32 resets the XU316 active high through an N-FET, and reaches its
QSPI flash through two SPI muxes while it is held in reset, which is how the
XMOS firmware is written. The XMOS firmware is under the XMOS Public Licence
v1, whose device condition is XMOS silicon, which this board has; its two
uplink channels are both processed, so the wire's "raw" channel is not raw
here either. The four added mics sit on PDM data lines the Satellite1 leaves
as test points and are invisible to the stock firmware; reading them, and
estimating direction, is firmware work with its own ADR.

The status stays proposed until a rev A board passes `task test:hardware`
over its cable.

## Alternatives rejected

- **ESP-SR's AFE on the ESP32-S3, no XMOS.** One chip and a cheaper board, but
  AEC would share the S3's cores with `micro_wake_word`, the bridge and the
  network stack, ESPHome has no component for it, and it is a new frontend,
  which is what §5 cannot afford.
- **XMOS XVF3800.** Four-mic beamforming and direction of arrival out of the
  box, but ESPHome support is a community fork with AEC reported not working,
  and it is a new frontend. It stays the fallback if the XU316 cannot run a
  direction estimator beside AEC.
- **The Voice PE's XMOS firmware and interface.** The same licence, and an
  I2C-controlled, two-bus ESP32 interface that would move pins for no gain.
- **The Satellite1's four mics only.** Its adjacent mics, 45.4 mm apart,
  alias direction above about 3.8 kHz; eight on the same circle push that to
  about 7 kHz for a few dollars, and adding them later means a new board.
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
voiceprint enrolment story, not just a new board. Sharing or selling boards
beyond the household: the frontend is redrawn from a CERN-OHL-S-2.0
schematic, so the board's full source would be published under it.
