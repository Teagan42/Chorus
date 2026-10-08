# 0029. Serve TitaNet-L on ONNX Runtime, pinned by asset digest, with measured thresholds

- **Status:** accepted; supersedes the threshold and model-revision parts of [ADR-0025](0025-voiceprints-in-a-local-file-behind-a-minimal-embed-contract.md), whose file stays as written (CONTRIBUTING §3)
- **Source:** SPEC §5, §10, §13, §4.3

ADR-0025 put the speaker-ID model behind `POST /v1/embed` and left two
questions open: the checkpoint's Hub revision was `main`, and the thresholds
in `internal/identity` were SpeechBrain's defaults and a guess. The sidecar
it described never produced an embedding where this was written: the
checkpoint lives on Hugging Face, which that environment cannot reach, and
the torch build behind it weighed gigabytes for a model that is a hundred
megabytes. This record replaces the backend, closes both questions, and keeps
the contract: the Go client changes only in the model name it expects.

**The backend is ONNX Runtime through `sherpa-onnx`, not torch and
SpeechBrain.** The sidecar's Python dependencies go from 78 locked packages,
with CUDA libraries the sidecar never used on a CPU box, to 29, and the
installed environment from gigabytes to about 140 MB. On a dev-box CPU an
utterance of 2–3 s embeds in 60–170 ms with four threads, inside the barge-in
gate's ~300 ms budget (SPEC §4.3) with the STT partial still to come. The
wheel is the CPU build; a GPU host (SPEC §13) is a different wheel and a
compose change, not a flag, so the image stays one thing. Two packaging facts
are pinned in the sidecar's manifest because they are not obvious from the
lockfile: the cp314 wheel on PyPI ships without `libonnxruntime.so`, so the
workspace is bounded to Python 3.13; and `uv` resolves `sherpa-onnx` from a
distribution that does not declare `sherpa-onnx-core`, which is where that
library lives, so the core package is pinned by hand.

**The model is one release asset, pinned by URL and sha256.**
`nemo_en_titanet_large.onnx` from sherpa-onnx's `speaker-recongition-models`
release (upstream's spelling), 101,405,493 bytes,
`d51abcf31717ef28162f26acb9d44dd4127c3d44c9b8624f699f3425daca8e77`. A release
asset does not move the way a Hub branch does, and the digest is checked at
image build and at every boot: a replaced, truncated, or stale file is fetched
again or refused, never served. This closes ADR-0025's "pin it before any
household is enrolled" with no Hub account needed. A household enrolled
against these bytes stays comparable across rebuilds until someone changes
the pin on purpose, which the identities file then refuses by model name.

**TitaNet-L rather than ECAPA-TDNN.** SPEC §10 lists both. TitaNet-L is the
one sherpa-onnx publishes as a release asset at 192 dims and 16 kHz, so the
width every enrolled centroid assumes is unchanged and the device's audio is
not resampled. The release's other English exports that could be verified
from here (WeSpeaker ResNet34, TitaNet-S, SpeakerNet) are not models the
spec names, and no ECAPA-TDNN asset was found under it.

**The thresholds are measured.** `identity.DefaultAccept` is 0.70 and
`identity.DefaultMargin` is 0.10, from the models tier's `-speakerid-wavs`
pass on 2026-10-08 — a real enrollment through `internal/identity` (three
takes per person), identification of the held-out takes, and guests scored
against every centroid — with the model above on a corpus the environment
could reach:

- sr-data (sherpa-onnx's own speaker-ID test set, Chinese sentences): three
  speakers with four or five takes each, enrolled; the 3D-Speaker samples it
  carries as four guests of one or two takes.
- AudioMNIST (spoken digits, 48 kHz, 60 speakers, CC BY 4.0): 30 speakers,
  all twelve women and eighteen men. Each take is five distinct digit
  recordings of one speaker concatenated and resampled to 16 kHz, about 3 s;
  25 speakers enrolled with six takes, five held back as guests with three.

In all: 28 enrolled, 80 held-out takes, 9 guests with 21 takes.

| Distribution | n | min | p05 | p50 | p95 | max |
|---|---|---|---|---|---|---|
| held-out take vs own centroid | 80 | 0.659 | 0.717 | 0.801 | 0.879 | 0.890 |
| held-out take vs other centroids | 2160 | −0.125 | −0.001 | 0.168 | 0.464 | 0.676 |
| guest take vs every centroid | 588 | −0.240 | −0.038 | 0.138 | 0.457 | 0.600 |
| best minus runner-up, correct top-1 | 80 | 0.138 | 0.182 | 0.335 | 0.526 | 0.590 |

Top-1 was 80/80 before any threshold. The accept was set so that no
cross-speaker trial passes: at 0.70 there are 0 false accepts in 2748 and 2
false rejects in 80 (both one speaker, at 0.659 and 0.699); at 0.65 there are
2 false accepts and none rejected; at 0.75, 15 rejected. The headroom above
the worst pair (0.676, two women reading the same digits) is thin, and a
false reject is a guest turn while a false accept hands over a person's
context, so the thin side is the safe one. The margin sits under the smallest
lead a correct identification had (0.138), so nothing in this corpus is
ambiguous while two voices closer than that still fall to guest. With both,
the pass identifies 78 of 80, rejects 2 to guest, misattributes none, and
rejects all 21 guest takes. ADR-0025's 0.25 accepted 6 of the 21.

What the corpus does not say: it is 37 speakers, not a household; the digit
takes share their text across speakers, which pushes cross-speaker scores up
and makes the accept conservative; and none of it is satellite audio through
the device's microphone path. The numbers are a floor for a household, not
its tuning. The models tier prints this table on every run and a household
measures itself by pointing `-speakerid-wavs` at its own recordings.

## Alternatives rejected

Keep torch and record the Hub commit. It needs Hub access to record and to
build, which the environment did not have, and leaves a 5 GB image for a 100
MB model.

ONNX Runtime directly, with our own feature extraction. The export expects
the exact filterbank front end NeMo trained it with; sherpa-onnx carries that
front end matched to each export it publishes, and getting it wrong does not
fail, it embeds noise.

A WeSpeaker or 3D-Speaker ECAPA export, to keep ADR-0025's model family.
SPEC §10 names ECAPA-TDNN and TitaNet-L, not those trainings, and the
TitaNet export is the one that keeps 192 dims and 16 kHz.

A larger public corpus for the thresholds. VoxCeleb and LibriSpeech were
unreachable from here; the honest move was a small corpus with the sizes
stated rather than a claim about one not run.

## Forecloses

GPU inference through this image: the runtime wheel is the CPU build. The
cost measured here makes that moot for a household; a GPU host would swap the
wheel and rebuild, and nothing in the contract would change.

The thresholds are per model. Changing the pin re-measures them and
re-enrolls the household, which the identities file enforces by refusing the
new model name.

The Docker build is specified but was not run where this was written, which
has no daemon. What ran: `uv sync --all-packages` from the lockfile,
`speakerid --download-only` against the live release URL with the digest
verified, the sidecar served from that venv, and the Go models tier against
it. The Dockerfile performs those same steps.
