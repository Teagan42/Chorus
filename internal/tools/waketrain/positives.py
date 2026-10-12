"""Speak "hey eddie" in every English Kokoro voice, and blends of them, as wake-word positives.

microWakeWord learns the wake word from synthetic speech: its notebook uses
Piper's sample generator. This renders the positives with Kokoro instead,
the TTS chorusd already speaks with, over kokoro-onnx as task canned:voice
does, so nothing new is fetched. Variety comes from every English voice at
three speeds and three phrasings, plus blends of two voices' styles, which
are speakers Kokoro does not ship. Reverberation and background noise are
added later, as the trainer makes spectrograms (features.py).

    task wake:positives                    # into .wake/positives
    task wake:positives -- --blends 120    # more blended speakers

The clips are written as the satellite's audio, 16 kHz s16le mono WAV, in
training, validation and testing directories. Whole voices are held out,
so a model is scored on speakers it never heard. Blends are made only of
training voices. A manifest records every clip's voice, phrase and speed.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import shutil
import sys
import wave
from dataclasses import dataclass
from pathlib import Path

import numpy as np

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "householdvoice"))
import voice  # noqa: E402  (the household fixture's Kokoro fetch and resampler)

SCHEMA_VERSION = 1
PHRASES = ("Hey Eddie", "Hey, Eddie!", "Hey Eddie?")
SPEEDS = (0.85, 1.0, 1.2)
# American and British English: the wake word is said in English.
ENGLISH = ("af_", "am_", "bf_", "bm_")


class Engine:
    """A TTS whose voices are style vectors that can be mixed."""

    def voices(self) -> list[str]:
        raise NotImplementedError

    def style(self, name: str) -> np.ndarray:
        raise NotImplementedError

    def create(
        self, text: str, style: np.ndarray, speed: float, lang: str
    ) -> tuple[np.ndarray, int]:
        raise NotImplementedError


class KokoroEngine(Engine):  # pragma: no cover - needs the model files
    def __init__(self, model: Path, voices: Path) -> None:
        from kokoro_onnx import Kokoro

        self.kokoro = Kokoro(str(model), str(voices))

    def voices(self) -> list[str]:
        return list(self.kokoro.get_voices())

    def style(self, name: str) -> np.ndarray:
        return self.kokoro.get_voice_style(name)

    def create(
        self, text: str, style: np.ndarray, speed: float, lang: str
    ) -> tuple[np.ndarray, int]:
        return self.kokoro.create(text, voice=style, speed=speed, lang=lang)


@dataclass(frozen=True)
class Speaker:
    """A shipped voice, or two blended: name is "a+b@w", w being a's share."""

    name: str
    parts: tuple[tuple[str, float], ...]

    @property
    def lang(self) -> str:
        return "en-gb" if self.parts[0][0].startswith("b") else "en-us"

    @property
    def slug(self) -> str:
        return self.name.replace("+", "-").replace("@", "-")


def shipped(name: str) -> Speaker:
    return Speaker(name, ((name, 1.0),))


def blend(a: str, b: str, w: float) -> Speaker:
    return Speaker(f"{a}+{b}@{w:.2f}", ((a, w), (b, round(1 - w, 2))))


def english(names: list[str]) -> list[str]:
    return sorted(n for n in names if n.startswith(ENGLISH))


def _rank(name: str) -> str:
    return hashlib.sha256(name.encode()).hexdigest()


def split_voices(names: list[str], validation: float, testing: float) -> dict[str, str]:
    """Each shipped voice's split: ranked by the SHA-256 of its name, the
    first share is testing and the next validation, at least one each when
    asked for, so the held-out voices are the same on every run."""
    ranked = sorted(names, key=_rank)
    n_test = max(1, math.ceil(len(ranked) * testing)) if testing > 0 else 0
    n_val = max(1, math.ceil(len(ranked) * validation)) if validation > 0 else 0
    if n_test + n_val >= len(ranked):
        raise ValueError(
            f"{len(ranked)} voices cannot hold out {n_test} and {n_val} and still train"
        )
    out = {}
    for i, name in enumerate(ranked):
        out[name] = "testing" if i < n_test else "validation" if i < n_test + n_val else "training"
    return dict(sorted(out.items()))


