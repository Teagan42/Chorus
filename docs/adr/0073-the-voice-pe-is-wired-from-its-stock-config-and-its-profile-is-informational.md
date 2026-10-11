# 0073. The Voice PE is wired from its stock config, and its profile is informational

- **Status:** accepted · refines [ADR-0056](0056-an-interjection-pauses-the-voice-and-the-session-drives-the-ring.md) and [ADR-0067](0067-the-speakers-volume-is-a-native-api-number-and-the-hardware-mute-bit-is-the-switch.md)
- **Source:** SPEC §3.2, §3.3.1, §9.3, §13 · ADR-0010, ADR-0033, ADR-0050, ADR-0066, ADR-0069 · `esphome/home-assistant-voice-pe` at `d4e6fa43d6a1a7d342b7d4d403567e06516719de`

SPEC names two satellites, the FutureProofHomes Satellite1 and the Home
Assistant Voice Preview Edition, but only the Satellite1 had a Chorus
config. ADR-0056 and ADR-0067 each ended on the same gap: a Voice PE has no
ring Chorus can drive, no volume number and no hardware mute bit until
someone writes one. `esphome/voice-pe.yaml` is that config. Its wiring comes
from the stock `home-assistant-voice.yaml` at the commit above, it uses
`packages/chorus-bridge.yaml` like the Satellite1, and it gives chorusd the
same entities by the same names, so the host changes nowhere.

```go
import "github.com/teagan42/chorus/internal/bridge"

// The office Voice PE's channel 1: kept for the corpus, decides nothing,
// as on the kitchen's Satellite1 (ADR-0050).
var officeSecond = bridge.ChannelSecond
```

## Where each piece comes from

| Piece | Stock config | `voice-pe.yaml` |
|---|---|---|
| XMOS | `voice_kit` on `internal_i2c`, reset GPIO4, firmware 1.3.1 by URL and MD5 | Same, the component pinned to the commit rather than `dev`, and its channel stages spelled out |
| Mics | `i2s_audio` stereo, 16 kHz 32-bit, secondary, LRCLK 14, BCLK 13, DIN 15 | Same; the bridge takes channels 0 and 1, the wake word channel 0 |
| Speaker | i2s secondary, LRCLK 7, BCLK 8, DOUT 10, 48 kHz 32-bit stereo, AIC3204; mixer of announcement and media inputs, a resampler on each | Same i2s speaker and DAC; mixer of `tts_source` (500 ms, as on the Satellite1) and `media_source`, one resampler, `tts_speaker`, at 16 kHz in |
| Amplifier | GPIO47, on a second after boot | Same, then the volume applied |
| Ducking | `apply_ducking` 20 dB on the media input | `ducking_speaker: media_source`; nothing sends `duck` yet (ADR-0056) |
| Ring | 12 WS2812 on GPIO21 with explicit timings, LED supply on GPIO45, remapped 7..11, 0..6 | Same strip and remap, as a light named LED Ring with Chorus's five effects |
| Dial | Rotary encoder on GPIO16 and 18, media player volume ±0.05 | Same encoder, Speaker Volume ±5 |
| Mute | GPIO3, on while muted, mirrored into a software switch | The binary sensor alone, as `hardware_mute` |
| Centre button | GPIO0 inverted; a click stops or starts things, longer holds are events and a factory reset | A short click is a wake |

## Channel 1 is set at boot, and set to the Satellite1's

The Voice PE's XMOS does not fix what its outputs carry: `voice_kit` writes
the stage each channel is taken after, over I2C, at every boot. Its defaults
are AGC for channel 0 and NS for channel 1, and the stock file takes them.
`voice-pe.yaml` writes the same values explicitly. Channel 1 is then
AEC+IC+NS, channel 0 before its AGC, as on the Satellite1, so both boards'
second channels are the same signal in the corpus (SPEC §9.3). SPEC §3.2 had
said AEC alone; the pinned source says otherwise, and §3.2 now says what the
config sets. `NONE` would make channel 1 the bare mic, which is a one-line
change if retraining wants it.

