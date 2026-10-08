"""ECAPA-TDNN from SpeechBrain, the checkpoint SPEC §10 names.

torch and speechbrain are imported inside the methods that need them: the
contract tests run against a fake embedder, and an import at module level
would make every one of them load a GPU framework.
"""

import os
from pathlib import Path

import numpy as np

MODEL = "speechbrain/spkrec-ecapa-voxceleb"

# What the checkpoint emits. Declared here and checked on every call so a
# swapped checkpoint fails the request rather than skewing every cosine.
DIM = 192

# A pinned revision is the only thing that makes an enrolled household
# reproducible across rebuilds; "main" is what a build gets until someone
# with Hub access records the commit (ADR-0025).
DEFAULT_REVISION = "main"


def unit(vec: np.ndarray) -> np.ndarray:
    """L2-normalise, so the host's cosine is a dot product and the contract's length is 1."""
    n = float(np.linalg.norm(vec))
    if not np.isfinite(n) or n == 0.0:
        raise ValueError("embedding has no direction")
    return (vec / n).astype(np.float32)


class EcapaEmbedder:
    model = MODEL
    dim = DIM

    def __init__(
        self,
        savedir: str | os.PathLike[str],
        revision: str = DEFAULT_REVISION,
        device: str | None = None,
    ) -> None:
        self._savedir = Path(savedir)
        self._revision = revision
        self._device = device
        self._encoder = None

    def load(self, download_only: bool = False) -> None:
        """Fetch and load the checkpoint. download_only bakes it into an image without a GPU."""
        import torch
        from speechbrain.inference.speaker import EncoderClassifier
        from speechbrain.utils.fetching import FetchConfig

        device = self._device or ("cuda" if torch.cuda.is_available() else "cpu")
        # allow_updates stays off: a container must serve the revision it was
        # built with, never a newer one the Hub happens to have.
        encoder = EncoderClassifier.from_hparams(
            source=MODEL,
            savedir=str(self._savedir),
            download_only=download_only,
            fetch_config=FetchConfig(revision=self._revision),
            run_opts={"device": device},
        )
        if not download_only:
            encoder.eval()
            self._encoder = encoder

    def embed(self, samples: np.ndarray) -> np.ndarray:
        import torch

        if self._encoder is None:
            raise RuntimeError("model is not loaded")
        wav = torch.from_numpy(np.ascontiguousarray(samples, dtype=np.float32)).unsqueeze(0)
        with torch.inference_mode():
            # encode_batch returns [batch, 1, dim].
            out = self._encoder.encode_batch(wav)
        vec = out.reshape(-1).cpu().numpy()
        if vec.shape != (DIM,):
            raise RuntimeError(f"checkpoint emitted {vec.shape[0]} dims, declared {DIM}")
        return unit(vec)
