"""Kokoro-82M on MLX: the same voice SPEC §10 names, rendered on this Mac's GPU
instead of a container's CPU. Measured here, 2026-10-13: a clause in 0.28 s
against the CPU image's 0.56-0.92 s, roughly a tenth of the audio's own length.

MLX reaches Metal directly, and Docker Desktop on macOS passes no GPU through,
so this sidecar is a host process and has no image (ADR-0092).

The pin is the weights' repo and revision. A revision is immutable and the Hub
checks each blob's digest as it lands, so there is no second digest to keep.

kokoro_mlx is imported inside load(): the contract tests run against a fake and
must not pull MLX, spacy and torch in to do it.
"""

import os
from pathlib import Path

import numpy as np

# The name the client sends and the journal records against every turn
# (SPEC §8). Kokoro-FastAPI serves this one name, so a drop-in serves it too --
# which also means the journal cannot tell this engine from that one.
MODEL = "kokoro"

# The endpoint's own default voice, repeated in the Go client as
# kokoro.DefaultVoice.
DEFAULT_VOICE = "af_heart"

# The pinned weights: the bf16 MLX conversion of hexgrad's Kokoro-82M.
REPO = "mlx-community/Kokoro-82M-bf16"
REVISION = "a71e4d38b236d968966a2002c4c895dbd12b1c3c"

# What is fetched out of the repo: the config, the weights, and the 54 voices
# as safetensors. The .pt copies of the voices and the sample WAVs are the
# other two thirds of it and nothing reads them.
PATTERNS = ("config.json", "kokoro-v1_0.safetensors", "voices/*.safetensors")

# Kokoro renders at this rate and the Go client's resampler is wired 3:2 to the
# device's 16 kHz (internal/provider/kokoro/resample.go). Serving 48 kHz, which
# kokoro-mlx will also do, would quietly halve the pitch at the speaker.
SAMPLE_RATE = 24_000


def fetch(model_dir: str | os.PathLike[str]) -> Path:
    """Put the pinned revision in model_dir and return it. Files already there
    are left alone: the Hub keeps its own cache in the directory and skips what
    it has."""
    from huggingface_hub import snapshot_download

    local = Path(model_dir)
    local.mkdir(parents=True, exist_ok=True)
    snapshot_download(
        repo_id=REPO,
        revision=REVISION,
        allow_patterns=list(PATTERNS),
        local_dir=str(local),
    )
    return local


class MlxVoice:
    model = MODEL
    sample_rate = SAMPLE_RATE

    def __init__(self, model_dir: str | os.PathLike[str]) -> None:
        self._model_dir = Path(model_dir)
        self._tts = None
        self._voices: list[str] = []

    def load(self, download_only: bool = False) -> None:
        """Fetch the pinned weights and load them onto the GPU."""
        path = fetch(self._model_dir)
        if download_only:
            return
        from kokoro_mlx import KokoroTTS

        # From the directory, never the repo id: given a repo id kokoro-mlx
        # fetches the whole repo into the Hub's own cache, unpinned.
        self._tts = KokoroTTS.from_pretrained(path)
        # Read once: upstream globs the voices directory on every call, and
        # every utterance checks its voice against this list.
        self._voices = self._tts.list_voices()
        # The first render pays for the G2P model and MLX's kernel compilation,
        # about eight seconds. Spent here so the port opens ready rather than
        # the first turn of the day sounding broken.
        self._tts.generate("Ready.", voice=DEFAULT_VOICE)

    def voices(self) -> list[str]:
        if self._tts is None:
            raise RuntimeError("model is not loaded")
        return self._voices

    def render(self, text: str, voice: str, speed: float) -> np.ndarray:
        """Float32 samples in [-1, 1] at SAMPLE_RATE. KokoroTTS.generate holds
        its own lock, so many satellites speaking at once queue rather than
        collide on the one GPU."""
        if self._tts is None:
            raise RuntimeError("model is not loaded")
        result = self._tts.generate(text, voice=voice, speed=speed, sample_rate=self.sample_rate)
        return np.asarray(result.audio, dtype=np.float32)
