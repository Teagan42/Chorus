# 0100. Muted, the microphones' audio does not leave the device

- **Status:** accepted · refines [ADR-0067](0067-the-speakers-volume-is-a-native-api-number-and-the-hardware-mute-bit-is-the-switch.md)
- **Source:** SPEC §3.2, §14 · ADR-0067, ADR-0074

ADR-0067 reported the mute switch and left the stream alone: a satellite
with the switch on still sent its mic frames, zero-filled only if the HAT's
own wiring silenced the mics, and the host dropped them. It recorded a
firmware that stops the uplink as a separate change. This is that change: a
switch labelled mute that still transmits is trusting the far end with what
the household asked not to be heard, sealed (ADR-0074) or not.

While either mute bit is set, hardware or software, as the host already
treats them, `chorus_bridge` queues no `mic` frame and drains each mic ring
without sending it. Draining rather than holding: a ring kept through the
mute would be sent the moment the device unmuted, a ring's worth of the room
arriving as if live. The state is read fresh each `loop()` rather than from
the last reported flags, which a full TX queue can leave behind the switch;
the `mute` frame is queued first in the same `loop()`, so the host is told
why before the audio stops. A switch not yet read counts as on, so nothing
is sent at boot until it is.

Nothing on the host changes: it still drops audio on either bit, which now
covers a device running older firmware.

## Forecloses

- Nothing reaches the host from a muted room, so nothing from one can be
  journalled or harvested: a reviewer cannot hear what the household said
  while muted, which is the point.
- `wake` frames are not suppressed. The on-device wake word hears the same
  silenced mics; a Voice PE's button still sends one, and the host already
  refuses a session while muted.
