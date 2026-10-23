# 0098. A satellite names its own speaker accept level and click ceiling

- **Status:** accepted · refines [ADR-0084](0084-the-accept-threshold-is-configurable-because-a-front-ends-gain-compresses-every-score.md) and [ADR-0090](0090-an-impulse-is-told-from-a-voice-by-its-shape-not-its-length.md)
- **Source:** SPEC §4.5, §5, §9.3 · ADR-0083, ADR-0084, ADR-0088, ADR-0090, ADR-0096

ADR-0084 made the speaker accept level configurable, and global, because the
resolver was shared by every link; it foreclosed a household that mixes a
close-mic satellite with a far-field one, which has to pick one level and
admits strangers on one of them. ADR-0090's click ceiling, 22 dB of crest,
was measured in one room on one front end and is global too. ADR-0083 and
ADR-0088 already moved the speech level per satellite, and ADR-0096 put its
echo threshold there from the start. These two follow.

`devices.yaml` takes, per satellite, `speaker_accept` (a cosine, 0 to 1
exclusive) and `max_crest` (dB, over 3 and at most 60). Absent is zero,
and zero is the house's: `CHORUS_SPEAKER_ACCEPT` or `identity.DefaultAccept`
for the first, `listen.DefaultMaxCrest` for the second. Out of range is
refused at load, naming the field.

```go
import "github.com/teagan42/chorus/internal/config"

// The Voice PE in the office, whose far-field audio compresses every score.
var office = config.Satellite{Name: "office", SpeakerAccept: 0.50, MaxCrest: 24}
```

## How it is built

The resolver is an embedder, the household and the thresholds, and builds
without touching the network, so chorusd builds one per satellite that
names a level, at startup, over the same embedder and household; the rest
share the house's. A model mismatch with the household would already have
refused the house's resolver, so a satellite's cannot fail differently.
`listen.Impulsive` takes the ceiling as an argument and `listen.Config`
carries it per link, so the guess asked in a pause (ADR-0095) judges a click
as the confirmed turn does. A `speaker_accept` with nothing identifying speakers
is logged as having no effect, since it reads as if it does something.

## Alternatives rejected

- **A voiceprint per satellite.** ADR-0082 records that takes from two
  satellites average into one centroid. That needs a change to
  `identities.yaml`'s schema and to enrollment, in `cmd/enroll` and on the
  Voices screen; this is
  the setting alone, which the measurements so far ask for.
- **Auto-calibrating the level from each satellite's journalled scores.**
  The scores that would tune it are the ones the level decides, so it
  drifts toward whatever got accepted. A number someone wrote down from
  their own room does not.

## Forecloses

The review UI's Voices screen still notes a clip "under the threshold"
against `identity.DefaultAccept`, not the satellite's level nor the house's
override, because it does not read `devices.yaml`.
