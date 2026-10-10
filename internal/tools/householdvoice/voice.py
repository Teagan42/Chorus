"""Speak the review UI's household day into the clips its journal cites.

The household fixture (internal/reviewui/household) is a Thursday the browser
tests and the hosted demo both walk through. Its journal refers to audio by
blob ref; this script renders each ref from voice.json with Kokoro, the TTS
chorusd itself speaks with, and writes it as device audio: 16 kHz s16le mono,
exactly as long as the fixture says, so every duration the tests assert holds.

A cut clip is rendered in two halves, so the barge-in lands on the word
boundary the journal recorded rather than wherever a fitted sentence puts it.

    task household:voice                     # everyone, in Kokoro
    task household:voice -- --who alice      # only Alice's clips

The model files are fetched once into a cache directory (not the repo). To
speak someone in a cloned voice instead, see clone.py; --who then keeps
this script off the clips it made.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import urllib.request
from pathlib import Path

import numpy as np

DEVICE_RATE = 16_000
RELEASE = "https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/"
MODEL_FILES = ("kokoro-v1.0.int8.onnx", "voices-v1.0.bin")

# Faster than this and a fitted line stops sounding like a person.
MAX_SPEED = 1.35
# Silence a clip opens on, so playback does not start mid-syllable.
LEAD_S = 0.05


def fetch(cache: Path) -> tuple[Path, Path]:
    cache.mkdir(parents=True, exist_ok=True)
    paths = []
    for name in MODEL_FILES:
        p = cache / name
        if not p.exists():
            print(f"fetching {name}")
            urllib.request.urlretrieve(RELEASE + name, p)
        paths.append(p)
    return paths[0], paths[1]


def resample(x: np.ndarray, src: int, dst: int) -> np.ndarray:
    """Band-limited resample; clips are seconds long, so one FFT is fine."""
    n = round(len(x) * dst / src)
    spec = np.fft.rfft(x)[: n // 2 + 1]
    return np.fft.irfft(spec, n) * (n / len(x))


def trim(x: np.ndarray, floor: float = 0.01) -> np.ndarray:
    loud = np.nonzero(np.abs(x) > floor)[0]
    return x[loud[0] : loud[-1] + 1] if len(loud) else x


class Speaker:
    """A TTS that says text in a voice at a speed, as 16 kHz float samples."""

    def say(self, voice: str, text: str, speed: float = 1.0) -> np.ndarray:
        raise NotImplementedError

    def fit(self, voice: str, text: str, frames: int) -> np.ndarray:
        """Speak text into exactly frames, faster if it must be, padded if not."""
        room = frames - round(LEAD_S * DEVICE_RATE)
        speed = 1.0
        speech = self.say(voice, text)
        # A TTS's speed is not quite proportional to length, so converge on it.
        while len(speech) > room and speed < MAX_SPEED:
            speed = min(MAX_SPEED, speed * 1.03 * len(speech) / room)
            speech = self.say(voice, text, speed)
        if len(speech) > room:
            lost = (len(speech) - room) / DEVICE_RATE
            print(f"  {text.strip()!r} loses {lost:.2f} s at {speed:.2f}x")
        return place(speech, frames)


class KokoroSpeaker(Speaker):
    def __init__(self, model: Path, voices: Path) -> None:
        # Imported here so clone.py can reuse this module without Kokoro.
        from kokoro_onnx import Kokoro

        self.kokoro = Kokoro(str(model), str(voices))

    def say(self, voice: str, text: str, speed: float = 1.0) -> np.ndarray:
        lang = "en-gb" if voice.startswith("b") else "en-us"
        audio, rate = self.kokoro.create(text.strip(), voice=voice, speed=speed, lang=lang)
        return resample(trim(np.asarray(audio, dtype=np.float64)), rate, DEVICE_RATE)


def place(speech: np.ndarray, frames: int) -> np.ndarray:
    out = np.zeros(frames)
    lead = min(round(LEAD_S * DEVICE_RATE), max(0, frames - len(speech)))
    body = speech[: frames - lead]
    if len(body) < len(speech):
        fade = min(len(body), DEVICE_RATE // 50)
        body[-fade:] *= np.linspace(1, 0, fade)
    out[lead : lead + len(body)] = body
    return out


def rng_for(ref: str) -> np.random.Generator:
    return np.random.default_rng(int.from_bytes(hashlib.sha256(ref.encode()).digest()[:8]))


def room(x: np.ndarray, ref: str) -> np.ndarray:
    """A far-field mic: quieter than the speaker, over a little room noise."""
    hiss = np.cumsum(rng_for(ref).normal(0, 1, len(x)))
    hiss -= np.convolve(hiss, np.ones(400) / 400, mode="same")
    hiss *= 10 ** (-52 / 20) / (np.std(hiss) or 1)
    return 0.6 * x + hiss


def dishwasher(frames: int, ref: str) -> np.ndarray:
    """Water against a door: low rumble swelling twice a second."""
    noise = np.cumsum(rng_for(ref).normal(0, 1, frames))
    noise -= np.convolve(noise, np.ones(160) / 160, mode="same")
    noise /= np.max(np.abs(noise)) or 1
    t = np.arange(frames) / DEVICE_RATE
    return 0.25 * noise * (0.55 + 0.45 * np.sin(2 * np.pi * 2.1 * t) ** 2)


def render(clip: dict, voices: dict, speaker: Speaker) -> np.ndarray:
    frames = round(clip["seconds"] * DEVICE_RATE)
    ref = clip["ref"]
    if clip.get("noise") == "dishwasher":
        return dishwasher(frames, ref)
    voice = voices[clip["who"]]
    if clip["who"] == "podcast":
        # The second stage heard a slice of someone else's sentence.
        speech = speaker.say(voice, clip["say"])
        start = max(0, (len(speech) - frames) // 2)
        return room(0.5 * place(speech[start:], frames), ref)
    if "unheard" in clip:
        cut = clip["heard_frames"]
        heard = speaker.fit(voice, clip["say"], cut)
        rest = speaker.fit(voice, clip["unheard"], frames - cut)
        return np.concatenate([heard, rest])
    out = speaker.fit(voice, clip["say"], frames)
    return room(out, ref) if ref.startswith("mic/") else out


def generate(script: dict, speaker: Speaker, out: Path, who: set[str] | None = None) -> list[Path]:
    """Write each clip as device PCM; with who, only those people's clips."""
    written = []
    for clip in script["clips"]:
        if who is not None and clip.get("who") not in who:
            continue
        pcm = render(clip, script["voices"], speaker)
        path = out / (clip["ref"] + ".pcm")
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes((np.clip(pcm, -1, 1) * 32767).astype("<i2").tobytes())
        print(f"{clip['ref']:28} {len(pcm) / DEVICE_RATE:.2f} s")
        written.append(path)
    return written


def main() -> None:
    here = Path(__file__).resolve().parents[3]
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--script", type=Path, default=here / "internal/reviewui/household/voice.json")
    ap.add_argument("--out", type=Path, default=here / "internal/reviewui/household/audio")
    ap.add_argument("--cache", type=Path, default=Path.home() / ".cache/chorus/kokoro")
    ap.add_argument(
        "--who",
        action="append",
        help="only this person's clips (a voices key in the script); repeat for more",
    )
    args = ap.parse_args()

    script = json.loads(args.script.read_text())
    unknown = set(args.who or ()) - set(script["voices"])
    if unknown:
        ap.error(f"--who {', '.join(sorted(unknown))}: not in {args.script.name}")
    speaker = KokoroSpeaker(*fetch(args.cache))
    generate(script, speaker, args.out, set(args.who) if args.who else None)


if __name__ == "__main__":
    main()
