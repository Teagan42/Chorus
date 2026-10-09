"""The HTTP contract, against a fake model: no runtime, no network."""

import numpy as np
import pytest
from fastapi.testclient import TestClient

from smartturn.app import CONTENT_TYPE, MIN_SAMPLES, create_app


class FakeTurn:
    """Answers a fixed probability and keeps what it was handed."""

    model = "fake"

    def __init__(self, p: float = 0.93) -> None:
        self.p = p
        self.seen: list[np.ndarray] = []

    def probability(self, samples: np.ndarray) -> float:
        self.seen.append(samples)
        return self.p


class BrokenTurn(FakeTurn):
    def probability(self, samples: np.ndarray) -> float:
        raise RuntimeError("onnxruntime: input_features has the wrong shape")


def said(seconds: float) -> bytes:
    """Seconds of something voiced at the device's rate, as the satellite sends it."""
    t = np.arange(int(seconds * 16_000)) / 16_000
    return (np.sin(2 * np.pi * 140 * t) * 6000).astype("<i2").tobytes()


@pytest.fixture
def turn() -> FakeTurn:
    return FakeTurn()


@pytest.fixture
def client(turn: FakeTurn) -> TestClient:
    return TestClient(create_app(turn), raise_server_exceptions=False)


def post(client: TestClient, body: bytes, content_type: str = CONTENT_TYPE):
    return client.post("/v1/turn", content=body, headers={"Content-Type": content_type})


# "Turn off the kitchen lights" and a pause: the reply is the whole contract
# from the client's side, the verdict, how sure, and which checkpoint said so.
#
# verifies SPEC §4.5
def test_turn_answers_the_contract(client: TestClient) -> None:
    r = post(client, said(1.9))
    assert r.status_code == 200, r.text
    assert r.json() == {"complete": True, "probability": pytest.approx(0.93), "model": "fake"}


# "Turn off the... uh..." is not over. The verdict is upstream's cut on the
# probability, so the two can never disagree.
#
# verifies SPEC §4.5
@pytest.mark.parametrize(("p", "complete"), [(0.009, False), (0.5, False), (0.501, True)])
def test_complete_is_the_probability_past_one_half(p: float, complete: bool) -> None:
    client = TestClient(create_app(FakeTurn(p)), raise_server_exceptions=False)
    r = post(client, said(1.3))
    assert r.status_code == 200, r.text
    assert r.json()["complete"] is complete


# The body is little-endian int16 and nothing else: a byte-order or scaling
# mistake here would judge noise and still answer.
def test_pcm_is_decoded_as_little_endian_int16(client: TestClient, turn: FakeTurn) -> None:
    samples = np.array([0, 1, -1, 32767, -32768] + [0] * (MIN_SAMPLES - 5), dtype="<i2")
    r = post(client, samples.tobytes())
    assert r.status_code == 200, r.text
    seen = turn.seen[0]
    assert seen.dtype == np.float32
    assert seen[:5].tolist() == [0.0, 1 / 32768, -1 / 32768, 32767 / 32768, -1.0]


# A turn longer than the model's window is still accepted whole: what to keep
# is the model's business, not the client's.
def test_a_long_turn_is_handed_over_whole(client: TestClient, turn: FakeTurn) -> None:
    r = post(client, said(11.0))
    assert r.status_code == 200, r.text
    assert len(turn.seen[0]) == 11 * 16_000


# A WAV header judged as audio would not fail; it would just be wrong.
def test_anything_but_raw_pcm_is_refused(client: TestClient, turn: FakeTurn) -> None:
    r = post(client, said(1.0), content_type="audio/wav")
    assert r.status_code == 415
    assert "audio/pcm" in r.json()["detail"]
    assert turn.seen == []


def test_a_parameterised_content_type_still_counts(client: TestClient) -> None:
    r = post(client, said(1.0), content_type="audio/pcm; rate=16000")
    assert r.status_code == 200, r.text


@pytest.mark.parametrize(
    ("body", "word"),
    [
        (b"", "no audio"),
        (said(1.0)[:-1], "16-bit"),
        (said(1.0)[: 2 * (MIN_SAMPLES - 1)], "minimum"),
    ],
)
def test_unjudgeable_audio_is_a_400_that_says_why(
    client: TestClient, turn: FakeTurn, body: bytes, word: str
) -> None:
    r = post(client, body)
    assert r.status_code == 400
    assert word in r.json()["detail"]
    assert turn.seen == []


# The Go client falls back to waiting out the silence on any failure, so a
# model error has to be a response and not a dropped connection or a verdict.
def test_a_failing_model_is_a_500_not_a_verdict() -> None:
    client = TestClient(create_app(BrokenTurn()), raise_server_exceptions=False)
    assert post(client, said(1.0)).status_code == 500


@pytest.mark.parametrize("p", [float("nan"), -0.1, 1.2])
def test_an_answer_that_is_not_a_probability_is_a_500(p: float) -> None:
    client = TestClient(create_app(FakeTurn(p)), raise_server_exceptions=False)
    r = post(client, said(1.0))
    assert r.status_code == 500
    assert "probability" in r.json()["detail"]


# What compose waits on, and what an operator reads to know which checkpoint
# a container is serving.
def test_health_declares_the_model(client: TestClient) -> None:
    r = client.get("/health")
    assert r.status_code == 200
    assert r.json() == {"status": "ok", "model": "fake"}
