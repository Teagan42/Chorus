# Retraining the wake word

The satellites wake on the household's own "Hey Eddie", a
[microWakeWord](https://github.com/kahrendt/microWakeWord) model that
ESPHome's `micro_wake_word` runs on the device (SPEC §13). Every wake the
host then rejects becomes a hard negative in the review UI's wake corpus:
a cough, the television, a voice that was nobody's (SPEC §9.3, ADR-0058).
This guide turns that corpus into a new model, checks it is better than the
one the satellites run, and puts it on them. Why it works this way is
[ADR-0078](adr/0078-the-wake-word-is-retrained-from-the-corpus-in-a-pinned-trainer-and-ships-only-when-it-beats-the-deployed-one.md).

No model of "Hey Eddie" has been trained by this pipeline yet. Everything
up to training is tested. The trainer has run end to end on toy data
outside its container, but the container itself has not been built or run
in CI, so the first household to run it is the first test of it.

## What you need

- The review UI, and the blob store it reads (`CHORUS_BLOB_DIR`).
- `uv`, for Kokoro, which `task wake:positives` fetches once, as
  `task canned:voice` does.
- Docker. Training uses a GPU if `nvidia-smi` finds one and the NVIDIA
  container toolkit is installed. Without one it trains on the CPU, which
  takes hours for the default 10,000 steps. Set `WAKE_DOCKER_GPUS=` to force
  the CPU, or to other `docker run` flags to pick a GPU.
- Network access for the image build and for microWakeWord's own negative
  datasets, which come from Hugging Face on the first `task wake:train`.

Everything is written under `.wake/` in the repository. It is gitignored,
and it holds recordings of your home: never commit it (CONTRIBUTING §7).

## 1. Review, then export the corpus

Listen to the held wakes in the review UI first. A wake that failed only
the speaker check is held until someone confirms it, because it is likely
the wake word from a guest (ADR-0058). Discard anything that was really
the wake word. Then download the corpus from **Export → Wake corpus**
(`/export/wake-negatives.jsonl`) and lay it out:

```sh norun
task wake:corpus -- -negatives ~/Downloads/chorus-wake-negatives.jsonl
```

This writes `.wake/corpus/negatives/<confirmed|auto>/<split>/*.wav`, and
`.wake/corpus/manifest.json`. The manifest names the journal row behind
each clip, and every row left out with the reason: `audio_gone`,
`audio_missing`, `not_hard_negative` or `held_unconfirmed`. By default a
tenth of the hours is held out for validation and a tenth for testing.
`-validation` and `-testing` change that, and `-channel second` trains on
the XMOS's second output instead of the processed one.

## 2. Speak the positives

```sh norun
task wake:positives
```

Kokoro says "Hey Eddie" three ways, at three speeds, in every English voice
and in 60 blends of two voices. That is about 800 clips, and on a CPU it
takes a while: roughly three seconds a clip on a laptop. `-- --blends 120`
makes more speakers. Whole voices are held out for testing.

Optionally, put room impulse responses (WAV) in `.wake/augment/rirs/` and
more background noise in `.wake/augment/background/`. Positives are
always mixed over your own training negatives. Without impulse responses
they are not reverberated, and the trainer says so.

## 3. Train

```sh norun
task wake:train
```

This builds the trainer's image, fetches microWakeWord's negative datasets
into `.wake/negative_datasets/` (once), makes spectrograms, and trains.
It leaves three files in `.wake/model/`:

- `hey_eddie.tflite`, the quantised streaming model;
- `hey_eddie.json`, its manifest;
- `provenance.json`, which records the corpus and positives it was trained
  on by digest, microWakeWord's commit, and every training parameter.

A model that ESPHome or the device would refuse is not written. To change
the hyperparameters, the feature-set weights or the manifest's cutoff, edit
`internal/tools/waketrain/training.yaml`. `-- --resume` continues an
interrupted run from its checkpoint.

## 4. Evaluate against the deployed model

```sh norun
task wake:evaluate
```

This scores the new model and the one the satellites load now, which is
the URL in `esphome/packages/chorus-bridge.yaml`. Both are scored on the
held-out testing split, by the rule the device wakes by. It reports false
accepts an hour on your held-out hard negatives, and false rejects on the
held-out positives, into `.wake/evaluation.json`. The task fails unless the
new model is better: no more false accepts, no more misses, and fewer of
one or the other. `-- --tolerance 0.02` allows two points more misses in
exchange for fewer false accepts. `-- --deployed none` scores a first model
alone.

The false-accept rate is over sounds the deployed model already woke on,
so it is far higher than your rooms will see. It compares the two models.
`evaluation.json` also sweeps the cutoff. If a different trade suits the
house better, change `probability_cutoff` in the manifest, check it with
`task wake:validate -- .wake/model/hey_eddie.json`, and evaluate again.

## 5. Deploy

ESPHome builds the model into the firmware at compile time from the
manifest URL in `esphome/packages/chorus-bridge.yaml`, and fetches the
`.tflite` the manifest names from beside it. The household's model lives
in Home Assistant's `www` folder, which Home Assistant serves under
`/local/`.

Give each model its own folder rather than overwriting the last one, so
rolling back is a URL change. Copy the model into a new folder:

```sh norun
# On the Home Assistant host; the date names this model.
mkdir -p /config/www/hey_eddie/2026-10-12
cp .wake/model/hey_eddie.json .wake/model/hey_eddie.tflite /config/www/hey_eddie/2026-10-12/
```

Point the satellites at it in your own device config, without editing the
shared package. ESPHome's `!extend` overrides a package's list item by its
`id`, so in `esphome/satellite1.yaml` (and `esphome/voice-pe.yaml`) add:

```yaml
micro_wake_word:
  models:
    - id: !extend wake_model
      model: https://<your-home-assistant>/local/hey_eddie/2026-10-12/hey_eddie.json
```

Then check the config, compile and flash each satellite:

```sh norun
task firmware:config
task firmware:compile
task firmware:upload
```

`firmware:config` downloads the manifest and refuses one it cannot read.
The ring's **Wake word sensitivity** select is relative to the manifest's
cutoff, so its three levels carry over to the new model. Keep
`provenance.json` and `evaluation.json` beside the model, so you can tell
later what each folder was trained on.

The `!extend` override has not been tried against this config. If
`firmware:config` still shows the old URL, change the URL in
`esphome/packages/chorus-bridge.yaml` itself.

## Rolling back

Point `model:` back at the previous folder, then run `task firmware:compile`
and `task firmware:upload` again. Nothing on the host changes with the wake
model: chorusd reads the name each wake frame carries, and the model is
swappable without touching anything else (SPEC §13).

## Running the steps' own tests

```sh norun
task wake:test        # the Python side, with stand-ins for Kokoro and TFLite
go test ./internal/tools/wakecorpus ./internal/harvest
```
