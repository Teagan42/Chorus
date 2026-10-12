"""When the satellite's micro_wake_word fires, given what the model says.

A replay of ESPHome 2026.7.2's streaming_model.cpp and micro_wake_word.cpp,
so a model is scored here by the rule the device wakes by rather than by a
mean probability. Per 10 ms feature slice: every stride slices the model
gives a uint8 probability into a ring of sliding_window_size; a slice whose
latest probability is under the cutoff cools the model down by one; once
cool, it fires when the ring's sum exceeds cutoff times its size, and a
detection empties the ring and needs 100 cool slices again.
"""

from __future__ import annotations

from collections.abc import Iterable

# streaming_model.h: slices under the cutoff needed after a detection.
MIN_SLICES_BEFORE_DETECTION = 100


def detections(probabilities: Iterable[int], stride: int, cutoff: int, window: int) -> int:
    """How many times the device fires over a clip.

    probabilities are the model's uint8 outputs, one per inference, each
    after stride feature slices; cutoff is the quantised probability_cutoff.
    The model starts cool, as one listening to a quiet room is.
    """
    if stride < 1 or window < 1:
        raise ValueError(f"stride {stride} and window {window} must be positive")
    ring = [0] * window
    last = 0
    ignore = 0
    fresh = False
    fired = 0
    for p in probabilities:
        for slice_ in range(stride):
            if slice_ == stride - 1:
                last = (last + 1) % window
                ring[last] = p
                fresh = True
            if ring[last] < cutoff:
                ignore = min(ignore + 1, 0)
            # determine_detected returns early while warming, leaving the
            # probability unprocessed, so the check repeats every slice.
            if fresh and ignore >= 0:
                fresh = False
                if sum(ring) > cutoff * window:
                    fired += 1
                    ring = [0] * window
                    ignore = -MIN_SLICES_BEFORE_DETECTION
    return fired
