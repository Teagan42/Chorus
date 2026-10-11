"""The canned-line renderer, with a stand-in TTS: no model is fetched."""

from pathlib import Path

import canned
import numpy as np
import voice


class Talker(voice.Speaker):
    """Speaks 14 characters a second as a tone, and remembers what it said."""

    def __init__(self) -> None:
        self.asked: list[tuple[str, str]] = []

    def say(self, voice_name: str, text: str, speed: float = 1.0) -> np.ndarray:
        self.asked.append((voice_name, text))
        t = np.arange(round(len(text) / 14 * voice.DEVICE_RATE)) / voice.DEVICE_RATE
        return 0.3 * np.sin(2 * np.pi * 200 * t)


MANIFEST = {
    "voice": "af_heart",
    "segments": [
        {"text": "Sorry, ", "file": "9975523217d82348.pcm"},
        {"text": "I've lost my voice for a moment.", "file": "27dc4ebc330578cd.pcm"},
    ],
}


def test_each_segment_is_written_as_device_audio_in_the_voice(tmp_path: Path):
    talker = Talker()
    written = canned.render(MANIFEST, talker, tmp_path)
    assert [p.name for p in written] == ["9975523217d82348.pcm", "27dc4ebc330578cd.pcm"]
    assert talker.asked == [
        ("af_heart", "Sorry, "),
        ("af_heart", "I've lost my voice for a moment."),
    ]
    pcm = np.frombuffer(written[0].read_bytes(), dtype="<i2")
    lead = round(canned.LEAD_S * voice.DEVICE_RATE)
    tail = round(canned.TAIL_S * voice.DEVICE_RATE)
    assert len(pcm) == lead + round(7 / 14 * voice.DEVICE_RATE) + tail
    assert np.all(pcm[:lead] == 0) and np.all(pcm[-tail:] == 0), "padded with silence"
    assert np.abs(pcm).max() > 0.2 * 32767


def test_a_file_no_segment_names_is_removed(tmp_path: Path):
    (tmp_path / "0000000000000000.pcm").write_bytes(b"\0\0")
    (tmp_path / "notes.txt").write_text("kept")
    canned.render(MANIFEST, Talker(), tmp_path)
    assert sorted(p.name for p in tmp_path.iterdir()) == [
        "27dc4ebc330578cd.pcm",
        "9975523217d82348.pcm",
        "notes.txt",
    ]
