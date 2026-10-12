"""Score a wake model on held-out clips, beside the one the satellites run now.

A model is scored by the rule the device wakes by (detect.py), at its own
manifest's cutoff and window, on the testing split nothing was trained on:

- false accepts an hour, over the household's held-out hard negatives. These
  are the sounds the deployed model already woke on (SPEC §9.3), so the rate
  is far above a real room's; it compares models, it does not forecast.
- false rejects, the share of held-out positives that never fire: Kokoro
  voices the training never heard.

A candidate ships only if it is better: no more false accepts than the
deployed model, misses no more than it beyond a tolerance, and fewer of one
or the other. Running the models needs TensorFlow Lite and microWakeWord's
feature frontend, so that part runs in the trainer's container.
"""

from __future__ import annotations

import json
import urllib.request
import wave
from dataclasses import asdict, dataclass
from pathlib import Path
from urllib.parse import urljoin, urlparse

import detect
import manifest
import numpy as np
from layout import HOUSEHOLD_SETS, Layout, wavs

DEVICE_RATE = 16_000

# Silence around a positive while it is scored: the frontend's noise
# estimate settles before the word, and the ring has room after it.
LEAD_S, TAIL_S = 1.0, 0.5

SWEEP = (0.5, 0.6, 0.7, 0.8, 0.85, 0.9, 0.93, 0.95, 0.97, 0.98, 0.99)


@dataclass(frozen=True)
class Score:
    cutoff: float
    window: int
    false_accepts: int
    hours: float
    false_accepts_per_hour: float
    missed: int
    positives: int
    false_reject_rate: float


@dataclass
class Outputs:
    """A model's uint8 outputs on every held-out clip, so a sweep costs nothing."""

    stride: int
    negatives: dict[str, list[int]]
    positives: dict[str, list[int]]
    negative_seconds: float


def held_out(lay: Layout, split: str = "testing") -> tuple[list[Path], list[Path]]:
    negatives = [p for s in HOUSEHOLD_SETS for p in wavs(lay.household(s, split))]
    positives = wavs(lay.positives / split)
    if not negatives:
        raise SystemExit(
            f"no held-out negatives in {lay.corpus}/negatives/*/{split}: run task wake:corpus"
        )
    if not positives:
        raise SystemExit(
            f"no held-out positives in {lay.positives / split}: run task wake:positives"
        )
    return negatives, positives


def read_pcm(path: Path) -> np.ndarray:
    """A clip as int16 samples, refused unless it is the device's format."""
    with wave.open(str(path), "rb") as w:
        if (w.getframerate(), w.getnchannels(), w.getsampwidth()) != (DEVICE_RATE, 1, 2):
            raise ValueError(
                f"{path}: {w.getframerate()} Hz, {w.getnchannels()} channel(s), "
                f"{8 * w.getsampwidth()}-bit; want 16 kHz mono 16-bit"
            )
        return np.frombuffer(w.readframes(w.getnframes()), dtype="<i2")


def padded(pcm: np.ndarray) -> np.ndarray:
    lead, tail = (
        np.zeros(round(LEAD_S * DEVICE_RATE), "<i2"),
        np.zeros(round(TAIL_S * DEVICE_RATE), "<i2"),
    )
    return np.concatenate([lead, pcm, tail])


def score(out: Outputs, cutoff: float, window: int) -> Score:
    q = manifest.quantised(cutoff)
    fa = sum(detect.detections(p, out.stride, q, window) for p in out.negatives.values())
    missed = sum(
        1 for p in out.positives.values() if detect.detections(p, out.stride, q, window) == 0
    )
    hours = out.negative_seconds / 3600
    n = len(out.positives)
    return Score(
        cutoff=cutoff,
        window=window,
        false_accepts=fa,
        hours=round(hours, 4),
        false_accepts_per_hour=round(fa / hours, 2) if hours else 0.0,
        missed=missed,
        positives=n,
        false_reject_rate=round(missed / n, 4) if n else 0.0,
    )


def sweep(out: Outputs, window: int, cutoffs: tuple[float, ...] = SWEEP) -> list[Score]:
    return [score(out, c, window) for c in cutoffs]


def verdict(candidate: Score, deployed: Score | None, tolerance: float) -> tuple[bool, str]:
    """Whether the candidate is better than what the satellites run, and why."""
    if deployed is None:
        return True, "nothing deployed to compare with"
    c, d = candidate, deployed
    if c.false_accepts > d.false_accepts:
        return False, (
            f"more false accepts on the held-out negatives: {c.false_accepts} "
            f"({c.false_accepts_per_hour}/h) against {d.false_accepts} "
            f"({d.false_accepts_per_hour}/h)"
        )
    if c.false_reject_rate > d.false_reject_rate + tolerance:
        return False, (
            f"misses more held-out positives: {c.false_reject_rate:.1%} against "
            f"{d.false_reject_rate:.1%}, past the {tolerance:.1%} allowed"
        )
    if c.false_accepts == d.false_accepts and c.false_reject_rate >= d.false_reject_rate:
        return False, "no better: as many false accepts, and misses no fewer positives"
    return True, (
        f"false accepts {d.false_accepts} -> {c.false_accepts} "
        f"({d.false_accepts_per_hour}/h -> {c.false_accepts_per_hour}/h), "
        f"false rejects {d.false_reject_rate:.1%} -> {c.false_reject_rate:.1%}"
    )


