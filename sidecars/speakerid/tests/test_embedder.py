"""The numeric glue around the model, which needs neither torch nor the checkpoint."""

import numpy as np
import pytest

from speakerid.app import pcm_to_samples
from speakerid.embedder import DIM, MODEL, unit


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


# Full scale maps onto [-1, 1), the range the checkpoint was trained on.
def test_pcm_maps_full_scale_onto_unit_range() -> None:
    s = pcm_to_samples(np.array([-32768, 32767, 0], dtype="<i2").tobytes())
    assert s.dtype == np.float32
    assert s.tolist() == [-1.0, 32767 / 32768, 0.0]


# The declaration the Go client is built against (internal/provider/speakerid).
#
# verifies SPEC §10
def test_the_declared_contract_is_ecapa_at_192() -> None:
    assert MODEL == "speechbrain/spkrec-ecapa-voxceleb"
    assert DIM == 192
