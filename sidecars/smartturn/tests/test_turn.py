"""The numeric glue around the model and the pin, which need neither the
runtime nor the model."""

import hashlib
import io
import zipfile
from pathlib import Path

import numpy as np
import pytest

from smartturn.model import (
    ASSET_NAME,
    ASSET_SHA256,
    MEMBER,
    MODEL,
    THRESHOLD,
    WHEEL_SHA256,
    WHEEL_URL,
    WINDOW,
    fetch,
    window,
)
from smartturn.whisper_features import compute_whisper_log_mel_features


# The declaration the Go client is built against (internal/provider/smartturn).
#
# verifies SPEC §10
def test_the_declared_contract_is_smart_turn_v3_2() -> None:
    assert MODEL == "smart-turn-v3.2-cpu"
    assert THRESHOLD == 0.5
    assert WINDOW == 8 * 16_000


# A short turn sits at the end of the window, where the model looks for the
# end of a turn; the silence it is padded with comes first.
def test_a_short_turn_is_padded_at_the_front() -> None:
    turn = np.full(16_000, 0.25, dtype=np.float32)
    w = window(turn)
    assert len(w) == WINDOW
    assert not w[: WINDOW - 16_000].any()
    assert (w[-16_000:] == 0.25).all()


# A long one keeps its end, which is what is being judged.
def test_a_long_turn_keeps_its_last_eight_seconds() -> None:
    turn = np.arange(10 * 16_000, dtype=np.float32)
    w = window(turn)
    assert len(w) == WINDOW
    assert w[0] == 2 * 16_000 and w[-1] == turn[-1]


# The model was trained on exactly this shape; anything else fails at the
# runtime, or worse, does not.
def test_features_are_eighty_mels_by_eight_hundred_frames() -> None:
    t = np.arange(WINDOW) / 16_000
    f = compute_whisper_log_mel_features(np.sin(2 * np.pi * 140 * t).astype(np.float32))
    assert f.shape == (80, 800) and f.dtype == np.float32
    assert np.isfinite(f).all()


# The pin is a PyPI file and two digests, not a Hub branch: every part has
# to be there for a rebuild to be reproducible (ADR-0036).
def test_the_model_is_pinned_by_wheel_and_member_digest() -> None:
    assert WHEEL_URL.startswith("https://files.pythonhosted.org/packages/")
    assert WHEEL_URL.endswith("/pipecat_ai-1.12.0-py3-none-any.whl")
    assert MEMBER.endswith("/" + ASSET_NAME)
    for digest in (WHEEL_SHA256, ASSET_SHA256):
        assert len(digest) == 64 and int(digest, 16)


MODEL_BYTES = b"not smart turn, but pinned like it"
MODEL_DIGEST = hashlib.sha256(MODEL_BYTES).hexdigest()


def wheel(members: dict[str, bytes]) -> bytes:
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        for name, data in members.items():
            z.writestr(name, data)
    return buf.getvalue()


def source(tmp_path: Path, content: bytes) -> tuple[str, str]:
    """A file:// wheel and its digest, so the fetch runs its real path with
    no network."""
    src = tmp_path / "upstream" / "pipecat_ai-1.12.0-py3-none-any.whl"
    src.parent.mkdir(exist_ok=True)
    src.write_bytes(content)
    return src.as_uri(), hashlib.sha256(content).hexdigest()


GOOD = wheel({MEMBER: MODEL_BYTES, "pipecat/__init__.py": b""})


# What `--download-only` does at image build: the model lands under its name,
# matching the pin, with neither the wheel nor anything half-written beside it.
def test_fetch_extracts_and_verifies_the_model(tmp_path: Path) -> None:
    model_dir = tmp_path / "models"
    url, wd = source(tmp_path, GOOD)
    got = fetch(model_dir, url=url, wheel_digest=wd, digest=MODEL_DIGEST)
    assert got == model_dir / ASSET_NAME
    assert got.read_bytes() == MODEL_BYTES
    assert sorted(p.name for p in model_dir.iterdir()) == [ASSET_NAME]


# A replaced wheel is refused before anything is read out of it.
def test_fetch_refuses_a_wheel_whose_digest_differs(tmp_path: Path) -> None:
    model_dir = tmp_path / "models"
    url, _ = source(tmp_path, GOOD)
    with pytest.raises(RuntimeError, match="sha256"):
        fetch(model_dir, url=url, wheel_digest="0" * 64, digest=MODEL_DIGEST)
    assert list(model_dir.iterdir()) == []


# A wheel that matches its pin but carries another model is refused too, and
# leaves nothing a later boot would load.
def test_fetch_refuses_a_model_whose_digest_differs(tmp_path: Path) -> None:
    model_dir = tmp_path / "models"
    url, wd = source(tmp_path, wheel({MEMBER: b"smart-turn-v2"}))
    with pytest.raises(RuntimeError, match=f"{MEMBER} sha256"):
        fetch(model_dir, url=url, wheel_digest=wd, digest=MODEL_DIGEST)
    assert list(model_dir.iterdir()) == []


def test_fetch_names_a_member_the_wheel_lacks(tmp_path: Path) -> None:
    model_dir = tmp_path / "models"
    url, wd = source(tmp_path, wheel({"pipecat/__init__.py": b""}))
    with pytest.raises(RuntimeError, match="no " + MEMBER):
        fetch(model_dir, url=url, wheel_digest=wd, digest=MODEL_DIGEST)
    assert list(model_dir.iterdir()) == []


# Every boot re-checks the file it already has, so a corrupt copy is fetched
# again and a good one costs no download. The unreachable URL is the proof.
def test_fetch_keeps_a_matching_file_and_replaces_a_stale_one(tmp_path: Path) -> None:
    model_dir = tmp_path / "models"
    model_dir.mkdir()
    (model_dir / ASSET_NAME).write_bytes(MODEL_BYTES)
    kept = fetch(model_dir, url="file:///nowhere", wheel_digest="0" * 64, digest=MODEL_DIGEST)
    assert kept.read_bytes() == MODEL_BYTES

    (model_dir / ASSET_NAME).write_bytes(b"stale")
    url, wd = source(tmp_path, GOOD)
    got = fetch(model_dir, url=url, wheel_digest=wd, digest=MODEL_DIGEST)
    assert got.read_bytes() == MODEL_BYTES


def test_fetch_names_a_source_it_cannot_reach(tmp_path: Path) -> None:
    with pytest.raises(RuntimeError, match="file:///nowhere"):
        fetch(tmp_path / "models", url="file:///nowhere", digest=MODEL_DIGEST)
