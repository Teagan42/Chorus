"""Render chorusd's canned lines into the audio it falls back on.

chorusd renders its canned lines with Kokoro at startup and keeps them, so a
voice that fails can still apologise (SPEC §7). Until that render succeeds,
it says them from audio built into the binary: this script renders that
audio, one file per segment the satellite cuts a line into, in the voice
chorusd speaks with, as device audio (16 kHz s16le mono). The manifest of
segments comes from Go, which owns how a line is cut (ADR-0069):

    task canned:voice

Stale files in the output directory are removed, so a reworded line leaves
nothing behind. The model files are fetched once into a cache directory.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import numpy as np
import voice

# Silence around each segment, as a live render has, so that segments
# played back to back do not run into each other.
LEAD_S = 0.03
TAIL_S = 0.08


def pad(speech: np.ndarray) -> np.ndarray:
    lead = np.zeros(round(LEAD_S * voice.DEVICE_RATE))
    tail = np.zeros(round(TAIL_S * voice.DEVICE_RATE))
    return np.concatenate([lead, speech, tail])


def render(manifest: dict, speaker: voice.Speaker, out: Path) -> list[Path]:
    """Write each segment as device PCM, and remove any file not listed."""
    out.mkdir(parents=True, exist_ok=True)
    listed = {seg["file"] for seg in manifest["segments"]}
    for stale in sorted(out.glob("*.pcm")):
        if stale.name not in listed:
            stale.unlink()
            print(f"removed {stale.name}")
    written = []
    for seg in manifest["segments"]:
        pcm = pad(speaker.say(manifest["voice"], seg["text"]))
        path = out / seg["file"]
        path.write_bytes((np.clip(pcm, -1, 1) * 32767).astype("<i2").tobytes())
        print(f"{seg['file']} {len(pcm) / voice.DEVICE_RATE:.2f} s  {seg['text']!r}")
        written.append(path)
    return written


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--cache", type=Path, default=Path.home() / ".cache/chorus/kokoro")
    args = ap.parse_args()
    manifest = json.load(sys.stdin)
    speaker = voice.KokoroSpeaker(*voice.fetch(args.cache))
    render(manifest, speaker, args.out)


if __name__ == "__main__":
    main()
