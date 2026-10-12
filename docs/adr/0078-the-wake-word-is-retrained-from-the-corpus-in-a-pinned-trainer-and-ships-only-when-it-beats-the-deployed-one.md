# 0078. The wake word is retrained from the corpus in a pinned trainer, and ships only when it beats the deployed one

- **Status:** accepted
- **Source:** SPEC §9.3, §13, §14 · ADR-0058, ADR-0060, ADR-0069, ADR-0072 · commits
  `feat(harvest): name the wake word on each wake corpus row`,
  `feat(tools): lay the wake corpus out as microWakeWord reads it`,
  `feat(tools): retrain the wake word with microWakeWord in a container`

The satellites wake on the household's own "Hey Eddie", a microWakeWord
model (SPEC §13), and every wake stage two rejects becomes a hard negative
in its own corpus (ADR-0058). Nothing trained on that corpus. Now four
steps do, each reading what the one before left in `.wake/`, and none of
them runs in `task test` or `task check`. The guide is
[docs/wake-retraining.md](../wake-retraining.md).

```go
import "github.com/teagan42/chorus/internal/harvest"

// The corpus step asks again what the export already asked: is this row a
// hard negative? An arbitrated wake, a press and an undecoded wake are not.
func trainable(reason, wakeWord string) bool {
	return harvest.HardNegative(map[string]string{"reason": reason, "wake_word": wakeWord})
}
```

## The corpus is the review UI's export, laid out again

`task wake:corpus` runs `internal/tools/wakecorpus`. It reads
`/export/wake-negatives.jsonl` and the blob store, and writes each row's
clip as a 16 kHz mono WAV under `negatives/<set>/<split>/`. The export, not
the journal, is the input, because the review UI is where ADR-0058's
shipping rule and the reviewer's verdicts live. Reading the journal again
would mean a second copy of that rule. Each row is still checked against
the rules a hard negative must pass: `HardNegative`, which needed the
export to carry `wake_word` (ADR-0072), and the held-guest rule. A row
that fails is skipped and named. So an export from an older build cannot
put a real wake word in front of the trainer. A clip retention dropped, or
one missing from the store, is skipped and named too. A file that is not
the wake corpus fails the run, as does a clip that is not 16-bit audio.

The manifest names the journal row behind every clip, the export's digest,
and every row left out with the reason. That is the provenance SPEC §9.3's
corpus carries into a model. The trainer copies it into the model's
`provenance.json` by digest. The model's manifest cannot hold it, because
ESPHome's schema refuses keys it does not name.

**Two sets, by who said so.** A clip a reviewer confirmed is in
`confirmed`, and one only the rule shipped is in `auto`. Each set is its
own feature set with its own weight, so a person's word counts for more
(ADR-0058).

**The split is by satellite and hour.** The key is the first eight bytes
of the SHA-256 of the satellite and the UTC hour of the rejection, read as
a fraction. One programme's run of false wakes stays on one side of the
split, and a larger held-out fraction only moves hours into it. This is
the dataset export's rule (ADR-0071), but keyed by hour: a device log is
one long conversation.

**The processed channel by default.** The device's `micro_wake_word`
listens to channel 0 (`esphome/packages/chorus-bridge.yaml`), so that is
what the model is trained on. `-channel second` trains on the XMOS's second
output, as SPEC §9.3 allows. A rejection from a one-channel device is then
left out.

## Positives are Kokoro, in held-out voices

microWakeWord's notebook speaks the wake word with Piper. Here
`task wake:positives` speaks it with Kokoro, which chorusd already uses and
`task canned:voice` already fetches. It says three phrasings at three
speeds, in every English voice and in blends of two voices' style vectors,
which are speakers Kokoro does not ship. Whole voices are held out by a
hash of their names, so the false-reject rate is measured on speakers the
model never heard. Blends are made only of training voices.

Room impulse responses and background noise are added when spectrograms
are made, as microWakeWord does. The background is the household's own
training negatives, plus anything in `.wake/augment/background`. So a
positive is heard over the rooms the satellites are in. Impulse responses
are the household's to supply in `.wake/augment/rirs`. The notebook's MIT
set is on Hugging Face and is not fetched.

## The trainer is a container at a pinned commit

