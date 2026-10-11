# 0067. The speaker's volume is a native API number, and the hardware mute bit is the switch

- **Status:** accepted
- **Source:** SPEC §3.2, §3.3.1, §5, §6, §7 · ADR-0011, ADR-0050, ADR-0056 · commits
  `feat(schema): declare set_volume for a satellite's own speaker`,
  `feat(volume): the set_volume executor, with room resolution`,
  `feat(chorusd): set each satellite's speaker volume over the native API`,
  `feat(esphome): a Speaker Volume number the orchestrator sets`,
  `fix(esphome): report the HAT's mute switch as the hardware mute bit`

Two controls a household reaches for first did not work. "Turn it down" had
nothing to act on: no wire frame, no native API call and no tool set a
satellite's volume, and the firmware had none to set, because the stock
volume lives on a media player `esphome/satellite1.yaml` leaves out (SPEC
§1). And the `mute` frame's hardware bit was not the hardware:
`chorus_bridge` sent `Microphone::get_mute_state()`, which in ESPHome
2026.7.2 is the flag `microphone.mute` sets, a software zero-fill. Nothing
read the Satellite1 HAT's slide switch. Now the satellite has a Speaker
Volume number that chorusd sets through a `set_volume` tool, and the
hardware bit is the switch.

```go
import (
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/volume"
)

// The level is committed the moment it is sent, so a barge-in keeps the result.
func volumeDetaches() bool {
	return registry.Specs["set_volume"].OnInterrupt == registry.InterruptDetach
}

// What a Voice PE with no volume control answers: SPEC §7's own word.
var noVolume = volume.ErrNotImplemented
```

## Volume is a native API number, not a frame

The satellite declares a template `number`, Speaker Volume, 0 to 100 in
steps of 1, with `restore_value` so a level survives a reboot. It starts at
50, where the stock firmware's media player starts. Its value goes to the
i2s speaker that owns the TAS2780, which defers the amplifier's I2C writes
to its own loop; 0 mutes the DAC rather than leaving the quietest audible
level. The amplifier's activation applies the same level, so a satellite
muted at 0 stays muted through a boot. Home Assistant, if a household
connects one, sees the same entity.

chorusd finds it as it finds the ring (ADR-0056): its native API
connection lists the device's entities, and the number listed as
`speaker_volume`, which is the name Speaker Volume, is the volume. It
subscribes to states when it finds one, as it already did for a presence
sensor (ADR-0050), so it knows the level the device last reported, however
it was set. `set_volume` sends `NumberCommandRequest` (message 51 in the
vendored `api.proto`), scaled to the number's own range, and does not wait
for the device's report: until one comes, the level just set is where the
next step starts.

## One tool: a level or a signed step

`set_volume` takes `level`, an exact 0 to 100, or `step`, a signed change
from the current level clamped to the ends, and refuses both or neither.
A signed step was chosen over an `up`/`down` enum because the size is the
speaker's to choose: "a bit quieter" and "much louder" are different calls
with one parameter, and the step shares the level's units, so the model
never has to convert. A step needs the current level; an exact level does
not, and a level the device has not reported yet is `level_unknown` only to
a step.

Muting the speaker is `level` 0, not a tool of its own. The result carries
the level before the call (`was`), so "turn it back on" is the model setting
that level again from the dialogue. That keeps the registry at one tool for
the speaker.

The tool is `household`-scoped: anyone in the room may turn it down, a
guest included (SPEC §6). It is `detach`: the level is set the moment the
command is sent, so a barge-in keeps the result instead of leaving the model
unsure how loud the room is (SPEC §4.4), as `timer_start` does.

## The room is the turn's

With no `room`, the satellite is the one the call was made on, which the
session already hands every executor (`session.Caller.Satellite`): the room
each turn is told (ADR-0050). A `room` names every satellite whose room or
name it is, so two satellites in one room are both set, and the result says
which were set and which could not be. A room no satellite is in is refused
with the rooms there are, as `announce` refuses one.

## Not implemented is a result

A satellite whose connection listed no volume number answers
`not_implemented`: the office's Voice PE, or a Satellite1 still on older
firmware. One whose native API connection is down, or has not listed yet,
answers `not_connected`, since what it has is not known. When every target
gave the same reason the error is that word alone, so the model reasons from
the same vocabulary as a tool with nothing behind it (SPEC §7). The tool is
offered whenever chorusd runs, as every tool with an executor is (ADR-0060).
The result is journalled as any tool result is.

## The hardware bit is the switch

The HAT's mute slide switch is a pin on the XMOS's button port, `INPUT_A`
pin 3, which FutureProofHomes' `buttons.yaml` reads as "Button Left (HW
Mute)" and mirrors into a "Mute Microphones" switch. `esphome/satellite1.yaml`
reads the pin as a `gpio` binary sensor, Mute Switch, and names it to
`chorus_bridge` as `hardware_mute`. The bridge now reports:

- bit 0, hardware: that binary sensor is on, or has not been read yet,
  since not knowing is not consent;
- bit 1, software: `Microphone::get_mute_state()`, the zero-fill
  `microphone.mute` sets.

The frame and its format are unchanged (`esphome/README.md`), and so is the
host: `internal/listen` already drops the room's audio while either bit is
set. The switch is read in firmware rather than from its native API entity
because the bridge is what the host trusts to say mute, on the link that
carries the audio, and it reports it from the moment the link is
authenticated.

## Alternatives rejected

- **A `chorus_bridge` volume frame.** The ring set the precedent: an
  ordinary native API entity does what a frame would, and a frame changes
  the wire protocol both ends must flash in step (ADR-0033, ADR-0066).
- **Reviving the stock media player for its volume.** Chorus is not a media
  player (SPEC §1), and its pipelines would take a mixer source and the
  memory beside it for one float.
- **`up` and `down` with a fixed step.** Every "a lot louder" would be two
  or three calls, and the step size would be the orchestrator's guess.
- **A `mute_speaker` tool.** A second tool for level 0 would grow what the
  model is offered without letting it do anything new.
- **Reporting the switch from chorusd, over the native API.** The host would
  hold mute on a connection independent of the audio link, which can be
  down while audio flows.

## Forecloses

- The volume is found by its name. A board whose YAML names its volume
  number otherwise is a satellite with no volume control, as a ring
  without Chorus's effects is left alone.
- The number is the speaker's whole output: speech and anything a later
  media source plays are turned down together.
- `set_volume` reports the level it sent, not the level the device
  confirmed. A device that ignores the command is corrected only by its
  next report.
- A satellite with the switch on still streams audio frames, zero-filled
  only if the HAT's own wiring silences the mics; the host drops them. A
  firmware that also stops the uplink on the switch is a separate change.
- A Voice PE has no Chorus YAML, so no volume number and no switch bit,
  until one is written.
