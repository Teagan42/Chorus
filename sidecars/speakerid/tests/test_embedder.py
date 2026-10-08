"""The numeric glue around the model and the asset pin, which need neither the
runtime nor the model."""

import hashlib
from pathlib import Path

import numpy as np
import pytest

from speakerid.app import pcm_to_samples
from speakerid.embedder import ASSET_NAME, ASSET_SHA256, ASSET_URL, DIM, MODEL, fetch, unit


# The contract says unit length: the host's cosine is then a dot product
# against centroids that are unit by construction.
def test_unit_normalises_to_length_one() -> None:
    v = unit(np.array([3.0, 4.0], dtype=np.float32))
    assert v.dtype == np.float32
    assert v.tolist() == pytest.approx([0.6, 0.8])


def test_unit_refuses_a_vector_with_no_direction() -> None:
    with pytest.raises(ValueError):
        unit(np.zeros(4, dtype=np.float32))
    with pytest.raises(ValueError):
        unit(np.array([np.nan, 1.0], dtype=np.float32))


# Full scale maps onto [-1, 1), the range the model was trained on.
def test_pcm_maps_full_scale_onto_unit_range() -> None:
    s = pcm_to_samples(np.array([-32768, 32767, 0], dtype="<i2").tobytes())
    assert s.dtype == np.float32
    assert s.tolist() == [-1.0, 32767 / 32768, 0.0]


# The declaration the Go client is built against (internal/provider/speakerid).
#
# verifies SPEC §10
def test_the_declared_contract_is_titanet_at_192() -> None:
    assert MODEL == "nemo_en_titanet_large"
    assert DIM == 192


# The pin is a release asset and a digest, not a branch: both halves have to
# be there for a rebuild to be reproducible (ADR-0029).
def test_the_asset_is_pinned_by_url_and_digest() -> None:
    assert ASSET_URL.startswith("https://github.com/k2-fsa/sherpa-onnx/releases/download/")
    assert ASSET_URL.endswith("/" + ASSET_NAME)
    assert len(ASSET_SHA256) == 64 and int(ASSET_SHA256, 16)


MODEL_BYTES = b"not a model, but pinned like one"
MODEL_DIGEST = hashlib.sha256(MODEL_BYTES).hexdigest()


def source(tmp_path: Path, content: bytes) -> str:
    """A file:// URL, so the fetch runs its real path with no network."""
    src = tmp_path / "upstream" / ASSET_NAME
    src.parent.mkdir()
    src.write_bytes(content)
    return src.as_uri()


# What `--download-only` does at image build: the file lands under its name,
# matching the pin, with nothing half-written beside it.
def test_fetch_downloads_and_verifies_the_asset(tmp_path: Path) -> None:
    model_dir = tmp_path / "models"
    got = fetch(model_dir, url=source(tmp_path, MODEL_BYTES), digest=MODEL_DIGEST)
    assert got == model_dir / ASSET_NAME
    assert got.read_bytes() == MODEL_BYTES
    assert sorted(p.name for p in model_dir.iterdir()) == [ASSET_NAME]


# A container must serve exactly the bytes that were pinned: a replaced or
# truncated asset is refused, and nothing is left that a later boot would load.
def test_fetch_refuses_an_asset_whose_digest_differs(tmp_path: Path) -> None:
    model_dir = tmp_path / "models"
    with pytest.raises(RuntimeError, match="sha256"):
        fetch(model_dir, url=source(tmp_path, b"something else"), digest=MODEL_DIGEST)
    assert list(model_dir.iterdir()) == []


# Every boot re-checks the file it already has, so a corrupt copy is fetched
# again and a good one costs no download. The unreachable URL is the proof.
def test_fetch_keeps_a_matching_file_and_replaces_a_stale_one(tmp_path: Path) -> None:
    model_dir = tmp_path / "models"
    model_dir.mkdir()
    (model_dir / ASSET_NAME).write_bytes(MODEL_BYTES)
    assert fetch(model_dir, url="file:///nowhere", digest=MODEL_DIGEST).read_bytes() == MODEL_BYTES

    (model_dir / ASSET_NAME).write_bytes(b"stale")
    got = fetch(model_dir, url=source(tmp_path, MODEL_BYTES), digest=MODEL_DIGEST)
    assert got.read_bytes() == MODEL_BYTES


def test_fetch_names_a_source_it_cannot_reach(tmp_path: Path) -> None:
    with pytest.raises(RuntimeError, match="file:///nowhere"):
        fetch(tmp_path / "models", url="file:///nowhere", digest=MODEL_DIGEST)
