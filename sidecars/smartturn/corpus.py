# /// script
# requires-python = "==3.11.*"
# dependencies = [
#   "chatterbox-tts==0.1.7",
#   # resemble-perth imports pkg_resources without declaring it, and newer
#   # setuptools no longer ship it; without it the watermarker is None.
#   "setuptools==80.9.0",
# ]
# ///
"""Render a turn corpus for the models tier with Chatterbox, on your own machine.

    uv run --script sidecars/smartturn/corpus.py --out .corpus/turns
    task test:models -- ./internal/provider/smartturn \\
        -smartturn-url http://127.0.0.1:8891 -smartturn-wavs .corpus/turns

Writes <out>/complete/*.wav and <out>/incomplete/*.wav at 16 kHz s16le mono,
the satellite's format. A standalone script, not a workspace member: torch
and the Chatterbox weights (from Hugging Face, on first run) stay out of the
repo's lockfile and image. The output is synthetic speech, never committed
(CONTRIBUTING §7); --voice clones a reference WAV if you want a household's
own voice in it.

Unfinished takes end on a comma: Chatterbox reads a trailing comma as more
to come, and a bare fragment would get a full stop and a falling pitch.
"""

import argparse
import wave
from collections.abc import Callable
from pathlib import Path

import numpy as np

SAMPLE_RATE = 16_000

# What a household says to a kitchen satellite, finished.
COMPLETE = {
    "kitchen-lights": "Turn off the kitchen lights.",
    "thermostat": "Set the thermostat to sixty eight degrees.",
    "weather": "What's the weather on Saturday?",
    "garage": "Is the garage door closed?",
    "timer": "Set a timer for twelve minutes.",
    "zeppelin": "Play something by Led Zeppelin.",
    "porch-light": "Turn on the porch light.",
    "remind-mom": "Remind me to call my mom at six.",
    "front-door": "Lock the front door.",
    "groceries": "Add oat milk and coffee filters to the shopping list.",
    "bedtime": "Good night.",
    "dishwasher": "How long is left on the dishwasher?",
}

# The same household, mid-sentence: a pause where the words are not done.
INCOMPLETE = {
    "kitchen-lights": "Turn off the,",
    "thermostat": "Set the thermostat to,",
    "weather": "What's the weather on,",
    "timer": "Set a timer for,",
    "remind-mom": "Remind me to call my mom and,",
    "kitchen-uh": "Turn off the, uh,",
    "zeppelin-um": "Play something by, um,",
    "groceries": "Add oat milk and,",
    "porch-light": "Turn on the porch light and the,",
    "garage-uh": "Is the garage door, uh,",
}


def pcm16(samples: np.ndarray) -> bytes:
    """Float samples in [-1, 1] to little-endian int16, clipped rather than wrapped."""
    clipped = np.clip(np.asarray(samples, dtype=np.float64), -1.0, 32767 / 32768)
    return (clipped * 32768).round().astype("<i2").tobytes()


def write_wav(path: Path, samples: np.ndarray) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(SAMPLE_RATE)
        w.writeframes(pcm16(samples))


def render(say: Callable[[str, str], np.ndarray], out: Path, voices: list[str]) -> list[Path]:
    """Say every take in every voice; say(text, voice) returns 16 kHz float samples."""
    written = []
    for kind, takes in (("complete", COMPLETE), ("incomplete", INCOMPLETE)):
        for name, text in takes.items():
            for voice in voices:
                path = out / kind / f"{name}.{voice}.wav"
                write_wav(path, say(text, voice))
                written.append(path)
    return written


def main() -> None:
    p = argparse.ArgumentParser(prog="corpus", description=__doc__.split("\n\n")[0])
    p.add_argument("--out", type=Path, default=Path(".corpus/turns"))
    p.add_argument("--device", default="cpu", help="cpu, mps or cuda")
    p.add_argument(
        "--voice",
        action="append",
        type=Path,
        default=[],
        help="reference WAV to clone; repeat for more voices. Default: Chatterbox's own",
    )
    p.add_argument("--exaggeration", type=float, default=0.5)
    args = p.parse_args()

    import librosa
    from chatterbox.tts import ChatterboxTTS

    model = ChatterboxTTS.from_pretrained(device=args.device)
    refs = {path.stem: str(path) for path in args.voice} or {"default": None}

    def say(text: str, voice: str) -> np.ndarray:
        wav = model.generate(text, audio_prompt_path=refs[voice], exaggeration=args.exaggeration)
        samples = wav.squeeze(0).cpu().numpy()
        return librosa.resample(samples, orig_sr=model.sr, target_sr=SAMPLE_RATE)

    for path in render(say, args.out, list(refs)):
        print(path)


if __name__ == "__main__":
    main()