def blends(training: list[str], n: int, seed: int) -> list[Speaker]:
    """n blends of two training voices each, the same for the same seed."""
    rng = np.random.default_rng(seed)
    out, seen = [], set()
    pool = sorted(training)
    while len(out) < n and len(pool) >= 2:
        a, b = (str(x) for x in rng.choice(pool, 2, replace=False))
        w = round(float(rng.uniform(0.25, 0.75)), 2)
        s = blend(a, b, w)
        if s.name not in seen:
            seen.add(s.name)
            out.append(s)
        if len(seen) > n * 10:
            break
    return out


def say(engine: Engine, speaker: Speaker, text: str, speed: float) -> np.ndarray:
    """Speech as device-rate float samples, trimmed of silence."""
    style = sum(w * engine.style(name) for name, w in speaker.parts)
    audio, rate = engine.create(text, np.asarray(style, dtype=np.float32), speed, speaker.lang)
    return voice.resample(voice.trim(np.asarray(audio, dtype=np.float64)), rate, voice.DEVICE_RATE)


def write_wav(path: Path, samples: np.ndarray) -> None:
    pcm = (np.clip(samples, -1, 1) * 32767).astype("<i2")
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(voice.DEVICE_RATE)
        w.writeframes(pcm.tobytes())


def prepare(out: Path) -> None:
    """Empty out of a previous run's clips; refuse a directory holding anything else."""
    if out.exists():
        for p in out.iterdir():
            if p.name != "manifest.json" and p.name not in ("training", "validation", "testing"):
                raise SystemExit(
                    f"{out} holds {p.name}: name an empty directory or a previous run's"
                )
            if p.is_dir():
                shutil.rmtree(p)
            else:
                p.unlink()
    out.mkdir(parents=True, exist_ok=True)


def generate(
    engine: Engine,
    out: Path,
    *,
    phrases: tuple[str, ...] = PHRASES,
    speeds: tuple[float, ...] = SPEEDS,
    n_blends: int = 60,
    validation: float = 0.1,
    testing: float = 0.1,
    seed: int = 78,
) -> dict:
    """Render every positive into out and return its manifest."""
    splits = split_voices(english(engine.voices()), validation, testing)
    training = [n for n, s in splits.items() if s == "training"]
    speakers = [(shipped(n), s) for n, s in splits.items()]
    speakers += [(b, "training") for b in blends(training, n_blends, seed)]
    prepare(out)
    clips = []
    for speaker, split in speakers:
        (out / split).mkdir(exist_ok=True)
        for i, phrase in enumerate(phrases):
            for speed in speeds:
                samples = say(engine, speaker, phrase, speed)
                rel = f"{split}/{speaker.slug}_{i}_{speed:.2f}.wav"
                write_wav(out / rel, samples)
                clips.append(
                    {
                        "path": rel,
                        "voice": speaker.name,
                        "phrase": phrase,
                        "speed": speed,
                        "split": split,
                        "seconds": round(len(samples) / voice.DEVICE_RATE, 3),
                    }
                )
    m = {
        "schema_version": SCHEMA_VERSION,
        "engine": "Kokoro v1.0 over kokoro-onnx (" + ", ".join(voice.MODEL_FILES) + ")",
        "audio": "WAV, 16 kHz, 16-bit signed, mono",
        "split": "shipped voices ranked by sha256(name): the first testing_fraction are testing, "
        "the next validation_fraction validation; blends are of training voices and train",
        "validation_fraction": validation,
        "testing_fraction": testing,
        "seed": seed,
        "phrases": list(phrases),
        "speeds": list(speeds),
        "voices": splits,
        "clips": clips,
    }
    (out / "manifest.json").write_text(json.dumps(m, indent=2) + "\n")
    return m


def main() -> None:  # pragma: no cover - fetches the model
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--out", type=Path, default=Path(".wake/positives"))
    ap.add_argument("--cache", type=Path, default=Path.home() / ".cache/chorus/kokoro")
    ap.add_argument(
        "--blends", type=int, default=60, help="blended speakers beyond the shipped voices"
    )
    ap.add_argument("--validation", type=float, default=0.1)
    ap.add_argument("--testing", type=float, default=0.1)
    args = ap.parse_args()
    engine = KokoroEngine(*voice.fetch(args.cache))
    m = generate(
        engine, args.out, n_blends=args.blends, validation=args.validation, testing=args.testing
    )
    by_split: dict[str, int] = {}
    for c in m["clips"]:
        by_split[c["split"]] = by_split.get(c["split"], 0) + 1
    print(f"{len(m['clips'])} positives in {args.out}: {by_split}")


if __name__ == "__main__":
    main()
