"""The real weights on the real GPU, when they are on disk. Skipped otherwise,
so `uv run pytest` stays hermetic by default (CONTRIBUTING §1).

    uv run --project sidecars/kokoromlx kokoromlx --download-only \\
        --model-dir .models/kokoromlx
    KOKOROMLX_MODEL_DIR=.models/kokoromlx \\
        uv run --project sidecars/kokoromlx pytest sidecars/kokoromlx

Apple Silicon only: MLX is Metal, and there is no CPU fallback to skip to.
"""

import os
import time
from pathlib import Path

import numpy as np
import pytest
from fastapi.testclient import TestClient

from kokoromlx.app import create_app
from kokoromlx.voice import DEFAULT_VOICE, MODEL, SAMPLE_RATE, MlxVoice

MODEL_DIR = os.environ.get("KOKOROMLX_MODEL_DIR", "")

# What a clause may cost, as a multiple of the audio it renders. Measured at
# 0.11-0.14 on an M-series Mac, 2026-10-13; the bar is loose enough to pass on
# a busy machine and still fail if a render has fallen back to the CPU.
MAX_RTF = 0.5


@pytest.fixture(scope="module")
def voice() -> MlxVoice:
    if not MODEL_DIR:
        pytest.skip("no weights: set KOKOROMLX_MODEL_DIR to a --download-only directory")
    if not (Path(MODEL_DIR) / "kokoro-v1_0.safetensors").is_file():
        pytest.fail(f"KOKOROMLX_MODEL_DIR={MODEL_DIR} does not hold the weights")
    v = MlxVoice(MODEL_DIR)
    v.load()
    return v


# The rate the Go client's resampler is wired to, and audio that is actually
# there: a silent render would pass every shape assertion.
#
# verifies SPEC §10
def test_a_clause_renders_as_speech(voice: MlxVoice) -> None:
    audio = voice.render("Turn off the kitchen lights.", DEFAULT_VOICE, 1.0)
    assert voice.sample_rate == SAMPLE_RATE
    assert audio.dtype == np.float32
    seconds = len(audio) / SAMPLE_RATE
    assert 1.0 < seconds < 4.0, f"{seconds:.2f}s for a six-word clause"
    assert 0.01 < float(np.abs(audio).max()) <= 1.0


# Why this sidecar exists: a clause renders in a fraction of its own length,
# which the containerised CPU build does not manage (ADR-0092).
def test_a_render_is_faster_than_the_speech(voice: MlxVoice) -> None:
    voice.render("Warm.", DEFAULT_VOICE, 1.0)
    clause = "I set a timer for ten minutes, and the kitchen one has four left."
    start = time.monotonic()
    audio = voice.render(clause, DEFAULT_VOICE, 1.0)
    rtf = (time.monotonic() - start) / (len(audio) / SAMPLE_RATE)
    assert rtf < MAX_RTF, f"real-time factor {rtf:.2f}"


# Speed is a multiplier, not a no-op: a sidecar that ignored it would sound
# right and record a speed the journal cannot explain.
def test_speed_shortens_the_render(voice: MlxVoice) -> None:
    clause = "The patio umbrella is probably worth closing before you leave."
    slow = len(voice.render(clause, DEFAULT_VOICE, 0.8))
    fast = len(voice.render(clause, DEFAULT_VOICE, 1.4))
    assert fast < slow


# All 54, with the client's default among them: the fetch keeps only the
# safetensors copies and a pattern that missed them would serve none.
def test_every_pinned_voice_is_there(voice: MlxVoice) -> None:
    names = voice.voices()
    assert len(names) == 54
    assert DEFAULT_VOICE in names


# End to end over HTTP, as the daemon asks: the bytes the Go client decodes.
#
# verifies SPEC §10
def test_the_endpoint_answers_pcm(voice: MlxVoice) -> None:
    client = TestClient(create_app(voice))
    r = client.post(
        "/v1/audio/speech",
        json={
            "model": MODEL,
            "input": "Okay.",
            "voice": DEFAULT_VOICE,
            "response_format": "pcm",
            "speed": 1.0,
            "stream": False,
        },
    )
    assert r.status_code == 200, r.text
    samples = np.frombuffer(r.content, dtype="<i2")
    assert len(samples) > SAMPLE_RATE // 4
    assert int(np.abs(samples).max()) > 300
