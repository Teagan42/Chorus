"""The corpus script's own glue, with a fake voice: Chatterbox and its
weights never load here."""

import importlib.util
import wave
from pathlib import Path

import numpy as np

SCRIPT = Path(__file__).parent.parent / "corpus.py"
spec = importlib.util.spec_from_file_location("corpus", SCRIPT)
corpus = importlib.util.module_from_spec(spec)
spec.loader.exec_module(corpus)


def read(path: Path) -> tuple[tuple[int, int, int], np.ndarray]:
    with wave.open(str(path), "rb") as w:
        shape = (w.getframerate(), w.getnchannels(), w.getsampwidth())
        return shape, np.frombuffer(w.readframes(w.getnframes()), dtype="<i2")


# Every take lands where the models tier looks, in the satellite's format,
# once per voice.
def test_render_writes_the_models_tier_layout(tmp_path: Path) -> None:
    said = []

    def say(text: str, voice: str) -> np.ndarray:
        said.append((text, voice))
        return np.full(1600, 0.25, dtype=np.float32)

    written = corpus.render(say, tmp_path, ["teagan", "alan"])
    want = 2 * (len(corpus.COMPLETE) + len(corpus.INCOMPLETE))
    assert len(written) == len(said) == want
    assert (tmp_path / "complete" / "kitchen-lights.teagan.wav") in written
    assert (tmp_path / "incomplete" / "kitchen-uh.alan.wav") in written
    shape, pcm = read(tmp_path / "complete" / "thermostat.alan.wav")
    assert shape == (16_000, 1, 2)
    assert len(pcm) == 1600 and (pcm == 8192).all()


# A cut-off take must not end like a sentence, or the voice's falling pitch
# says "finished" and the corpus measures the TTS, not the model.
def test_unfinished_takes_end_on_a_comma() -> None:
    assert all(text.endswith(",") for text in corpus.INCOMPLETE.values())
    assert all(text[-1] in ".?" for text in corpus.COMPLETE.values())


# Loud TTS peaks clip at full scale instead of wrapping to the other sign.
def test_pcm16_clips_rather_than_wraps() -> None:
    pcm = np.frombuffer(corpus.pcm16(np.array([1.5, -1.5, 0.5, -1.0])), dtype="<i2")
    assert pcm.tolist() == [32767, -32768, 16384, -32768]
