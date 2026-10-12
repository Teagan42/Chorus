"""Spectrograms of the positives and the household's negatives, as microWakeWord trains on.

Planning which directory becomes which feature set is plain and tested; the
generation itself needs microWakeWord, TensorFlow and pymicro-features, so
it runs in the trainer's container (Dockerfile).

The positives are augmented as the training notebook does, against room
impulse responses and background noise. The background is the household's
own training negatives, and anything in .wake/augment/background, so a
positive is heard over the rooms the satellites are in. The negatives are
real audio and are not augmented. Their held-out splits are also written as
the ambient sets microWakeWord estimates false accepts an hour from.
"""

from __future__ import annotations

import shutil
from dataclasses import dataclass
from pathlib import Path

from layout import HOUSEHOLD_SETS, SPLITS, Layout, wavs

# The notebook's: training sees each positive twice, slid over ten frames to
# simulate streaming; testing scores the streaming model, so no slide.
POSITIVE_PASSES = {"training": (10, 2), "validation": (10, 1), "testing": (1, 1)}

AUDIO_SUFFIXES = (".wav", ".flac", ".mp3")


@dataclass(frozen=True)
class Job:
    name: str
    source: Path
    out: Path
    augment: bool
    slide_frames: int | None
    repeat: int


def plan(lay: Layout) -> list[Job]:
    """Every spectrogram set to generate, for the clips the layout holds."""
    jobs = []
    for split in SPLITS:
        src = lay.positives / split
        if wavs(src):
            slide, repeat = POSITIVE_PASSES[split]
            out = lay.features / "positives" / split / "wakeword_mmap"
            jobs.append(Job(f"positives/{split}", src, out, True, slide, repeat))
    for set_ in HOUSEHOLD_SETS:
        for split in SPLITS:
            src = lay.household(set_, split)
            if not wavs(src):
                continue
            for mode in (split,) if split == "training" else (split, f"{split}_ambient"):
                out = lay.household_features(set_) / mode / "household_mmap"
                jobs.append(Job(f"household_{set_}/{mode}", src, out, False, None, 1))
    return jobs


def _has_audio(d: Path) -> bool:
    return d.is_dir() and any(p.suffix in AUDIO_SUFFIXES for p in d.rglob("*"))


def backgrounds(lay: Layout) -> list[Path]:
    """What the positives are mixed over: never a held-out negative."""
    dirs = [lay.household(s, "training") for s in HOUSEHOLD_SETS]
    dirs.append(lay.background)
    return [d for d in dirs if _has_audio(d)]


def impulses(lay: Layout) -> list[Path]:
    return [lay.rirs] if _has_audio(lay.rirs) else []


def run(jobs: list[Job], spec: dict, lay: Layout) -> None:  # pragma: no cover - the container's
    from microwakeword.audio.augmentation import Augmentation
    from microwakeword.audio.clips import Clips
    from microwakeword.audio.spectrograms import SpectrogramGeneration
    from mmap_ninja.ragged import RaggedMmap

    step = spec["training"]["window_step_ms"]
    bg, rirs = [str(d) for d in backgrounds(lay)], [str(d) for d in impulses(lay)]
    if not rirs:
        print("no room impulse responses in", lay.rirs, "- positives are not reverberated")
    for job in jobs:
        print(f"features: {job.name} from {job.source}")
        if job.out.exists():
            shutil.rmtree(job.out)
        job.out.parent.mkdir(parents=True, exist_ok=True)
        clips = Clips(input_directory=str(job.source), file_pattern="*.wav", remove_silence=False)
        augmenter = None
        if job.augment:
            augmenter = Augmentation(
                **spec["augmentation"], impulse_paths=rirs, background_paths=bg
            )
        gen = SpectrogramGeneration(
            clips=clips, augmenter=augmenter, slide_frames=job.slide_frames, step_ms=step
        )
        RaggedMmap.from_generator(
            out_dir=str(job.out),
            sample_generator=gen.spectrogram_generator(repeat=job.repeat),
            batch_size=100,
            verbose=True,
        )
