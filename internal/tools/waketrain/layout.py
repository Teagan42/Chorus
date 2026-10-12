"""Where each step of the wake-word pipeline reads and writes, under one directory.

    .wake/                 the household's; gitignored, never committed
      corpus/              task wake:corpus: the hard negatives, by set and split
      positives/           task wake:positives: "hey eddie" in Kokoro's voices
      augment/rirs/        yours, optional: room impulse responses for the positives
      augment/background/  yours, optional: more background noise for the positives
      negative_datasets/   microWakeWord's own negative spectrograms (wake.py fetch)
      features/            spectrograms of the above (wake.py features)
      train/               microWakeWord's train_dir
      model/               the trained model, its manifest and its provenance
      evaluation.json      task wake:evaluate's report

The trainer runs in a container with .wake mounted at /wake.
"""

from __future__ import annotations

from pathlib import Path

SPLITS = ("training", "validation", "testing")

# The household's two piles of hard negatives (internal/tools/wakecorpus).
HOUSEHOLD_SETS = ("confirmed", "auto")

# microWakeWord's pre-generated negative spectrograms, by the name of the
# zip its training notebook fetches each from.
NEGATIVE_DATASETS = ("speech", "dinner_party", "no_speech", "dinner_party_eval")
NEGATIVE_DATASETS_URL = "https://huggingface.co/datasets/kahrendt/microwakeword/resolve/main/"


class Layout:
    def __init__(self, root: Path) -> None:
        self.root = Path(root)

    @property
    def corpus(self) -> Path:
        return self.root / "corpus"

    @property
    def positives(self) -> Path:
        return self.root / "positives"

    @property
    def rirs(self) -> Path:
        return self.root / "augment" / "rirs"

    @property
    def background(self) -> Path:
        return self.root / "augment" / "background"

    @property
    def negative_datasets(self) -> Path:
        return self.root / "negative_datasets"

    @property
    def features(self) -> Path:
        return self.root / "features"

    @property
    def train(self) -> Path:
        return self.root / "train"

    @property
    def model(self) -> Path:
        return self.root / "model"

    @property
    def evaluation(self) -> Path:
        return self.root / "evaluation.json"

    def household(self, set_: str, split: str) -> Path:
        return self.corpus / "negatives" / set_ / split

    def household_features(self, set_: str) -> Path:
        return self.features / f"household_{set_}"


def wavs(d: Path) -> list[Path]:
    """The clips in one split directory, in a stable order."""
    return sorted(d.glob("*.wav")) if d.is_dir() else []