The stock file runs `micro_wake_word` on channel 1 with a gain of 4. The
package runs it on channel 0 on both boards, because the host confirms the
wake on channel 0, and the package is shared.

## The ring, the volume and the mute are the Satellite1's

The ring keeps the Satellite1's effects and colours: steady blue for
`Listening`, a short arc chasing round for `Thinking`, the ring breathing
for `Speaking`, steady red for `Muted`, blinking amber for `Error`. The arc
is two LEDs where the Satellite1's is three, and it steps every 80 ms, so it
still goes round once a second.

Speaker Volume is the same number: 0 to 100, kept, 0 mutes the DAC. Levels
1 to 100 map onto 0.4 to 0.85 of the AIC3204's volume, the range the stock
media player maps its own 0 to 100% onto. The dial sets the number, never
the DAC, so the number's state changes and chorusd, already subscribed to
it, reads the level a dial turn left. The host and the dial cannot disagree
for longer than a state report takes.

The side switch is the `mute` frame's hardware bit, read as stock reads it.
Stock also unmutes the `Microphone` when the switch is released, which is
the software bit; Chorus leaves the software bit alone.

## The centre button is a wake

A short click calls `id(chorus).on_wake_word("button")`, the hook the wake
word calls. The host arms the listener exactly as for "Hey Eddie" and
confirms the wake on the next utterance (SPEC §9.3, checks 2 and 3). It
ignores the press while muted, during a session or during an announcement,
so the button is push-to-talk and nothing else. It needs no `chorus_bridge`
change and no host change: the wake frame already carries a name, and the
host already ignores it.

## The profile is informational

`config.Satellite.Profile` is parsed and read by nothing. Every difference a
Voice PE has, chorusd already finds out from the device: the ring and the
volume by name and effect, presence by device class, and the Voice PE lists
no presence sensor, which chorusd already logs as no fault. Both boards
stream the same two channels, and the ring's size is the YAML's business.
So no Go code branches on the profile, and SPEC §13 and
`devices.example.yaml` say it is informational.

## Alternatives rejected

- **A package over the stock file, with `voice_assistant: !remove`.** Its
  LED, button, dial and timer scripts reference `voice_assistant`, its
  media player and Sendspin throughout. Removing each is more YAML than the
  wiring, and every stock update would re-break it.
- **The button stops playback.** The device would stop the voice without
  the host's tagged stop, so there would be no truncation point and nothing
  journalled (ADR-0033). It needs a frame that asks the host to stop, which
  is a `chorus_bridge` and wire change.
- **The button toggles the software mute.** It needs nothing new, and the
  ring would show it. But the board already has a mute switch, and a stock
  user pressing the button to talk would silently mute the room.
- **A Go profile switch.** Nothing found differs in a way the entity list
  does not already tell the host.
- **The stock file's ESPHome 2026.9.** The Satellite1's vendor components
  fix the repo at 2026.7.2, and nothing this config uses needs 2026.9.

## Forecloses

- **Nothing here has run on a Voice PE.** The config passes
  `esphome config` on 2026.7.2. It has not been compiled or flashed, and the
  duplex measurements in SPEC §3.3.2 are the Satellite1's alone.
- **A button wake that fails confirmation is journalled as a wake word's.**
  `wake_rejected` is a hard negative for retraining, and the host drops the
  wake's name, so a press followed by silence or a guest lands in the corpus
  as if the model had fired. Telling them apart means recording the name the
  frame already carries, a change in `internal/listen` and the event schema
  not made here. When check 1, re-scoring the pre-roll, is built, a button
  wake has no wake word to score and must be exempt by that name.
- **The dial is volume only.** Stock's hue control, group volume and the
  volume shown on the ring are gone: the ring belongs to chorusd, and a
  local display would fight it.
- **No factory reset from the button, no jack handling, no BLE setup.**
  Recovery is USB, as on the Satellite1.
- **The XMOS is updated on boot** to firmware 1.3.1 whenever it reports
  another version, as stock does. A Voice PE on newer XMOS firmware is
  downgraded.
