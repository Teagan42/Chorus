# 0051. Say a failed model or voice out loud, keep it in the log, and never record it as a barge-in

- **Status:** accepted
- **Source:** SPEC §4.5, §7, §9.1 · ADR-0008

ADR-0008 reserved canned speech for an unavailable model or voice and made
failures first-class events. Neither was built, and the 2026-10-10 audit
found three ways it showed:

- Ollama refusing the connection made `Engine.Turn` fail, and the listener
  only logged it. The house went silent and the log held nothing.
- A model that never answered kept the turn open forever. The Ollama client
  has no timeout on purpose, because a turn streams for as long as the model
  talks, and the 20 s backstop skips while `thinking` is alive. The
  satellite ignored every wake until its link dropped.
- When Kokoro died mid-utterance, the speech channel defaulted the missing
  reason to `barge_in`. The log said the person interrupted, and the
  harvester and replay read it that way.

**The model.** Each ask has a deadline, `session.Config.ModelTimeout`,
measured from the last thing the model emitted and restarted by every
action. When it passes, only the model's stream is cancelled; calls the model
already made run on. The default is 90 s. That is longer than a cold load
(~71 s measured, `internal/provider/ollama`), because Ollama aborts a load
whose request is cancelled. A shorter deadline would leave a model that was
evicted overnight unloadable, since every ask would cancel the load the next
one needs.

An ask that never starts, breaks mid-stream, or misses its deadline is
journalled as `model_failed` (`unavailable`, `failed` or `timed_out`, with
the engine's error), and the turn ends without a follow-up ask. A barge-in
or the session ending is not a failure, and records none.

**The voice.** A stream that stops short when nobody cut it reports
`Playback.Failure`, which is one of two things:

- `tts_unavailable`: the synthesiser would not open or render.
- `playback_unconfirmed`: the drain deadline passed before the device
  confirmed the end.

The session journals `speech_failed`, then the truncation or discard with
that reason. `speech_truncated` now always names its reason. The speak call's
result is `error`, and the dialogue tells the model its voice failed rather
than that the person cut it off. A failed voice also drops the rest of the
turn's speech, since it would fail the same way. A device that went quiet
may still be playing, so it drops nothing.

**What is said.** One canned line per turn, from `session.DefaultCanned`:

- one for the model ("Sorry, I can't think straight right now…")
- one for the voice ("Sorry, I've lost my voice for a moment.")

It is a speak call of its own with `"canned":true`, and the failure event
names it in `canned_call_id`. The dialogue, the harvester and re-runs skip it,
as they skip announcements (ADR-0045): nobody's turn chose those words.

The voice's line can only be heard if it was rendered before the voice
failed. `satellite.Canned` wraps the synthesiser and keeps both lines,
rendered by the daemon at startup and retried every 30 s until Kokoro
answers. It serves a kept segment whatever state Kokoro is in. A voice that
fails before that render has finished, or a device that will not open a
stream at all, still fails silently, but it fails in the log, not as a
barge-in.

## Alternatives rejected

- **A deadline from the start of the ask.** A model that keeps calling
  tools and speaking is working, however long the turn takes.
- **A short deadline with a "still thinking" line, then the real one.**
  This is better to hear during a cold load, and it is a feature of its own.
  The deadline alone ends the deafness.
- **Recording a voice failure as `speech_spoken` plus `speech_discarded`.**
  The utterance was cut short, and the truncation is the honest event. The
  missing piece was the reason.
- **A synthesised earcon instead of rendered words.** SPEC §7 promises
  speech, and the words cost one render at startup.

## Forecloses

A model that says nothing for 90 s is given up on, even if it would have
answered at 91 s. A deployment with slower cold loads raises
`ModelTimeout`; lowering it below the load time breaks warm-up.
