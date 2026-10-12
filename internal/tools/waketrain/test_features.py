"""Which clips become which spectrogram set, before anything is generated."""

from pathlib import Path

import features
from layout import Layout


def clip(d: Path, name: str = "a.wav") -> None:
    d.mkdir(parents=True, exist_ok=True)
    (d / name).write_bytes(b"RIFF")


def test_each_split_of_clips_becomes_its_feature_set(tmp_path):
    lay = Layout(tmp_path)
    for split in ("training", "validation", "testing"):
        clip(lay.positives / split)
    clip(lay.household("auto", "training"))
    clip(lay.household("auto", "testing"))
    clip(lay.household("confirmed", "validation"))

    jobs = {j.name: j for j in features.plan(lay)}
    assert sorted(jobs) == [
        "household_auto/testing",
        "household_auto/testing_ambient",
        "household_auto/training",
        "household_confirmed/validation",
        "household_confirmed/validation_ambient",
        "positives/testing",
        "positives/training",
        "positives/validation",
    ]
    train = jobs["positives/training"]
    assert train.augment and (train.slide_frames, train.repeat) == (10, 2)
    assert train.out == lay.features / "positives/training/wakeword_mmap"
    assert (jobs["positives/testing"].slide_frames, jobs["positives/testing"].repeat) == (1, 1)
    neg = jobs["household_auto/testing_ambient"]
    assert not neg.augment and neg.slide_frames is None
    assert neg.source == lay.household("auto", "testing")
    assert neg.out == lay.household_features("auto") / "testing_ambient/household_mmap"
    assert all(j.out.name.endswith("_mmap") for j in jobs.values()), "microWakeWord globs *_mmap"


def test_an_empty_split_makes_no_set(tmp_path):
    lay = Layout(tmp_path)
    (lay.positives / "testing").mkdir(parents=True)
    clip(lay.positives / "training")
    assert [j.name for j in features.plan(lay)] == ["positives/training"]


def test_positives_are_mixed_over_training_negatives_only(tmp_path):
    lay = Layout(tmp_path)
    clip(lay.household("auto", "training"))
    clip(lay.household("auto", "testing"))
    clip(lay.household("confirmed", "validation"))
    assert features.backgrounds(lay) == [lay.household("auto", "training")]
    clip(lay.background / "fma", "song.wav")
    assert features.backgrounds(lay) == [lay.household("auto", "training"), lay.background]
    assert features.impulses(lay) == []
    clip(lay.rirs, "hall.wav")
    assert features.impulses(lay) == [lay.rirs]
