"""The HTTP contract: the part of Kokoro-FastAPI's OpenAI-compatible speech API
that internal/provider/kokoro speaks, so pointing KOKORO_URL here is the whole
change. The model sits behind Voice so this is testable without MLX.
"""

import io
import wave
from typing import Protocol

import numpy as np
from fastapi import FastAPI, HTTPException
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import Response
from pydantic import BaseModel

from kokoromlx.voice import DEFAULT_VOICE, MODEL

# Raw samples, as the Go client asks for: upstream's WAV declares a data chunk
# of 0xFFFFFFFF and a parser that trusts it reads past the buffer. The WAV this
# serves is honest, and is there for a person with curl, not for the daemon.
FORMATS = {"pcm": "audio/pcm", "wav": "audio/wav"}

# Upstream's range. Past either end the prosody falls apart rather than the
# speech getting faster or slower.
MIN_SPEED = 0.25
MAX_SPEED = 4.0

# Roughly two minutes of speech, which renders in about fifteen seconds -- half
# the client's 30 s timeout (kokoro.DefaultTimeout). A paragraph past this is a
# caller that stopped chunking clauses, and rendering it would park the
# satellite's feeder goroutine until the timeout fires.
MAX_INPUT = 2000

# How much of the voice list an error may quote. Unknown voices are a typo in
# one name, and the Go client bounds the body it reads: all 54 would be cut off
# mid-name in the log.
ERR_VOICES = 8


class Voice(Protocol):
    """Text in, float32 samples in [-1, 1] out. MlxVoice fills it; tests use a
    fake."""

    model: str
    sample_rate: int

    def voices(self) -> list[str]: ...

    def render(self, text: str, voice: str, speed: float) -> np.ndarray: ...


class Speech(BaseModel):
    """One POST /v1/audio/speech body. Unknown fields are ignored: an OpenAI
    client sends more than this serves."""

    model: str = MODEL
    input: str = ""
    voice: str = DEFAULT_VOICE
    response_format: str = "pcm"
    speed: float = 1.0

    # Upstream defaults this to true and streams nothing early anyway. Accepted
    # and ignored: kokoro-mlx splits a stream by sentence, and the satellite
    # already hands over one clause at a time (internal/satellite/chunk.go).
    stream: bool = True


def to_pcm(audio: np.ndarray) -> bytes:
    """Signed 16-bit little-endian, the device's format. Scaled by 32767, not
    32768: a sample at exactly +1.0 would wrap to full-scale negative and click."""
    return np.round(np.clip(audio, -1.0, 1.0) * 32767.0).astype("<i2").tobytes()


def to_wav(pcm: bytes, rate: int) -> bytes:
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(pcm)
    return buf.getvalue()


def create_app(voice: Voice) -> FastAPI:
    app = FastAPI(title="kokoromlx", docs_url=None, redoc_url=None)

    @app.get("/health")
    async def health() -> dict:
        return {"status": "ok", "model": voice.model}

    @app.get("/v1/audio/voices")
    async def voices() -> dict:
        return {"voices": voice.voices()}

    @app.post("/v1/audio/speech")
    async def speech(req: Speech) -> Response:
        if req.model != voice.model:
            raise HTTPException(400, f"model {req.model!r}; this serves {voice.model!r}")
        if req.response_format not in FORMATS:
            raise HTTPException(
                400,
                f"response format {req.response_format!r}; "
                f"this serves {', '.join(sorted(FORMATS))}",
            )
        if not MIN_SPEED <= req.speed <= MAX_SPEED:
            raise HTTPException(400, f"speed {req.speed} is outside {MIN_SPEED}-{MAX_SPEED}")
        if not req.input.strip():
            raise HTTPException(400, "input contains no speakable text")
        if len(req.input) > MAX_INPUT:
            raise HTTPException(
                400, f"input is {len(req.input)} characters, over the {MAX_INPUT} limit"
            )
        known = voice.voices()
        if req.voice not in known:
            raise HTTPException(
                400,
                f"unknown voice {req.voice!r}; "
                f"{len(known)} available, e.g. {', '.join(known[:ERR_VOICES])}",
            )

        # Seconds on the GPU, and synchronous: off the event loop so a health
        # check still answers and a second satellite's clause can queue.
        audio = await run_in_threadpool(voice.render, req.input, req.voice, req.speed)
        pcm = to_pcm(audio)
        if req.response_format == "wav":
            return Response(to_wav(pcm, voice.sample_rate), media_type=FORMATS["wav"])
        return Response(pcm, media_type=FORMATS["pcm"])

    return app
