"""The real model, when it is on disk. Skipped otherwise, so `uv run pytest`
stays hermetic by default (CONTRIBUTING §1).

    uv run speakerid --download-only --model-dir .models/speakerid
    SPEAKERID_MODEL_DIR=.models/speakerid uv run pytest sidecars/speakerid

`task test:sidecars` does both. Without SPEAKERID_CORPUS the voices are the
household fixture's: Teagan, Alice and Alan asking for the weather, the
garage door, Zeppelin and an oven timer, in the Kokoro voices that speak
them (internal/reviewui/household/voice.json). SPEAKERID_CORPUS points at
<dir>/<speaker>/*.wav at 16 kHz s16le mono instead, and is never committed:
the repo holds no real recordings (CONTRIBUTING §7).
"""

import json
import os
import wave
from pathlib import Path

import numpy as np
import pytest

from speakerid.embedder import ASSET_NAME, ASSET_SHA256, DIM, SAMPLE_RATE, OnnxEmbedder, sha256

MODEL_DIR = os.environ.get("SPEAKERID_MODEL_DIR", "")
CORPUS = os.environ.get("SPEAKERID_CORPUS", "")


# The fixture the review UI demo plays, which is device-format PCM with who
# said each clip beside it.
HOUSEHOLD = Path(__file__).resolve().parents[3] / "internal" / "reviewui" / "household"


def read_wav(path: Path) -> np.ndarray:
    with wave.open(str(path), "rb") as w:
        if (w.getframerate(), w.getnchannels(), w.getsampwidth()) != (SAMPLE_RATE, 1, 2):
            raise ValueError(f"{path}: want {SAMPLE_RATE} Hz s16le mono")
        pcm = w.readframes(w.getnframes())
    return np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0


@pytest.fixture(scope="module")
def embedder() -> OnnxEmbedder:
    if not MODEL_DIR:
        pytest.skip("no model: set SPEAKERID_MODEL_DIR to a directory holding the pinned asset")
    # Set and empty is a broken fetch, not an opted-out machine: CI sets it,
    # and a skip there would pass with the model never run.
    if not (Path(MODEL_DIR) / ASSET_NAME).is_file():
        pytest.fail(f"SPEAKERID_MODEL_DIR={MODEL_DIR} does not hold {ASSET_NAME}")
    e = OnnxEmbedder(MODEL_DIR)
    e.load()
    return e


def read_pcm(path: Path) -> np.ndarray:
    return np.fromfile(path, dtype="<i2").astype(np.float32) / 32768.0


def household() -> dict[str, list[np.ndarray]]:
    """What each person said to a satellite. The wake clips are the
    dishwasher and a podcast, which are nobody's voice."""
    voice = json.loads((HOUSEHOLD / "voice.json").read_text())
    by_speaker: dict[str, list[np.ndarray]] = {}
    for clip in voice["clips"]:
        if clip["ref"].startswith("mic/"):
            path = HOUSEHOLD / "audio" / f"{clip['ref']}.pcm"
            by_speaker.setdefault(clip["who"], []).append(read_pcm(path))
    return by_speaker


@pytest.fixture(scope="module")
def corpus() -> dict[str, list[np.ndarray]]:
    """Two speakers with two utterances each is the least that says anything."""
    if CORPUS:
        by_speaker = {
            d.name: [read_wav(p) for p in sorted(d.glob("*.wav"))]
            for d in sorted(Path(CORPUS).iterdir())
            if d.is_dir()
        }
    else:
        by_speaker = household()
    by_speaker = {k: v for k, v in by_speaker.items() if len(v) >= 2}
    if len(by_speaker) < 2:
        pytest.skip(f"{CORPUS or HOUSEHOLD}: fewer than two speakers with two utterances")
    return by_speaker


# The file on disk is the one the sidecar was built against, or every
# number below is about some other model.
def test_the_model_on_disk_is_the_pinned_one(embedder: OnnxEmbedder) -> None:
    assert sha256(Path(MODEL_DIR) / ASSET_NAME) == ASSET_SHA256


# The contract the matcher is built on: the declared width, unit length,
# finite everywhere, and the same audio twice gives the same vector.
#
# verifies SPEC §10
def test_the_model_embeds_to_the_contract(embedder: OnnxEmbedder, corpus: dict) -> None:
    samples = next(iter(corpus.values()))[0]
    v = embedder.embed(samples)
    assert v.shape == (DIM,) and v.dtype == np.float32
    assert np.isfinite(v).all()
    assert float(np.linalg.norm(v)) == pytest.approx(1.0, abs=1e-5)
    assert np.array_equal(v, embedder.embed(samples))


# What makes it a speaker model rather than an audio hash: a voice's takes
# agree with each other more than with any other voice's. Means over takes,
# not single pairs, because a centroid is a mean and a single pair of short
# takes can land anywhere (measured: the models tier reports the spread).
#
# verifies SPEC §5
def test_one_voice_scores_above_two(embedder: OnnxEmbedder, corpus: dict) -> None:
    vecs = {spk: [embedder.embed(x) for x in takes] for spk, takes in corpus.items()}
    for spk, own in vecs.items():
        same = np.mean([own[i] @ own[j] for i in range(len(own)) for j in range(i + 1, len(own))])
        for other, theirs in vecs.items():
            if other == spk:
                continue
            cross = np.mean([a @ b for a in own for b in theirs])
            assert same > cross, f"{spk}: own takes {same:.3f} <= {other}'s takes {cross:.3f}"