def model_url(manifest_url: str, model: str) -> str:
    """Where ESPHome fetches a manifest's model from: beside the manifest."""
    return urljoin(manifest_url, model)


def fetch(source: str, dest: Path) -> Path:  # pragma: no cover - network
    """A deployed model's manifest and .tflite, from a URL or a local path."""
    if urlparse(source).scheme not in ("http", "https"):
        return Path(source)
    dest.mkdir(parents=True, exist_ok=True)
    with urllib.request.urlopen(source, timeout=30) as r:
        body = r.read()
    m = json.loads(body)
    (dest / "manifest.json").write_bytes(body)
    with urllib.request.urlopen(model_url(source, m["model"]), timeout=60) as r:
        (dest / m["model"]).write_bytes(r.read())
    return dest / "manifest.json"


def load(manifest_path: Path, esphome_version: str) -> tuple[dict, bytes, list[str]]:
    """A model's manifest (as ESPHome reads it), its .tflite, and what is wrong with them."""
    m = manifest.as_v2(json.loads(Path(manifest_path).read_text()))
    data = (Path(manifest_path).parent / m["model"]).read_bytes()
    return m, data, manifest.problems(m, esphome_version) + manifest.model_problems(data)


class Runner:  # pragma: no cover - the container's: TFLite and pymicro-features
    """A streaming .tflite run clip by clip, from a fresh state, as the device would."""

    def __init__(self, model: bytes, step_ms: int) -> None:
        from ai_edge_litert.interpreter import Interpreter

        self._interpreter = Interpreter
        self.model = model
        self.step_ms = step_ms
        probe = Interpreter(model_content=model)
        probe.allocate_tensors()
        problems = manifest.tensor_problems(probe.get_input_details(), probe.get_output_details())
        if problems:
            raise ValueError("the device would refuse this model: " + "; ".join(problems))
        self.stride = int(probe.get_input_details()[0]["shape"][1])

    def outputs(self, pcm: np.ndarray) -> list[int]:
        from microwakeword.audio.audio_utils import generate_features_for_clip

        it = self._interpreter(model_content=self.model)
        it.allocate_tensors()
        inp, outp = it.get_input_details()[0], it.get_output_details()[0]
        scale, zero = (
            inp["quantization_parameters"]["scales"][0],
            inp["quantization_parameters"]["zero_points"][0],
        )
        spectrogram = generate_features_for_clip(pcm, step_ms=self.step_ms)
        if np.issubdtype(spectrogram.dtype, np.uint16):
            spectrogram = spectrogram.astype(np.float32) * 0.0390625
        out = []
        for end in range(self.stride, len(spectrogram) + 1, self.stride):
            chunk = spectrogram[end - self.stride : end] / scale + zero
            it.set_tensor(inp["index"], np.reshape(chunk.astype(np.int8), inp["shape"]))
            it.invoke()
            out.append(int(it.get_tensor(outp["index"])[0][0]))
        return out


def run_model(runner, negatives: list[Path], positives: list[Path]) -> Outputs:  # pragma: no cover
    neg = {str(p): runner.outputs(read_pcm(p)) for p in negatives}
    pos = {str(p): runner.outputs(padded(read_pcm(p))) for p in positives}
    seconds = sum(len(read_pcm(p)) for p in negatives) / DEVICE_RATE
    return Outputs(stride=runner.stride, negatives=neg, positives=pos, negative_seconds=seconds)


def report(
    candidate: tuple[str, dict, Outputs],
    deployed: tuple[str, dict, Outputs] | None,
    tolerance: float,
    split: str,
) -> dict:
    """The evaluation, as .wake/evaluation.json keeps it."""

    def scored(source: str, m: dict, out: Outputs) -> dict:
        micro = m["micro"]
        at = score(out, micro["probability_cutoff"], micro["sliding_window_size"])
        return {
            "source": source,
            "wake_word": m["wake_word"],
            "score": asdict(at),
            "sweep": [asdict(s) for s in sweep(out, micro["sliding_window_size"])],
        }

    c = scored(*candidate)
    d = scored(*deployed) if deployed else None
    ships, why = verdict(Score(**c["score"]), Score(**d["score"]) if d else None, tolerance)
    out = candidate[2]
    return {
        "split": split,
        "negatives": {"clips": len(out.negatives), "hours": round(out.negative_seconds / 3600, 4)},
        "positives": {"clips": len(out.positives)},
        "false_reject_tolerance": tolerance,
        "candidate": c,
        "deployed": d,
        "ships": ships,
        "why": why,
        "note": "false accepts an hour are over held-out hard negatives, the sounds the deployed "
        "model already woke on; they compare models and overstate a real room's rate",
    }
