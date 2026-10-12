"""training.yaml, checked and rendered into microWakeWord's training parameters.

The checked-in file says how to train; what to train on is whatever the
pipeline's earlier steps left in the layout. So a feature set enters the
rendered config only when its spectrograms exist, and the model's manifest
is checked against the firmware before a minute is spent training it.
"""

from __future__ import annotations

from pathlib import Path

import manifest
import yaml
from layout import HOUSEHOLD_SETS, NEGATIVE_DATASETS, Layout

CHECKED_IN = Path(__file__).resolve().parent / "training.yaml"

SECTIONS = ("manifest", "augmentation", "feature_sets", "training", "model")


class ConfigError(ValueError):
    pass


def load(path: Path, esphome_version: str) -> dict:
    """The training config at path, refused if it could only train a model the firmware rejects."""
    spec = yaml.safe_load(Path(path).read_text())
    if not isinstance(spec, dict):
        raise ConfigError(f"{path}: not a mapping")
    missing = [s for s in SECTIONS if s not in spec]
    if missing:
        raise ConfigError(f"{path}: missing {', '.join(missing)}")
    problems = manifest.problems(manifest.build(spec["manifest"]), esphome_version)
    if problems:
        raise ConfigError(f"{path}: the model's manifest would be refused: " + "; ".join(problems))
    step, window = spec["manifest"]["feature_step_size"], spec["training"].get("window_step_ms")
    if step != window:
        raise ConfigError(
            f"{path}: manifest.feature_step_size {step} is not training.window_step_ms {window}: "
            "the device would feed the model features at another step than it learned"
        )
    sets = set(spec["feature_sets"])
    known = {"positives", *(f"household_{s}" for s in HOUSEHOLD_SETS), *NEGATIVE_DATASETS}
    if sets - known:
        raise ConfigError(f"{path}: unknown feature sets {sorted(sets - known)}")
    return spec


def features_dir(lay: Layout, name: str) -> Path:
    if name == "positives":
        return lay.features / "positives"
    if name.startswith("household_"):
        return lay.household_features(name.removeprefix("household_"))
    return lay.negative_datasets / name


def present(lay: Layout, spec: dict) -> set[str]:
    """The feature sets whose spectrograms are on disk."""
    return {n for n in spec["feature_sets"] if any(features_dir(lay, n).glob("**/*_mmap"))}


def render(spec: dict, lay: Layout, have: set[str]) -> dict:
    """microWakeWord's training_parameters, over the feature sets in have."""
    if "positives" not in have:
        raise ConfigError("no positive spectrograms: run task wake:positives, then features")
    if have <= {"positives"}:
        raise ConfigError(
            "no negative spectrograms: run task wake:corpus, or fetch microWakeWord's"
        )
    params = dict(spec["training"])
    params["train_dir"] = str(lay.train)
    params["features"] = [
        {
            "features_dir": str(features_dir(lay, name)),
            "sampling_weight": float(w["sampling_weight"]),
            "penalty_weight": float(w["penalty_weight"]),
            "truth": name == "positives",
            "truncation_strategy": w["truncation_strategy"],
            "type": "mmap",
        }
        for name, w in spec["feature_sets"].items()
        if name in have
    ]
    return params


def dump(params: dict) -> str:
    return yaml.safe_dump(params, sort_keys=False)
