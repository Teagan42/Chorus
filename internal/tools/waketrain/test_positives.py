"""The positive generator, with a stand-in Kokoro: no model is fetched."""

import json
import wave

import numpy as np
import positives
import pytest

KOKORO_RATE = 24_000
VOICES = [
    "af_heart", "af_sarah", "af_bella", "af_nicole", "am_michael", "am_adam", "am_eric",
    "bf_emma", "bf_isabella", "bm_george", "bm_daniel", "bm_lewis",
    "jf_alpha", "zf_xiaobei", "ef_dora",
]  # fmt: skip


class Kokoro(positives.Engine):
    """Speaks 14 characters a second as a 440 Hz tone at Kokoro's 24 kHz."""

    def __init__(self) -> None:
        self.asked: list[tuple[str, float, str, np.ndarray]] = []

    def voices(self) -> list[str]:
        return VOICES

    def style(self, name: str) -> np.ndarray:
        return np.full(4, float(VOICES.index(name)), dtype=np.float32)

    def create(self, text, style, speed, lang):
        self.asked.append((text, speed, lang, style))
        n = round(len(text) / 14 / speed * KOKORO_RATE)
        t = np.arange(n) / KOKORO_RATE
        return (0.5 * np.sin(2 * np.pi * 440 * t)).astype(np.float32), KOKORO_RATE


def read(path):
    with wave.open(str(path), "rb") as w:
        assert (w.getframerate(), w.getnchannels(), w.getsampwidth()) == (16_000, 1, 2)
        return np.frombuffer(w.readframes(w.getnframes()), dtype="<i2").astype(np.float64) / 32767


def test_kokoros_24_khz_speech_is_written_as_16_khz_device_audio(tmp_path):
    m = positives.generate(Kokoro(), tmp_path, phrases=("Hey Eddie",), speeds=(1.0,), n_blends=0)
    first = m["clips"][0]
    samples = read(tmp_path / first["path"])
    # 9 characters at 14 a second, resampled, not stretched.
    assert abs(len(samples) - 9 / 14 * 16_000) <= 2
    assert first["seconds"] == pytest.approx(9 / 14, abs=1e-3)
    spectrum = np.abs(np.fft.rfft(samples))
    peak = np.argmax(spectrum) * 16_000 / len(samples)
    assert abs(peak - 440) < 5, "the pitch survives the resample"
    assert 0.4 < np.abs(samples).max() <= 0.51


def test_only_english_voices_speak_and_whole_voices_are_held_out(tmp_path):
    m = positives.generate(Kokoro(), tmp_path, n_blends=0)
    assert set(m["voices"]) == {v for v in VOICES if v[:3] in ("af_", "am_", "bf_", "bm_")}
    held = {v: s for v, s in m["voices"].items() if s != "training"}
    assert sorted(held.values()) == ["testing"] * 2 + ["validation"] * 2, (
        "a tenth of 12, rounded up"
    )
    for c in m["clips"]:
        assert c["split"] == m["voices"][c["voice"]]
        assert c["path"].startswith(c["split"] + "/")
    assert len(m["clips"]) == 12 * len(positives.PHRASES) * len(positives.SPEEDS)
    on_disk = json.loads((tmp_path / "manifest.json").read_text())
    assert on_disk == m


def test_the_split_is_the_same_on_every_run_and_only_grows():
    names = positives.english(VOICES)
    a = positives.split_voices(names, 0.1, 0.1)
    assert a == positives.split_voices(list(reversed(names)), 0.1, 0.1)
    wider = positives.split_voices(names, 0.1, 0.3)
    assert {v for v, s in a.items() if s == "testing"} <= {
        v for v, s in wider.items() if s == "testing"
    }
    with pytest.raises(ValueError):
        positives.split_voices(names[:2], 0.5, 0.5)


def test_blends_mix_two_training_voices_styles(tmp_path):
    kokoro = Kokoro()
    m = positives.generate(kokoro, tmp_path, phrases=("Hey Eddie",), speeds=(1.0,), n_blends=5)
    blended = [c for c in m["clips"] if "+" in c["voice"]]
    assert len(blended) == 5
    training = {v for v, s in m["voices"].items() if s == "training"}
    for c in blended:
        assert c["split"] == "training"
        a, rest = c["voice"].split("+")
        b, w = rest.split("@")
        assert a in training and b in training and a != b
        assert 0.25 <= float(w) <= 0.75
    # A blend's style is the weighted sum of its two voices'.
    s = positives.blend("af_heart", "bm_george", 0.25)
    kokoro.asked.clear()
    positives.say(kokoro, s, "Hey Eddie", 1.0)
    want = 0.25 * VOICES.index("af_heart") + 0.75 * VOICES.index("bm_george")
    assert np.allclose(kokoro.asked[0][3], want)
    assert kokoro.asked[0][2] == "en-us", "the first voice's accent"
    assert positives.blends(sorted(training), 5, 78) == positives.blends(sorted(training), 5, 78)


def test_british_voices_speak_british_english(tmp_path):
    kokoro = Kokoro()
    positives.say(kokoro, positives.shipped("bf_emma"), "Hey Eddie", 1.0)
    positives.say(kokoro, positives.shipped("am_adam"), "Hey Eddie", 1.0)
    assert [lang for _, _, lang, _ in kokoro.asked] == ["en-gb", "en-us"]


def test_a_rerun_replaces_the_last_clips_and_refuses_a_foreign_directory(tmp_path):
    positives.generate(Kokoro(), tmp_path, phrases=("Hey Eddie",), speeds=(1.0,), n_blends=0)
    stale = tmp_path / "training" / "old.wav"
    stale.write_bytes(b"RIFF")
    positives.generate(Kokoro(), tmp_path, phrases=("Hey Eddie",), speeds=(1.0,), n_blends=0)
    assert not stale.exists()
    other = tmp_path / "elsewhere"
    other.mkdir()
    (other / "notes.txt").write_text("kept")
    with pytest.raises(SystemExit):
        positives.generate(Kokoro(), other, n_blends=0)
    assert (other / "notes.txt").read_text() == "kept"
