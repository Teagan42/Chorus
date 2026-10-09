"""The real model, when it is on disk. Skipped otherwise, so `uv run pytest`
stays hermetic by default (CONTRIBUTING §1).

    uv run smartturn --download-only --model-dir .models/smartturn
    SMARTTURN_MODEL_DIR=.models/smartturn SMARTTURN_CORPUS=/path/to/turns \
        uv run pytest sidecars/smartturn

The corpus is <dir>/complete/*.wav and <dir>/incomplete/*.wav at 16 kHz s16le
mono, never committed: the repo holds no real recordings (CONTRIBUTING §7).
"""

import os
import wave
from pathlib import Path

import numpy as np
import pytest

from smartturn.model import ASSET_NAME, ASSET_SHA256, SAMPLE_RATE, OnnxTurn, sha256

MODEL_DIR = os.environ.get("SMARTTURN_MODEL_DIR", "")
CORPUS = os.environ.get("SMARTTURN_CORPUS", "")


def read_wav(path: Path) -> np.ndarray:
    with wave.open(str(path), "rb") as w:
        if (w.getframerate(), w.getnchannels(), w.getsampwidth()) != (SAMPLE_RATE, 1, 2):
            raise ValueError(f"{path}: want {SAMPLE_RATE} Hz s16le mono")
        pcm = w.readframes(w.getnframes())
    return np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0


@pytest.fixture(scope="module")
def turn() -> OnnxTurn:
    if not MODEL_DIR or not (Path(MODEL_DIR) / ASSET_NAME).is_file():
        pytest.skip("no model: set SMARTTURN_MODEL_DIR to a directory holding the pinned asset")
    t = OnnxTurn(MODEL_DIR)
    t.load()
    return t


@pytest.fixture(scope="module")
def corpus() -> dict[str, list[Path]]:
    if not CORPUS:
        pytest.skip("no corpus: set SMARTTURN_CORPUS to <dir>/{complete,incomplete}/*.wav")
    out = {k: sorted((Path(CORPUS) / k).glob("*.wav")) for k in ("complete", "incomplete")}
    if not all(out.values()):
        pytest.skip(f"{CORPUS}: needs at least one complete and one incomplete turn")
    return out


# The file on disk is the one the sidecar was built against, or every
# verdict below is some other model's.
def test_the_model_on_disk_is_the_pinned_one(turn: OnnxTurn) -> None:
    assert sha256(Path(MODEL_DIR) / ASSET_NAME) == ASSET_SHA256


# The contract the endpointer is built on: a probability, and the same audio
# twice gives the same one. Synthetic, so it runs without a corpus.
#
# verifies SPEC §4.5
def test_the_model_answers_a_probability(turn: OnnxTurn) -> None:
    t = np.arange(int(1.5 * SAMPLE_RATE)) / SAMPLE_RATE
    hum = (0.2 * np.sin(2 * np.pi * 140 * t)).astype(np.float32)
    p = turn.probability(hum)
    assert 0.0 <= p <= 1.0
    assert turn.probability(hum) == p


# What makes it a turn model: finished turns score above unfinished ones on
# average. Means, not every take, because the model is wrong on some and the
# endpointer's silence fallback exists for exactly those (ADR-0036).
#
# verifies SPEC §4.5
def test_finished_turns_score_above_unfinished_ones(turn: OnnxTurn, corpus: dict) -> None:
    scores = {k: [turn.probability(read_wav(p)) for p in paths] for k, paths in corpus.items()}
    done, open_ = np.mean(scores["complete"]), np.mean(scores["incomplete"])
    assert done > open_, f"complete {done:.3f} <= incomplete {open_:.3f}"
