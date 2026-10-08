"""The HTTP contract, against a fake embedder: no model, no network, no GPU."""

import numpy as np
import pytest
from fastapi.testclient import TestClient

from speakerid.app import CONTENT_TYPE, MIN_SAMPLES, create_app
from speakerid.embedder import unit


class FakeEmbedder:
    """Returns a fixed direction and keeps what it was handed."""

    model = "fake"
    dim = 4

    def __init__(self) -> None:
        self.seen: list[np.ndarray] = []

    def embed(self, samples: np.ndarray) -> np.ndarray:
        self.seen.append(samples)
        return unit(np.array([1.0, 2.0, 3.0, 4.0], dtype=np.float32))


class BrokenEmbedder(FakeEmbedder):
    def embed(self, samples: np.ndarray) -> np.ndarray:
        raise RuntimeError("cuda out of memory")


def pcm(n: int) -> bytes:
    """n whole samples of a ramp, as the device would send them."""
    return (np.arange(n, dtype="<i2") * 7).tobytes()


@pytest.fixture
def embedder() -> FakeEmbedder:
    return FakeEmbedder()


@pytest.fixture
def client(embedder: FakeEmbedder) -> TestClient:
    return TestClient(create_app(embedder), raise_server_exceptions=False)


def post(client: TestClient, body: bytes, content_type: str = CONTENT_TYPE):
    return client.post("/v1/embed", content=body, headers={"Content-Type": content_type})


# The reply is the whole contract from the client's side: the vector, and the
# width and checkpoint it should be checked against.
#
# verifies SPEC §10
def test_embed_answers_the_contract(client: TestClient) -> None:
    r = post(client, pcm(MIN_SAMPLES))
    assert r.status_code == 200, r.text
    body = r.json()
    assert set(body) == {"embedding", "dim", "model"}
    assert body["dim"] == 4
    assert body["model"] == "fake"
    assert len(body["embedding"]) == 4
    assert abs(float(np.linalg.norm(body["embedding"])) - 1.0) < 1e-6


# The body is little-endian int16 and nothing else: a byte-order or scaling
# mistake here would embed noise that still has the right shape.
def test_pcm_is_decoded_as_little_endian_int16(client: TestClient, embedder: FakeEmbedder) -> None:
    samples = np.array([0, 1, -1, 32767, -32768] + [0] * (MIN_SAMPLES - 5), dtype="<i2")
    r = post(client, samples.tobytes())
    assert r.status_code == 200, r.text
    seen = embedder.seen[0]
    assert seen.dtype == np.float32
    assert seen[:5].tolist() == [0.0, 1 / 32768, -1 / 32768, 32767 / 32768, -1.0]


# A WAV header embedded as audio would not fail; it would just be wrong.
def test_anything_but_raw_pcm_is_refused(client: TestClient, embedder: FakeEmbedder) -> None:
    r = post(client, pcm(MIN_SAMPLES), content_type="audio/wav")
    assert r.status_code == 415
    assert "audio/pcm" in r.json()["detail"]
    assert embedder.seen == []


def test_a_parameterised_content_type_still_counts(client: TestClient) -> None:
    r = post(client, pcm(MIN_SAMPLES), content_type="audio/pcm; rate=16000")
    assert r.status_code == 200, r.text


@pytest.mark.parametrize(
    ("body", "word"),
    [
        (b"", "no audio"),
        (pcm(MIN_SAMPLES)[:-1], "16-bit"),
        (pcm(MIN_SAMPLES - 1), "minimum"),
    ],
)
def test_unembeddable_audio_is_a_400_that_says_why(
    client: TestClient, embedder: FakeEmbedder, body: bytes, word: str
) -> None:
    r = post(client, body)
    assert r.status_code == 400
    assert word in r.json()["detail"]
    assert embedder.seen == []


# The Go client quotes the body of a failure, so a model error has to be a
# response and not a dropped connection.
def test_a_failing_model_is_a_500_not_a_hang() -> None:
    client = TestClient(create_app(BrokenEmbedder()), raise_server_exceptions=False)
    r = post(client, pcm(MIN_SAMPLES))
    assert r.status_code == 500


# What compose waits on, and what an operator reads to know which checkpoint
# a container is serving.
def test_health_declares_the_model_and_width(client: TestClient) -> None:
    r = client.get("/health")
    assert r.status_code == 200
    assert r.json() == {"status": "ok", "model": "fake", "dim": 4}
