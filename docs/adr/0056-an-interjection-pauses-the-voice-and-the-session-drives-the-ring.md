# 0056. An interjection pauses the voice, and the session drives the LED ring

- **Status:** accepted
- **Source:** SPEC §3.2.1, §3.3.1, §4.2, §4.4 · ADR-0033, ADR-0050, ADR-0051

Two things the satellite was described as doing, it did not do. `speak`'s
`interject` mode was declared as "ducks and cuts in", but the speech
channel cut: the line playing was truncated as `preempted` and its rest
thrown away, which made `interject` a `preempt` that kept the queue. And
SPEC §3.3.1 has the LED ring show the satellite listening, working and
speaking, but nothing drove it, and `esphome/satellite1.yaml` had dropped
the ring along with `voice_assistant`. Now an interjection pauses what is
playing and resumes it after, and each session's state is shown on the ring
over the native API.

```go
import "github.com/teagan42/chorus/internal/session"

// The garage door, said over Teagan's forecast: paused, said, resumed.
var garage = session.SpeechDelta{CallID: "call_g1", Text: "Sorry to cut in, the garage door is open.", Mode: session.ModeInterject, Last: true}

// What the kitchen's ring shows while the forecast plays.
var playing = session.RingSpeaking
```

## Interject pauses, because one voice cannot duck under itself

The `duck` frame calls `SourceSpeaker::apply_ducking` on the mixer source
the YAML names as `ducking_speaker`. On the Satellite1 that is
`media_source`; the voice is `tts_source`, fed by the one `chorus_bridge`
stream. Ducking the voice would duck the interjection with it, since both
are that stream, and nothing feeds `media_source` on this firmware. So the
frame cannot lower the current speech under the interjection, and nothing
sends it. `satellite1.example.yaml`, which named `tts_source`, now names
`media_source` too.

An interjection therefore pauses. The speech channel holds the playing
utterance and cancels its stream, which sends the tagged stop; the pause
point is the report that answers it (ADR-0033), the same byte a barge-in
records. The interjection plays next, and the unheard rest is queued right
behind it under the same speak call, with any deltas that arrived in
between, ahead of whatever was already queued. The call has one result,
once the rest has played.

The pause is journalled as `speech_truncated` with the new reason
`interjected`: `spoken_text` is what was heard, `unspoken_text` everything
generated and not yet heard. When the device had played all that was
generated and the model is still going, there is no unheard text to name,
so the pause is `speech_spoken` instead. The rest is recorded as it plays,
like any speech. A barge-in or a close that lands before it resumes
discards it under that reason, so the call is cut by the person, never by
the interjection.

The reducer keeps a paused call playing (`Entry.Held`, `Pending`) with the
words heard so far, adds what plays after it, and cuts it if the rest is
discarded. The next ask is told the whole line once it has been heard.
`rerun` takes a paused and resumed line as one line of the take. Harvest
needs nothing: only a person's cut is half of a pair, and `interjected` is
not one.

The rest starts at the next clause, not the next word: a clause the DAC
entered counts as heard (internal/satellite/chunk.go), so at most one
clause's tail is skipped, as a barge-in already credits it.

Building this found a cut that skipped the settle: a speak call usually
arrives whole, so its stream is closed and waiting for the drain when a
cut lands, and that cut took the last routine report as its position and
called the shortfall `playback_unconfirmed`. A cut during the drain now
waits for its stop's answer like any other, barge-ins included.

## The session shows its state; the native API drives the ring

The stock FutureProofHomes and Voice PE firmware expose a user-facing
"LED Ring" light, but animate an internal one from `voice_assistant`'s
phase. Chorus's firmware has no `voice_assistant`, so nothing would
animate it. `esphome/satellite1.yaml` declares the ring again, wired as
their `led_ring.yaml` wires it (24 WS2812s on GPIO21), as a plain light
with three effects: `Listening`, `Thinking` and `Speaking`.

A session tells `session.Config.Ring` what its live children amount to,
whenever they change: speaking outranks working, which is the model or a
tool call still out, which outranks listening. The ring goes dark at the
close, before the cut speech settles. chorusd keeps the newest state per
satellite. Its native API connection lists the device's lights, and the
first one with all three effects is driven: `LightCommandRequest` (message
32 in the vendored `api.proto`) turning it on with the state's effect, or
off. It sends the current state when the listing ends, then each change.
A state passed through before it could be sent is skipped.

A ring without the three effects is left alone. The stock firmware's
"LED Ring" has none, and driving it would fight the firmware's own
animation of the same LEDs.

## Alternatives rejected

- **Mixing the interjection over the ducked line on the host.** Two
  overlapping utterances would need one truncation point each from one
  DAC position, and two sentences at once are not intelligible anyway.
- **Ducking the media source for the interjection.** Nothing plays
  through it on this firmware, and ducking for one mode of speech and not
  the others would be a rule nobody asked for.
- **A `chorus_bridge` state frame and firmware scripts that animate the
  ring from it.** The ring is an ordinary native API entity, and a frame
  would change the wire protocol (ADR-0033) for what an existing message
  does.
- **Finding the ring by its object id, `led_ring`.** The stock firmware's
  ring has the same one, and is the firmware's to animate.

## Forecloses

- An interjection cannot be heard over the speech it interrupts.
- The effect names are a contract between chorusd and the firmware. A
  board's YAML owns the colours and the animation, and a Voice PE needs a
  Chorus YAML with the same three effects before its ring is driven.
- The ring shows one session's state at a time and nothing else: not
  mute, not a timer ringing.
- `Link.SetMicEnabled` still has no caller. SPEC §2 keeps the mic open
  even during playback, and nothing in the spec stops the uplink.
