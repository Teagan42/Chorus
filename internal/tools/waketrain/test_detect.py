"""The device's wake rule, replayed: what fires, and what a cough does not."""

import detect
import pytest

CUTOFF = 247  # 0.97, quantised
WINDOW = 5
STRIDE = 3


def test_a_held_wake_word_fires_once_on_the_fifth_confident_inference():
    # The ring must sum past 247 * 5; five 255s do, four do not.
    assert detect.detections([255] * 4, STRIDE, CUTOFF, WINDOW) == 0
    assert detect.detections([255] * 5, STRIDE, CUTOFF, WINDOW) == 1
    # Still confident after firing: no slice is cool, so it does not refire.
    assert detect.detections([255] * 200, STRIDE, CUTOFF, WINDOW) == 1


def test_a_single_spike_like_a_cough_does_not_fire():
    assert detect.detections([0, 0, 255, 0, 0, 0], STRIDE, CUTOFF, WINDOW) == 0


def test_a_ring_exactly_at_the_cutoff_does_not_fire():
    assert detect.detections([CUTOFF] * 50, STRIDE, CUTOFF, WINDOW) == 0
    assert detect.detections([CUTOFF + 1] * 50, STRIDE, CUTOFF, WINDOW) == 1


def test_a_second_wake_fires_only_after_a_second_of_cool_slices():
    # 100 cool slices at a stride of 3: the emptied ring is cool from the
    # slice after firing, so 33 quiet inferences and two slices of the next.
    soon = [255] * 5 + [0] * 32 + [255] * 5
    later = [255] * 5 + [0] * 33 + [255] * 5
    assert detect.detections(soon, STRIDE, CUTOFF, WINDOW) == 1
    assert detect.detections(later, STRIDE, CUTOFF, WINDOW) == 2


def test_the_cool_down_counts_slices_not_inferences():
    # At a stride of 1, the same second is 100 quiet inferences.
    assert detect.detections([255] * 5 + [0] * 99 + [255] * 5, 1, CUTOFF, WINDOW) == 1
    assert detect.detections([255] * 5 + [0] * 100 + [255] * 5, 1, CUTOFF, WINDOW) == 2


def test_a_model_that_needs_no_stride_or_window_is_refused():
    with pytest.raises(ValueError):
        detect.detections([255], 0, CUTOFF, WINDOW)
    with pytest.raises(ValueError):
        detect.detections([255], STRIDE, CUTOFF, 0)
