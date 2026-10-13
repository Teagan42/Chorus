# Enroll a household voice

Chorus accepts a spoken wake only when speaker ID matches an enrolled
household voice. This prevents television dialogue and other unknown voices
from opening a session. Until the first person is enrolled, spoken wakes are
silently rejected; an unknown speaker cannot be distinguished from the TV.

Record at least three clean, distinct takes as WAV files: 16 kHz, mono, signed
16-bit PCM. Use your own voice, with the satellite in its usual room if
possible. Never use clips of the TV, or of anyone but the person being
enrolled.

A microphone that is not the satellite's matches it less surely, and the gap
is large: a household enrolled from a laptop browser scored 0.406 against 30
seconds of its own cleanly-transcribed speech through a Voice PE, under the
0.70 threshold, so every spoken wake was refused. Prefer takes the satellite
itself recorded — the Voices screen offers them (ADR-0082).

Put those files in `voice-takes/` at the repository root, then enroll through
the one-shot Docker helper:

```sh norun
docker compose run --rm enroll add -id household -name "Household member" \
  -identities /var/lib/chorus/identity/identities.yaml \
  -wav /takes/take-1.wav -wav /takes/take-2.wav -wav /takes/take-3.wav
```

The helper uses the speaker-ID sidecar and writes the biometric voiceprint to a
private Docker volume. Restart chorusd to load it:

```sh norun
docker compose restart chorusd
```

Or enroll from a browser: the review UI's Voices screen
(`docker compose up -d speakerid reviewui`, then
`http://127.0.0.1:8080/voices`) takes the same WAV files, or records the
takes on the page, and writes the same volume. Restart chorusd afterwards
just the same. See [Voices](reviewui/README.md#voices).

## From a satellite's own recordings

The same screen offers what chorusd already kept from a satellite you pick,
which is that satellite's own front end's output and so the closest match to
what it will hear (ADR-0082). Tick at least three, after playing each one:
the log does not know whose voice a clip holds, so hearing it is the check.
Only clips with enough speech in them are listed, newest first, each showing
how much was spoken; a clip is measured on the part that was actually spoken,
since a 30-second recording is mostly room. The floor is 3.5 seconds: two
one-second slices of a single voice score about 0.5 against each other, and a
2.8-second clip scored 0.268 against its own speaker's voiceprint, while the
takes that built a working one ran 3.2 to 4.4 seconds.

Record from more than one spot in the room, and say whole sentences. A
voiceprint is the mean of its takes, so takes from one position cover only
that position: four takes from one spot measured 0.63 to 0.75 against each
other, which straddles the 0.70 accept threshold, and eight to twelve from
varied positions is what closes that gap.

The wake confirmation is intentionally fail-closed. Until someone is
enrolled, no satellite answers: a Voice PE's button press takes the same
speaker check as a spoken wake, and each refusal is journalled as a
`wake_rejected` with reason `unknown_speaker`, which the review UI shows.
