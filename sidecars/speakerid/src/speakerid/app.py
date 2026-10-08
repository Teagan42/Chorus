"""The HTTP contract. The model sits behind Embedder so this is testable without it."""

from typing import Protocol

import numpy as np
from fastapi import FastAPI, HTTPException, Request
from fastapi.concurrency import run_in_threadpool

# The satellite's format, fixed by the device (internal/bridge): 16 kHz,
# signed 16-bit, mono, little-endian, no header.
SAMPLE_RATE = 16_000
CONTENT_TYPE = "audio/pcm"

# Below this the convolution stack sees fewer frames than its kernel and
# fails deep inside torch; the refusal belongs at the contract instead.
MIN_SAMPLES = SAMPLE_RATE // 10


class Embedder(Protocol):
    """One utterance in, one vector out. ECAPA fills it; tests use a fake."""

    model: str
    dim: int

    def embed(self, samples: np.ndarray) -> np.ndarray: ...


def pcm_to_samples(body: bytes) -> np.ndarray:
    """Little-endian int16 to float32 in [-1, 1), which is what the model was trained on."""
    return np.frombuffer(body, dtype="<i2").astype(np.float32) / 32768.0


def create_app(embedder: Embedder) -> FastAPI:
    app = FastAPI(title="speakerid", docs_url=None, redoc_url=None)

    @app.get("/health")
    async def health() -> dict:
        return {"status": "ok", "model": embedder.model, "dim": embedder.dim}

    @app.post("/v1/embed")
    async def embed(request: Request) -> dict:
        # A WAV sent by mistake would embed its header as audio and nobody
        # would notice, so the label is checked rather than the bytes sniffed.
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
        # The model is synchronous and takes tens of milliseconds on a GPU;
        # off the event loop so a health check still answers meanwhile.
        vec = await run_in_threadpool(embedder.embed, samples)
        return {"embedding": vec.tolist(), "dim": embedder.dim, "model": embedder.model}

    return app
