# 0032. Record the ear and the voice as named versions, one string each

- **Status:** accepted
- **Source:** SPEC §8 · commits `743a7f7`, `e068f3b`, `0daab62`

SPEC §8 says every event carries the versions in effect, and until these
commits `journal.Versions` held exactly the three the clause names: model,
prompt, tool schema. All three attribute a *completion*. Nothing recorded which
STT model produced an `utterance_transcribed` or which TTS voice a
`speech_truncated` cut, so an eval could not separate two sidecar builds'
transcripts and a replay could not say what the household actually heard. Two
slots are added, stamped on every event like the other three.

```go
import "github.com/teagan42/chorus/internal/journal"

// The ear is a model id; the voice is model/voice as one string. Neither is
// hashed: both are already stable names, unlike a prompt body.
func cascade() journal.Versions {
	return journal.Versions{STT: "istupakov/parakeet-tdt-0.6b-v2-onnx", TTS: "kokoro/af_heart"}
}
```

**TTS is one string, `model/voice`, not two fields.** A voice id is a name
inside one model's namespace: Kokoro's `af_heart` says nothing about any other
synthesiser, and a future voice chosen by speaker embedding has no id at all,
only a model. One slot means one nullable column, one `meta.versions` key in
the harvest export, and one thing a replay compares. The `/` is the same
separator the STT model id already carries, so a reader splits either with one
rule.

**The columns are nullable; the three before them are `NOT NULL`.** Migration
0002 runs on a table that may already hold a household's log. A row from before
it holds `NULL`, which reads as "not recorded"; a journal running without a
configured ear or voice writes `''`, which reads as "nothing was configured".
`PgStore` maps both to the empty string Go holds, and the column keeps the
difference for SQL. `NOT NULL DEFAULT ''` would have erased it.

**Neither slot gates a completion.** `Versions.complete()` still checks model,
prompt and tool schema only. A `model_completed` is attributable to its model
whether or not anything heard or spoke; a test journal and a text-only
deployment must still record one.

## Alternatives rejected

- *Record the STT and TTS identities as fields on the events that need them.*
  That is where the data is produced, but the schema's `requires_versions` is a
  per-event flag over a per-journal stamp, and the stamp is what replay and the
  harvester read. A field would have to be threaded through the listening and
  speaking children and would be absent from every event that was not one of
  theirs, so a `session_opened` could not say what the session was built with.
- *Hash the TTS configuration like the prompt.* The prompt is a body; a hash is
  how it gets a name. A model id and a voice id already are names, and a hash of
  them would hide which voice from the reviewer with no gain in precision.
- *Include the speed multiplier in the TTS string.* Speed changes what the
  household heard and so the `frames_played` at a cut, but it is a per-deployment
  setting on the same voice, not a different voice. Left out; see Forecloses.

## Forecloses

The TTS string is not structured, so a query by voice across models is a string
match on the suffix. If speed or another synthesis parameter must be attributed
later, it is a third slot or a suffix convention, with a migration either way.
