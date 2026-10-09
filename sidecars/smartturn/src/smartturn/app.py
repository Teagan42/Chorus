"""The HTTP contract. The model sits behind Turn so this is testable without it."""

import math
from typing import Protocol

import numpy as np
from fastapi import FastAPI, HTTPException, Request
from fastapi.concurrency import run_in_threadpool

from smartturn.model import SAMPLE_RATE, THRESHOLD

# The satellite's format, fixed by the device (internal/bridge): SAMPLE_RATE,
# signed 16-bit, mono, little-endian, no header.
CONTENT_TYPE = "audio/pcm"

# A tenth of a second is under a syllable: nothing the model could call a
# turn, so the refusal belongs at the contract rather than in a verdict.
MIN_SAMPLES = SAMPLE_RATE // 10


class Turn(Protocol):
    """The turn so far in, how likely it is over out. The ONNX model fills it;
    tests use a fake."""

    model: str

    def probability(self, samples: np.ndarray) -> float: ...


def pcm_to_samples(body: bytes) -> np.ndarray:
    """Little-endian int16 to float32 in [-1, 1), which is what the model was trained on."""
    return np.frombuffer(body, dtype="<i2").astype(np.float32) / 32768.0


def create_app(turn: Turn) -> FastAPI:
    app = FastAPI(title="smartturn", docs_url=None, redoc_url=None)

    @app.get("/health")
    async def health() -> dict:
        return {"status": "ok", "model": turn.model}

    @app.post("/v1/turn")
    async def judge(request: Request) -> dict:
        # A WAV sent by mistake would be judged with its header as audio and
        # nobody would notice, so the label is checked rather than the bytes
        # sniffed.
        content_type = request.headers.get("content-type", "").split(";")[0].strip()
        if content_type != CONTENT_TYPE:
            raise HTTPException(415, f"content type {content_type!r}; send {CONTENT_TYPE}")
        body = await request.body()
        if not body:
            raise HTTPException(400, "no audio")
        if len(body) % 2:
            raise HTTPException(400, f"{len(body)} bytes is not whole 16-bit samples")
        samples = pcm_to_samples(body)
        if len(samples) < MIN_SAMPLES:
            raise HTTPException(
                400, f"{len(samples)} samples is under the {MIN_SAMPLES}-sample minimum"
            )
        # Synchronous and tens of milliseconds on a CPU; off the event loop
        # so a health check still answers meanwhile.
        p = await run_in_threadpool(turn.probability, samples)
        if not math.isfinite(p) or not 0.0 <= p <= 1.0:
            raise HTTPException(500, f"model answered {p}, not a probability")
        return {"complete": p > THRESHOLD, "probability": p, "model": turn.model}

    return app
