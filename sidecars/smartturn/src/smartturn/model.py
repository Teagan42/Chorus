"""Smart Turn v3.2, the CPU build, on ONNX Runtime: an 8M-parameter Whisper
encoder with a classifier on top that says whether the audio so far ends a
turn (SPEC §4.5, §10). It listens to the words' shape, not to the silence
after them, which is why "turn off the... uh..." is not over.

The model ships inside pipecat's wheel on PyPI and nowhere this environment
can reach otherwise, so the pin is the wheel's URL and digest plus the
member's own digest (ADR-0036).

onnxruntime is imported inside the method that needs it: the contract tests
run against a fake model and must not load the runtime.
"""

import hashlib
import os
import shutil
import tempfile
import threading
import urllib.request
import zipfile
from pathlib import Path

import numpy as np

from smartturn.whisper_features import compute_whisper_log_mel_features

# The name the contract reports. The Go client checks it, so a sidecar
# serving another checkpoint is refused rather than trusted with every turn.
MODEL = "smart-turn-v3.2-cpu"

# The model was trained at the satellite's own rate (internal/bridge), so
# nothing is resampled between the device and the verdict.
SAMPLE_RATE = 16_000

# What the model sees: the last eight seconds, silence-padded at the front
# when the turn is shorter. Upstream's window, not a tuning.
WINDOW = 8 * SAMPLE_RATE

# Above this the turn is over. Upstream's cut, kept so the probability the
# contract reports means what upstream's evaluations say it means.
THRESHOLD = 0.5

# The pin. A PyPI release never changes its files, and the digests refuse a
# replaced one at build and at every boot.
WHEEL_URL = (
    "https://files.pythonhosted.org/packages/10/fe/"
    "566fd73f43e66ce48b9a7e5dfa9cf79c713184978681708ac5c109c233ee/"
    "pipecat_ai-1.12.0-py3-none-any.whl"
)
WHEEL_SHA256 = "4cd3dc071b7b64da7ac700a26a224b773ae6ef8d6694a4566332d2e5e39ed6d9"
MEMBER = "pipecat/audio/turn/smart_turn/data/smart-turn-v3.2-cpu.onnx"
ASSET_SHA256 = "2bb026316b14a660486a75b1733cd3fbab8c2fd0314dc9af7be49f8cca967e4f"
ASSET_NAME = "smart-turn-v3.2-cpu.onnx"

# One verdict is a few tens of milliseconds on one core; past four threads
# it is no faster, and the sidecar shares the host with the rest of the stack.
MAX_THREADS = 4


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def fetch(
    model_dir: str | os.PathLike[str],
    url: str = WHEEL_URL,
    wheel_digest: str = WHEEL_SHA256,
    member: str = MEMBER,
    digest: str = ASSET_SHA256,
) -> Path:
    """Put the pinned model in model_dir and return its path. A file already
    there is kept only if its digest matches, so a stale or corrupt copy is
    replaced rather than served. The wheel is checked before anything is read
    out of it, and the model after; only the model is kept."""
    model_dir = Path(model_dir)
    path = model_dir / ASSET_NAME
    if path.is_file() and sha256(path) == digest:
        return path
    model_dir.mkdir(parents=True, exist_ok=True)
    # Both land beside the target and the model is renamed over it, so a
    # failure halfway leaves nothing that looks like the model.
    wfd, wheel = tempfile.mkstemp(dir=model_dir, prefix="wheel.", suffix=".part")
    mfd, tmp = tempfile.mkstemp(dir=model_dir, prefix=ASSET_NAME + ".", suffix=".part")
    try:
        try:
            with os.fdopen(wfd, "wb") as out, urllib.request.urlopen(url) as src:
                shutil.copyfileobj(src, out, 1 << 20)
        except OSError as e:
            raise RuntimeError(f"fetch {url}: {e}") from e
        got = sha256(Path(wheel))
        if got != wheel_digest:
            raise RuntimeError(f"fetch {url}: sha256 {got}, pinned {wheel_digest}")
        try:
            with zipfile.ZipFile(wheel) as z, z.open(member) as src, os.fdopen(mfd, "wb") as out:
                shutil.copyfileobj(src, out, 1 << 20)
        except KeyError as e:
            raise RuntimeError(f"fetch {url}: no {member} in the wheel") from e
        got = sha256(Path(tmp))
        if got != digest:
            raise RuntimeError(f"fetch {url}: {member} sha256 {got}, pinned {digest}")
        # mkstemp is owner-only; the model is not a secret and a server
        # running as another user must read it.
        os.chmod(tmp, 0o644)
        os.replace(tmp, path)
    finally:
        for f in (wheel, tmp):
            if os.path.exists(f):
                os.remove(f)
    return path


def window(samples: np.ndarray) -> np.ndarray:
    """The last WINDOW samples, zero-padded at the front: the end of the turn
    is what the model judges, so that is the part kept."""
    if len(samples) >= WINDOW:
        return samples[-WINDOW:]
    return np.pad(samples, (WINDOW - len(samples), 0))


def default_threads() -> int:
    return min(MAX_THREADS, os.cpu_count() or 1)


class OnnxTurn:
    model = MODEL

    def __init__(self, model_dir: str | os.PathLike[str], threads: int | None = None) -> None:
        self._model_dir = Path(model_dir)
        self._threads = threads or default_threads()
        self._session = None
        # One runtime session behind many threadpool workers. Serialised here:
        # a satellite asks about one pause at a time, and the lock costs nothing.
        self._lock = threading.Lock()

    def load(self, download_only: bool = False) -> None:
        """Fetch the pinned model and load it. download_only bakes it into an image."""
        path = fetch(self._model_dir)
        if download_only:
            return
        import onnxruntime as ort

        # Upstream's session options. The wheel is the CPU build; the model
        # is the CPU export too.
        so = ort.SessionOptions()
        so.execution_mode = ort.ExecutionMode.ORT_SEQUENTIAL
        so.inter_op_num_threads = 1
        so.intra_op_num_threads = self._threads
        so.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        self._session = ort.InferenceSession(
            str(path), sess_options=so, providers=["CPUExecutionProvider"]
        )

    def probability(self, samples: np.ndarray) -> float:
        """How likely the turn is over, from the audio so far."""
        if self._session is None:
            raise RuntimeError("model is not loaded")
        features = compute_whisper_log_mel_features(window(samples), do_normalize=True)
        with self._lock:
            out = self._session.run(None, {"input_features": np.expand_dims(features, 0)})
        p = float(np.asarray(out[0]).reshape(-1)[0])
        if not 0.0 <= p <= 1.0:
            raise RuntimeError(f"model answered {p}, not a probability")
        return p
