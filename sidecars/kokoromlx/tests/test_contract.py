"""The HTTP contract, against a fake model: no MLX, no weights, no network."""

import io
import wave

import numpy as np
import pytest
from fastapi.testclient import TestClient

from kokoromlx.app import MAX_INPUT, MAX_SPEED, MIN_SPEED, create_app
from kokoromlx.voice import MODEL, SAMPLE_RATE


class FakeVoice:
    """Renders a fixed tone and keeps what it was asked for."""

    model = MODEL
    sample_rate = SAMPLE_RATE

    def __init__(self, audio: np.ndarray | None = None) -> None:
        self.audio = audio if audio is not None else np.zeros(480, dtype=np.float32)
        self.asked: list[tuple[str, str, float]] = []

    def voices(self) -> list[str]:
        return ["af_heart", "am_adam", "bf_alice"]

    def render(self, text: str, voice: str, speed: float) -> np.ndarray:
        self.asked.append((text, voice, speed))
        return self.audio


class BrokenVoice(FakeVoice):
    def render(self, text: str, voice: str, speed: float) -> np.ndarray:
        raise RuntimeError("mlx: metal device is unavailable")


@pytest.fixture
def voice() -> FakeVoice:
    return FakeVoice()


@pytest.fixture
def client(voice: FakeVoice) -> TestClient:
    return TestClient(create_app(voice), raise_server_exceptions=False)


def post(client: TestClient, **body):
    return client.post("/v1/audio/speech", json={"model": MODEL, "input": "Okay.", **body})


# What the Go client asks for and what it decodes: raw samples, no header, and
# the request's own words and voice reaching the model unaltered.
#
# verifies SPEC §10
def test_speech_is_raw_little_endian_pcm(client: TestClient, voice: FakeVoice) -> None:
    voice.audio = np.array([0.0, 0.5, -0.5, 1.0, -1.0], dtype=np.float32)
    r = post(client, input="Turn off the kitchen lights.", voice="am_adam", speed=1.1)
    assert r.status_code == 200, r.text
    assert r.headers["content-type"] == "audio/pcm"
    assert np.frombuffer(r.content, dtype="<i2").tolist() == [0, 16384, -16384, 32767, -32767]
    assert voice.asked == [("Turn off the kitchen lights.", "am_adam", 1.1)]


# A sample at full scale must not come back as its opposite: scaling by 32768
# wraps +1.0 to -32768, which is a click in the middle of a word.
def test_full_scale_does_not_wrap(client: TestClient, voice: FakeVoice) -> None:
    voice.audio = np.array([1.0, 1.2, -1.2], dtype=np.float32)
    r = post(client)
    assert np.frombuffer(r.content, dtype="<i2").tolist() == [32767, 32767, -32767]


# The WAV is for a person with curl. Upstream's lies about its data chunk; this
# one is checked against the bytes it carries so no parser has to guess.
def test_wav_declares_the_length_it_carries(client: TestClient, voice: FakeVoice) -> None:
    voice.audio = np.zeros(1200, dtype=np.float32)
    r = post(client, response_format="wav")
    assert r.status_code == 200, r.text
    assert r.headers["content-type"] == "audio/wav"
    with wave.open(io.BytesIO(r.content)) as w:
        assert w.getnchannels() == 1
        assert w.getsampwidth() == 2
        assert w.getframerate() == SAMPLE_RATE
        assert w.getnframes() == 1200


# The client sends stream: false; upstream defaults it to true. Either way the
# whole render is the response, so neither can get a truncated utterance.
@pytest.mark.parametrize("stream", [True, False])
def test_stream_is_accepted_either_way(client: TestClient, voice: FakeVoice, stream: bool) -> None:
    voice.audio = np.zeros(480, dtype=np.float32)
    r = post(client, stream=stream)
    assert r.status_code == 200, r.text
    assert len(r.content) == 960


# A failed render ends the utterance at the satellite, so every refusal has to
# say which of these it was -- they are all 400 and only the body differs.
@pytest.mark.parametrize(
    ("body", "word"),
    [
        ({"model": "kokoro-v1"}, "this serves"),
        ({"voice": "af_nobody"}, "unknown voice"),
        ({"response_format": "mp3"}, "response format"),
        ({"speed": MIN_SPEED / 2}, "speed"),
        ({"speed": MAX_SPEED + 1}, "speed"),
        ({"input": ""}, "no speakable text"),
        ({"input": "   \n "}, "no speakable text"),
        ({"input": "a" * (MAX_INPUT + 1)}, "limit"),
    ],
)
def test_an_unrenderable_request_is_a_400_that_says_why(
    client: TestClient, voice: FakeVoice, body: dict, word: str
) -> None:
    r = post(client, **body)
    assert r.status_code == 400, r.text
    assert word in r.json()["detail"]
    assert voice.asked == []


# The voice list is 54 names long. Quoting it whole would fill the client's
# 1 KiB of error body and the log line with it.
def test_an_unknown_voice_is_not_the_whole_list(client: TestClient) -> None:
    detail = post(client, voice="af_nobody").json()["detail"]
    assert len(detail) < 1024
    assert "3 available" in detail


# What startup checks a configured voice against (kokoro.Config.Voice).
def test_voices_are_listed(client: TestClient) -> None:
    r = client.get("/v1/audio/voices")
    assert r.status_code == 200
    assert r.json() == {"voices": ["af_heart", "am_adam", "bf_alice"]}


# The satellite treats a failed render as the end of the utterance, so a dead
# GPU has to be a response and not a dropped connection.
def test_a_failing_render_is_a_500() -> None:
    client = TestClient(create_app(BrokenVoice()), raise_server_exceptions=False)
    assert post(client).status_code == 500


def test_health_declares_the_model(client: TestClient) -> None:
    r = client.get("/health")
    assert r.status_code == 200
    assert r.json() == {"status": "ok", "model": MODEL}
