# /// script
# requires-python = "==3.11.*"
# dependencies = [
#   "chatterbox-tts==0.1.7",
#   # resemble-perth imports pkg_resources without declaring it, and newer
#   # setuptools no longer ship it; without it the watermarker is None.
#   "setuptools==80.9.0",
# ]
# ///
"""Speak some of the household's clips in cloned voices, with Chatterbox.

voice.py gives everyone a stock Kokoro voice. This gives the people you name
a voice cloned from a reference recording of them, and leaves every other
clip as it is, on your own machine:

    task household:voice:clone -- --voice teagan=teagan.wav --voice alan=alan.wav

A reference is 5-15 s of one person talking, alone, in any format Chatterbox
reads. Each clip is still fitted to the length voice.json gives it, so the
journal's timings and the household tests hold; Chatterbox has no speed
knob, so a long take is time-stretched instead (at most voice.MAX_SPEED).

The clips are committed and the demo is public, so clone only voices whose
owners said yes (CONTRIBUTING §7). A standalone script, like the Smart Turn
corpus: torch and the Chatterbox weights (from Hugging Face, on first run)
stay out of the workspace lockfile.
"""

from __future__ import annotations

import argparse
import hashlib
import json
from collections.abc import Callable
from pathlib import Path

import numpy as np
import voice

# say(text, reference) -> (samples, rate); one take, as the model gives it.
Generate = Callable[[str, str], tuple[np.ndarray, int]]
# stretch(samples, rate) -> samples, rate times shorter.
Stretch = Callable[[np.ndarray, float], np.ndarray]


class CloneSpeaker(voice.Speaker):
    """A voice is a reference recording's path."""

    def __init__(self, generate: Generate, stretch: Stretch) -> None:
        self.generate = generate
        self.stretch = stretch
        self.takes: dict[tuple[str, str], np.ndarray] = {}

    def say(self, ref: str, text: str, speed: float = 1.0) -> np.ndarray:
        # One take per line: fit asks again at each speed, and a fresh take
        # would be a different reading, not the same one faster.
        key = (ref, text.strip())
        if key not in self.takes:
            audio, rate = self.generate(text.strip(), ref)
            samples = voice.trim(np.asarray(audio, dtype=np.float64))
            self.takes[key] = voice.resample(samples, rate, voice.DEVICE_RATE)
        take = self.takes[key]
        return take if speed == 1.0 else self.stretch(take, speed)


def parse_voices(pairs: list[str], people: dict) -> dict[str, str]:
    """--voice who=path.wav pairs, checked against the script's people."""
    out = {}
    for pair in pairs:
        who, sep, path = pair.partition("=")
        if not sep or not who or not path:
            raise ValueError(f"--voice {pair}: want who=reference.wav")
        if who not in people:
            raise ValueError(f"--voice {pair}: {who} is not one of {', '.join(sorted(people))}")
        if not Path(path).is_file():
            raise ValueError(f"--voice {pair}: no such file")
        out[who] = str(Path(path).resolve())
    return out


def seed_for(text: str, ref: str) -> int:
    """The same line in the same reference reads the same on every run."""
    digest = hashlib.sha256(f"{ref}\x00{text}".encode()).digest()
    return int.from_bytes(digest[:4])


def chatterbox(device: str, exaggeration: float) -> tuple[Generate, Stretch]:
    import librosa
    import torch
    from chatterbox.tts import ChatterboxTTS

    model = ChatterboxTTS.from_pretrained(device=device)

    def generate(text: str, ref: str) -> tuple[np.ndarray, int]:
        torch.manual_seed(seed_for(text, Path(ref).name))
        wav = model.generate(text, audio_prompt_path=ref, exaggeration=exaggeration)
        return wav.squeeze(0).cpu().numpy(), model.sr

    def stretch(samples: np.ndarray, rate: float) -> np.ndarray:
        return librosa.effects.time_stretch(samples, rate=rate)

    return generate, stretch


def main() -> None:
    here = Path(__file__).resolve().parents[3]
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--script", type=Path, default=here / "internal/reviewui/household/voice.json")
    ap.add_argument("--out", type=Path, default=here / "internal/reviewui/household/audio")
    ap.add_argument(
        "--voice",
        action="append",
        required=True,
        help="who=reference.wav: speak who's clips in the voice of that recording; repeat",
    )
    ap.add_argument("--device", default="cpu", help="cpu, mps or cuda")
    ap.add_argument("--exaggeration", type=float, default=0.5)
    args = ap.parse_args()

    script = json.loads(args.script.read_text())
    try:
        refs = parse_voices(args.voice, script["voices"])
    except ValueError as err:
        ap.error(str(err))
    speaker = CloneSpeaker(*chatterbox(args.device, args.exaggeration))
    cloned = {**script, "voices": {**script["voices"], **refs}}
    voice.generate(cloned, speaker, args.out, set(refs))


if __name__ == "__main__":
    main()
