"""Scoring a model against the one deployed, from recorded outputs: no model runs."""

import json
import wave

import evaluate
import numpy as np
import pytest
from layout import Layout

HIGH = [255] * 8  # a confident wake: five 255s fill the ring
LOW = [10] * 40  # an hour's worth of nothing, in miniature


def outputs(
    negatives: list[list[int]], positives: list[list[int]], seconds: float = 3600
) -> evaluate.Outputs:
    return evaluate.Outputs(
        stride=3,
        negatives={f"n{i}.wav": p for i, p in enumerate(negatives)},
        positives={f"p{i}.wav": p for i, p in enumerate(positives)},
        negative_seconds=seconds,
    )


def test_false_accepts_an_hour_and_false_rejects_are_counted_by_the_device_rule():
    out = outputs(
        [LOW + HIGH, LOW, HIGH + LOW], [HIGH, HIGH, LOW, [255, 0, 255, 0, 255, 0]], seconds=1800
    )
    s = evaluate.score(out, 0.97, 5)
    assert (s.false_accepts, s.hours, s.false_accepts_per_hour) == (2, 0.5, 4.0)
    assert (s.missed, s.positives, s.false_reject_rate) == (2, 4, 0.5)


def test_a_lower_cutoff_trades_misses_for_false_accepts():
    out = outputs([[240] * 20], [[240] * 20, [200] * 20])
    strict, loose = evaluate.score(out, 0.97, 5), evaluate.score(out, 0.75, 5)
    assert strict.false_accepts == 0 and strict.missed == 2
    assert loose.false_accepts == 1 and loose.missed == 0
    cutoffs = [s.cutoff for s in evaluate.sweep(out, 5)]
    assert cutoffs == sorted(cutoffs) and 0.97 in cutoffs


def score(fa: int, frr: float) -> evaluate.Score:
    return evaluate.Score(0.97, 5, fa, 1.0, float(fa), round(frr * 100), 100, frr)


@pytest.mark.parametrize(
    ("candidate", "deployed", "ships", "says"),
    [
        (score(3, 0.05), score(9, 0.05), True, "9 -> 3"),
        (score(9, 0.02), score(9, 0.05), True, "5.0% -> 2.0%"),
        (score(10, 0.01), score(9, 0.05), False, "more false accepts"),
        (score(3, 0.09), score(9, 0.05), False, "misses more"),
        (score(9, 0.05), score(9, 0.05), False, "no better"),
    ],
)
def test_a_model_ships_only_if_it_is_better_than_the_deployed_one(candidate, deployed, ships, says):
    got, why = evaluate.verdict(candidate, deployed, tolerance=0.0)
    assert got is ships
    assert says in why


def test_a_tolerance_lets_fewer_false_accepts_buy_a_few_misses():
    assert evaluate.verdict(score(3, 0.07), score(9, 0.05), tolerance=0.02)[0]
    assert not evaluate.verdict(score(3, 0.08), score(9, 0.05), tolerance=0.02)[0]


def test_the_first_model_has_nothing_to_beat():
    assert evaluate.verdict(score(9, 0.5), None, tolerance=0.0) == (
        True,
        "nothing deployed to compare with",
    )


def wav(path, rate=16_000, channels=1, seconds=0.5):
    path.parent.mkdir(parents=True, exist_ok=True)
    with wave.open(str(path), "wb") as w:
        w.setnchannels(channels)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(np.zeros(round(rate * seconds) * channels, "<i2").tobytes())


def test_only_the_testing_split_is_held_out(tmp_path):
    lay = Layout(tmp_path)
    wav(lay.household("auto", "testing") / "kitchen-2.wav")
    wav(lay.household("confirmed", "testing") / "kitchen-3.wav")
    wav(lay.household("auto", "training") / "office-2.wav")
    wav(lay.positives / "testing" / "bm_lewis_0_1.00.wav")
    wav(lay.positives / "training" / "af_heart_0_1.00.wav")
    negatives, positives = evaluate.held_out(lay)
    assert [p.name for p in negatives] == ["kitchen-3.wav", "kitchen-2.wav"]
    assert [p.name for p in positives] == ["bm_lewis_0_1.00.wav"]
    with pytest.raises(SystemExit, match="wake:corpus"):
        evaluate.held_out(lay, split="validation")


def test_a_clip_not_in_the_devices_format_is_refused(tmp_path):
    wav(tmp_path / "ok.wav")
    wav(tmp_path / "kokoro.wav", rate=24_000)
    wav(tmp_path / "stereo.wav", channels=2)
    assert len(evaluate.read_pcm(tmp_path / "ok.wav")) == 8000
    for bad in ("kokoro.wav", "stereo.wav"):
        with pytest.raises(ValueError, match="16 kHz mono"):
            evaluate.read_pcm(tmp_path / bad)
    assert len(evaluate.padded(np.ones(100, "<i2"))) == 100 + 16_000 + 8000


def test_a_deployed_models_file_is_fetched_from_beside_its_manifest():
    url = "https://ha.example/local/hey_eddie/hey_eddie.json"
    assert (
        evaluate.model_url(url, "hey_eddie.tflite")
        == "https://ha.example/local/hey_eddie/hey_eddie.tflite"
    )


def test_a_deployed_model_is_loaded_and_checked(tmp_path):
    m = {
        "type": "micro", "wake_word": "Hey Eddie", "author": "a", "website": "https://example.org/",
        "model": "hey_eddie.tflite", "version": 1,
        "micro": {"probability_cutoff": 0.9, "sliding_window_average_size": 10},
    }  # fmt: skip
    (tmp_path / "hey_eddie.json").write_text(json.dumps(m))
    (tmp_path / "hey_eddie.tflite").write_bytes(b"\x1c\x00\x00\x00TFL3" + b"\x00" * 32)
    got, data, problems = evaluate.load(tmp_path / "hey_eddie.json", "2026.7.2")
    assert got["micro"]["sliding_window_size"] == 10 and data[4:8] == b"TFL3"
    # A version 1 model was converted on read; only a missing minimum version is left.
    assert problems == ["micro.minimum_esphome_version is required"]


def test_the_report_says_whether_the_candidate_ships_and_why():
    m = {"wake_word": "Hey Eddie", "micro": {"probability_cutoff": 0.97, "sliding_window_size": 5}}
    better = outputs([LOW, LOW], [HIGH, HIGH])
    worse = outputs([LOW + HIGH, LOW], [HIGH, HIGH])
    r = evaluate.report(
        ("cand.json", m, better), ("https://ha/hey_eddie.json", m, worse), 0.0, "testing"
    )
    assert r["ships"] is True
    assert r["candidate"]["score"]["false_accepts"] == 0
    assert r["deployed"]["score"]["false_accepts"] == 1
    assert len(r["candidate"]["sweep"]) == len(evaluate.SWEEP)
    assert r["negatives"] == {"clips": 2, "hours": 1.0}
    assert json.loads(json.dumps(r)) == r
