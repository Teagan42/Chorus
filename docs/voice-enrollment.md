# Enroll a household voice

Chorus accepts a spoken wake only when speaker ID matches an enrolled
household voice. This prevents television dialogue and other unknown voices
from opening a session. Until the first person is enrolled, spoken wakes are
silently rejected; an unknown speaker cannot be distinguished from the TV.

Record at least three clean, distinct takes as WAV files: 16 kHz, mono, signed
16-bit PCM. Use your own voice, with the satellite in its usual room if
possible. Do not use clips from the TV or the review journal as enrollment
takes.

Put those files in `voice-takes/` at the repository root, then enroll through
the one-shot Docker helper:

```sh
docker compose run --rm enroll add -id household -name "Household member" \
  -identities /var/lib/chorus/identity/identities.yaml \
  -wav /takes/take-1.wav -wav /takes/take-2.wav -wav /takes/take-3.wav
```

The helper uses the speaker-ID sidecar and writes the biometric voiceprint to a
private Docker volume. Restart chorusd to load it:

```sh
docker compose restart chorusd
```

The wake confirmation is intentionally fail-closed. Until enrollment is
complete, this Satellite1 has no push-to-talk button, so it will not answer a
spoken wake.
