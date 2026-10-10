"""The household voice tools, with stand-in TTS engines: no model is fetched.

What the Go tests in internal/reviewui/household check of the committed
clips (length, not silent, cut on a word) holds only if these hold first.
"""

import json
from pathlib import Path

import clone
import numpy as np
import pytest
import voice

SCRIPT = Path(__file__).resolve().parents[2] / "reviewui/household/voice.json"


def tone(seconds: float, pitch: float = 200.0, rate: int = voice.DEVICE_RATE) -> np.ndarray:
    t = np.arange(round(seconds * rate)) / rate
    return 0.3 * np.sin(2 * np.pi * pitch * t)


class Talker(voice.Speaker):
    """Speaks 14 characters a second, honestly faster when asked."""

    def __init__(self) -> None:
        self.asked: list[tuple[str, str, float]] = []

    def say(self, voice_name: str, text: str, speed: float = 1.0) -> np.ndarray:
        self.asked.append((voice_name, text, speed))
        return tone(len(text.strip()) / 14 / speed)


def decimate(samples: np.ndarray, rate: float) -> np.ndarray:
    """A crude stretch: keep every rate-th sample."""
    return samples[np.floor(np.arange(0, len(samples), rate)).astype(int)]


def frames(seconds: float) -> int:
    return round(seconds * voice.DEVICE_RATE)


@pytest.fixture
def script() -> dict:
    return json.loads(SCRIPT.read_text())


def test_a_short_line_is_padded_to_its_slot():
    # 19 characters is 1.36 s at 14/s; the slot is 1.6 s.
    out = Talker().fit("af_sarah", "lock the front door", frames(1.6))
    assert len(out) == frames(1.6)
    assert np.all(out[: frames(voice.LEAD_S)] == 0), "playback starts on silence"
    assert np.abs(out).max() > 0.1


def test_a_long_line_is_spoken_faster_until_it_fits():
    talker = Talker()
    # 30 characters is 2.1 s at 14/s; the slot is 1.6 s.
    out = talker.fit("am_michael", "set a timer for twelve minutes", frames(1.6))
    assert len(out) == frames(1.6)
    speeds = [s for _, _, s in talker.asked]
    assert speeds[0] == 1.0 and 1.0 < speeds[-1] <= voice.MAX_SPEED


def test_a_line_too_long_even_at_top_speed_is_cut_with_a_fade():
    out = Talker().fit("am_michael", "set a timer for twelve minutes", frames(0.5))
    assert len(out) == frames(0.5)
    assert abs(out[-1]) < 1e-3


def test_only_the_people_asked_for_are_rewritten(tmp_path, script):
    (tmp_path / "mic").mkdir()
    untouched = tmp_path / "mic/weather-ask.pcm"
    untouched.write_bytes(b"teagan's take")

    written = voice.generate(script, Talker(), tmp_path, {"alan"})

    alan = [c for c in script["clips"] if c.get("who") == "alan"]
    assert sorted(p.name for p in written) == sorted(c["ref"].split("/")[1] + ".pcm" for c in alan)
    for c in alan:
        pcm = np.frombuffer((tmp_path / (c["ref"] + ".pcm")).read_bytes(), dtype="<i2")
        assert len(pcm) == frames(c["seconds"]), c["ref"]
        assert np.abs(pcm).max() > 1000, f"{c['ref']} is silent"
    assert untouched.read_bytes() == b"teagan's take"


def test_everyone_is_rewritten_when_nobody_is_named(tmp_path, script):
    written = voice.generate(script, Talker(), tmp_path)
    assert len(written) == len(script["clips"])
    # The dishwasher has no voice and still comes out its scripted length.
    pcm = (tmp_path / "wake/kitchen-dishwasher.pcm").read_bytes()
    dishwasher = next(c for c in script["clips"] if c["ref"] == "wake/kitchen-dishwasher")
    assert len(pcm) == 2 * frames(dishwasher["seconds"])


def test_a_cut_take_lands_its_cut_on_the_heard_frame(tmp_path, script):
    voice.generate(script, Talker(), tmp_path, {"assistant"})
    clip = next(c for c in script["clips"] if c["ref"] == "tts/jazz-playlist")
    pcm = np.frombuffer((tmp_path / "tts/jazz-playlist.pcm").read_bytes(), dtype="<i2")
    cut = clip["heard_frames"]
    assert len(pcm) == frames(clip["seconds"])
    # The heard half fades out by the cut and the unheard half opens on silence.
    assert abs(int(pcm[cut - 1])) < 100
    assert np.all(pcm[cut : cut + frames(voice.LEAD_S)] == 0)


def test_a_clone_reads_each_line_once_however_often_fit_asks(tmp_path):
    ref = tmp_path / "alan-kitchen.wav"
    ref.write_bytes(b"RIFF")
    takes = []

    def generate(text: str, reference: str) -> tuple[np.ndarray, int]:
        takes.append((text, reference))
        # Chatterbox speaks at 24 kHz, slower than the slot allows.
        return tone(len(text) / 10, pitch=120, rate=24_000), 24_000

    speaker = clone.CloneSpeaker(generate, decimate)
    out = speaker.fit(str(ref), "set a timer for twelve minutes", frames(1.6))

    assert takes == [("set a timer for twelve minutes", str(ref))]
    assert len(out) == frames(1.6)
    assert np.abs(out).max() > 0.1


def test_a_clone_speaks_only_the_people_it_has_a_voice_for(tmp_path, script):
    ref = tmp_path / "teagan-office.wav"
    ref.write_bytes(b"RIFF")
    refs = clone.parse_voices([f"teagan={ref}"], script["voices"])
    heard = []

    def generate(text: str, reference: str) -> tuple[np.ndarray, int]:
        heard.append(reference)
        return tone(len(text) / 14, rate=24_000), 24_000

    cloned = {**script, "voices": {**script["voices"], **refs}}
    written = voice.generate(cloned, clone.CloneSpeaker(generate, decimate), tmp_path, set(refs))

    teagan = [c for c in script["clips"] if c.get("who") == "teagan"]
    assert len(written) == len(teagan) == 6
    assert set(heard) == {str(ref.resolve())}


@pytest.mark.parametrize(
    ("pair", "why"),
    [
        ("teagan", "want who=reference.wav"),
        ("=teagan.wav", "want who=reference.wav"),
        ("bob=teagan.wav", "bob is not one of"),
        ("alan=missing.wav", "no such file"),
    ],
)
def test_a_bad_voice_is_refused_before_any_model_loads(pair, why, script):
    with pytest.raises(ValueError, match=why):
        clone.parse_voices([pair], script["voices"])


def test_the_same_line_seeds_the_same_reading():
    a = clone.seed_for("no, wait", "alan-kitchen.wav")
    assert a == clone.seed_for("no, wait", "alan-kitchen.wav")
    assert a != clone.seed_for("something quieter", "alan-kitchen.wav")
    assert a != clone.seed_for("no, wait", "teagan-office.wav")