`internal/tools/waketrain/Dockerfile` checks microWakeWord out at commit
`4665173c` (main on 2026-07-06, "Pin dependencies"), whose notebook
`training.yaml` starts from. It is put on `PYTHONPATH` rather than
installed: its `audio` and `layers` packages have no `__init__.py`, so a
plain install leaves them out, and the notebook's editable install is what
the code expects. microWakeWord names its dependencies without versions, so
`requirements.txt` resolves them as of that commit's date
(`--exclude-newer 2026-07-07`) and the image installs exactly those. Two
constraints are added by hand, each found by running the trainer:
`datasets` stays below 4, which decodes audio only through torchcodec and
torch, and `tensorboard` is listed, because TensorFlow 2.21 no longer
brings the `tf.summary` microWakeWord logs with. TensorFlow comes with
CUDA's wheels. `--gpus all` trains on a GPU, and without one TensorFlow
uses the CPU, which takes hours rather than minutes.
microWakeWord's own negative spectrograms (speech, dinner party, no speech)
are fetched from Hugging Face at train time. That source is unpinned, so
each zip's digest is recorded in the model's provenance.

`training.yaml` is checked in, and it is checked before any training: the
manifest it would produce must pass the firmware's checks, and its feature
step must match the features. It renders into microWakeWord's
`training_parameters.yaml` with a feature set only for data that exists.
Weights are picked first for the fewest false accepts an hour on the
ambient sets. The household's held-out negatives are one of those sets.

## A model ships only when it is better

The trained model is exported only if ESPHome 2026.7.2 and the device would
load it. The manifest is checked against `MANIFEST_SCHEMA_V2`, with
`minimum_esphome_version` no newer than the ESPHome the firmware is built
with. The `.tflite` must be a TFLite flatbuffer, with int8 input of shape
`[1, stride, 40]` and a uint8 `[1, 1]` output, as `streaming_model.cpp`
requires. Chorus adds one rule: the quantised cutoff must be at least 8,
because the sensitivity select lowers it by 8 in a byte.

`task wake:evaluate` scores the model and the deployed one on the testing
split, using the rule the device wakes by. That is a replay of
`micro_wake_word`'s sliding window, cutoff and one-second cool-down, not a
mean probability. It counts false accepts an hour over the held-out hard
negatives, and false rejects over the held-out positives. The deployed
model is fetched from the URL the satellites' package names. The candidate
ships only if it has no more false accepts, misses no more positives
beyond a tolerance (0 by default), and is strictly better on one of the
two. Otherwise the task fails. The report sweeps the cutoff, so a household
can choose its own trade.

## Alternatives rejected

- **Read the journal directly in the corpus step.** It would give the step
  the raw `wake_word` with no export change. But the reviewer's verdicts and
  ADR-0058's rule live with the review UI. A second reading of them would
  drift, and the export is already the format trainers read.
- **Piper, as the notebook does.** It is a second TTS to fetch and pin, and
  Kokoro's voices are already the household's. Piper's generator also
  mixes speakers, but Kokoro's style vectors blend just as well.
- **Train in the uv workspace.** TensorFlow and CUDA in `uv.lock` would sit
  in every sidecar's resolution, as torch would have (`smartturn:corpus`
  keeps it out the same way). microWakeWord's notebook also targets 3.10
  or 3.11, and the workspace is 3.13.
- **Score by validation accuracy.** It says nothing about how the device
  fires. A model can gain accuracy and still wake more often under the
  sliding window.
- **Ship whatever trains.** A retrain on a few dozen household clips can
  overfit, and the household would find out in their kitchen.

## Forecloses

- **The clips are post-wake audio.** The wire carries no pre-roll
  (`bridge.TypeWake` is the word alone, SPEC §9.3). So a rejected wake's
  clip is the first utterance after the wake fired, not the sound that
  fired it. That is the television's dialogue, the dishwasher, the room
  after a cough: the household's false-trigger acoustics, but not the
  trigger itself. Once the firmware sends a pre-roll, the corpus gets the
  trigger without changing format.
- **False accepts an hour on hard negatives overstate a real room.** The
  held-out negatives are what the deployed model already woke on. The
  number compares two models and forecasts neither.
- **The held-out positives are synthetic.** Real household recordings of
  the wake word would measure false rejects better, and nothing collects
  them: false rejects are uncapturable without the near-miss buffer
  (SPEC §9.4).
- **`.wake/` holds household audio.** It is gitignored, as `blobs/` is, and
  never committed (CONTRIBUTING §7). Retention does not reach it, so a clip
  a reviewer deleted stays there until the corpus is laid out again.
- **The trainer has not run in its container.** Docker could not build
  the image where this was written, and no model of "Hey Eddie" has been
  trained by it. The steps up to training are tested hermetically. The
  trainer itself was run once outside the container, on CPU, with the pins
  in `requirements.txt` and microWakeWord at its commit. On 288 Kokoro
  positives and 48 synthetic negatives it made spectrograms, trained 40
  steps, exported a model the tensor checks passed, and scored it against
  ESPHome's stock `okay_nabu`. The scorer was checked on that stock model
  too: it fired on 7 of 12 Kokoro "Okay Nabu" clips and none of 12
  "Hey Eddie" ones.
