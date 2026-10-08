"""TitaNet-L, one of the two models SPEC §10 names, as the ONNX export that
sherpa-onnx publishes. ONNX Runtime rather than torch: the sidecar is then a
tenth of the size, and the model is one file pinned by URL and digest instead
of a Hub revision that can move (ADR-0029).

sherpa_onnx is imported inside the method that needs it: the contract tests
run against a fake embedder and must not load the runtime.
"""

import hashlib
import os
import shutil
import tempfile
import threading
import urllib.request
from pathlib import Path

import numpy as np

# The name the contract reports. identities.yaml records it and the Go client
# checks it, so a different model is refused rather than scored as noise.
MODEL = "nemo_en_titanet_large"

# What the model emits. Declared here and checked at load and on every call so
# a swapped file fails the request rather than skewing every cosine.
DIM = 192

# The model was trained at the satellite's own rate (internal/bridge), so
# nothing is resampled between the device and the embedding.
SAMPLE_RATE = 16_000

# The pin. A release asset does not move the way a Hub branch does, and the
# digest refuses a replaced one at build and at every boot. The tag's
# spelling is upstream's.
ASSET_URL = (
    "https://github.com/k2-fsa/sherpa-onnx/releases/download/"
    "speaker-recongition-models/nemo_en_titanet_large.onnx"
)
ASSET_SHA256 = "d51abcf31717ef28162f26acb9d44dd4127c3d44c9b8624f699f3425daca8e77"
ASSET_NAME = "nemo_en_titanet_large.onnx"

# Past four threads an utterance embeds no faster on this model, and the
# sidecar shares the host with the rest of the stack.
MAX_THREADS = 4


def unit(vec: np.ndarray) -> np.ndarray:
    """L2-normalise, so the host's cosine is a dot product and the contract's length is 1."""
    n = float(np.linalg.norm(vec))
    if not np.isfinite(n) or n == 0.0:
        raise ValueError("embedding has no direction")
    return (vec / n).astype(np.float32)


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def fetch(
    model_dir: str | os.PathLike[str], url: str = ASSET_URL, digest: str = ASSET_SHA256
) -> Path:
    """Put the pinned asset in model_dir and return its path. A file already
    there is kept only if its digest matches, so a stale or corrupt copy is
    replaced rather than served."""
    model_dir = Path(model_dir)
    path = model_dir / ASSET_NAME
    if path.is_file() and sha256(path) == digest:
        return path
    model_dir.mkdir(parents=True, exist_ok=True)
    # Downloaded beside the target and renamed over it, so a failure halfway
    # leaves nothing that looks like the model.
    fd, tmp = tempfile.mkstemp(dir=model_dir, prefix=ASSET_NAME + ".", suffix=".part")
    try:
        try:
            with os.fdopen(fd, "wb") as out, urllib.request.urlopen(url) as src:
                shutil.copyfileobj(src, out, 1 << 20)
        except OSError as e:
            raise RuntimeError(f"fetch {url}: {e}") from e
        got = sha256(Path(tmp))
        if got != digest:
            raise RuntimeError(f"fetch {url}: sha256 {got}, pinned {digest}")
        # mkstemp is owner-only; the model is not a secret and a server
        # running as another user must read it.
        os.chmod(tmp, 0o644)
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.remove(tmp)
    return path


def default_threads() -> int:
    return min(MAX_THREADS, os.cpu_count() or 1)


class OnnxEmbedder:
    model = MODEL
    dim = DIM

    def __init__(self, model_dir: str | os.PathLike[str], threads: int | None = None) -> None:
        self._model_dir = Path(model_dir)
        self._threads = threads or default_threads()
        self._extractor = None
        # One runtime session behind many threadpool workers. Serialised here:
        # a household says one thing at a time, and the lock costs nothing.
        self._lock = threading.Lock()

    def load(self, download_only: bool = False) -> None:
        """Fetch the pinned asset and load it. download_only bakes it into an image."""
        path = fetch(self._model_dir)
        if download_only:
            return
        import sherpa_onnx

        # The PyPI wheel is the CPU build. A GPU build is a different wheel
        # and a compose change, not a flag (SPEC §13).
        cfg = sherpa_onnx.SpeakerEmbeddingExtractorConfig(
            model=str(path), num_threads=self._threads, provider="cpu"
        )
        if not cfg.validate():
            raise RuntimeError(f"load {path}: the runtime rejects the model config")
        extractor = sherpa_onnx.SpeakerEmbeddingExtractor(cfg)
        if extractor.dim != DIM:
            raise RuntimeError(f"load {path}: model emits {extractor.dim} dims, declared {DIM}")
        self._extractor = extractor

    def embed(self, samples: np.ndarray) -> np.ndarray:
        if self._extractor is None:
            raise RuntimeError("model is not loaded")
        wav = np.ascontiguousarray(samples, dtype=np.float32)
        with self._lock:
            stream = self._extractor.create_stream()
            stream.accept_waveform(sample_rate=SAMPLE_RATE, waveform=wav)
            stream.input_finished()
            if not self._extractor.is_ready(stream):
                raise RuntimeError(f"{len(wav)} samples is too few for the model to pool over")
            vec = np.asarray(self._extractor.compute(stream), dtype=np.float32)
        if vec.shape != (DIM,):
            raise RuntimeError(f"model emitted {vec.shape[0]} dims, declared {DIM}")
        return unit(vec)
