"""The trainer's entry point: what it exports, what it refuses, and what it runs."""

import json
import re
from pathlib import Path

import pytest
import trainconfig
import wake
from layout import Layout

ESPHOME = "2026.7.2"
HERE = Path(__file__).resolve().parent
TFLITE = b"\x1c\x00\x00\x00TFL3" + b"\x00" * 64


@pytest.fixture
def spec() -> dict:
    return trainconfig.load(trainconfig.CHECKED_IN, ESPHOME)


def corpus(lay: Layout) -> None:
    lay.corpus.mkdir(parents=True)
    (lay.corpus / "manifest.json").write_text(json.dumps({"clips": [{"id": "device:kitchen/2"}]}))


def test_a_trained_model_is_exported_as_the_satellites_load_it(tmp_path, spec, capsys):
    lay = Layout(tmp_path)
    corpus(lay)
    trained = tmp_path / "stream_state_internal_quant.tflite"
    trained.write_bytes(TFLITE)
    out = wake.export(lay, spec, trained, ESPHOME, {"batch_size": 128}, tensors=lambda _: [])
    assert out == lay.model / "hey_eddie.json"
    m = json.loads(out.read_text())
    assert m["model"] == "hey_eddie.tflite" and m["wake_word"] == "Hey Eddie"
    assert (lay.model / "hey_eddie.tflite").read_bytes() == TFLITE
    wake.main(["--root", str(tmp_path), "--esphome", ESPHOME, "validate", str(out)])
    assert "the satellites take it" in capsys.readouterr().out
    p = json.loads((lay.model / "provenance.json").read_text())
    assert p["microwakeword_commit"] == wake.MICROWAKEWORD_COMMIT
    assert p["corpus"]["clips"] == 1 and len(p["corpus"]["sha256"]) == 64
    assert p["positives"] is None, "no positives manifest in this layout"
    assert "provenance" not in m, "ESPHome's schema refuses keys it does not name"


def test_a_model_the_device_would_refuse_is_not_exported(tmp_path, spec):
    lay = Layout(tmp_path)
    trained = tmp_path / "model.tflite"
    trained.write_bytes(TFLITE)
    with pytest.raises(SystemExit, match="float32"):
        wake.export(
            lay, spec, trained, ESPHOME, {}, tensors=lambda _: ["input is float32, not int8"]
        )
    trained.write_bytes(b"<html>not found</html>")
    with pytest.raises(SystemExit, match="TFLite"):
        wake.export(lay, spec, trained, ESPHOME, {})
    assert not lay.model.exists(), "nothing is written for a refused model"


def test_validate_refuses_a_manifest_the_firmware_would(tmp_path, capsys):
    m = {
        "type": "micro", "wake_word": "Hey Eddie", "author": "a", "model": "hey_eddie.tflite",
        "trained_languages": ["en"], "version": 2,
        "micro": {"probability_cutoff": 0.97, "feature_step_size": 10, "sliding_window_size": 5,
                  "tensor_arena_size": 30000, "minimum_esphome_version": "2026.9.0"},
    }  # fmt: skip
    path = tmp_path / "hey_eddie.json"
    path.write_text(json.dumps(m))
    with pytest.raises(SystemExit) as refused:
        wake.main(["--esphome", ESPHOME, "validate", str(path)])
    assert refused.value.code == 1
    err = capsys.readouterr().err
    assert "newer than ESPHome" in err and "not beside the manifest" in err


def test_training_runs_the_notebooks_model_and_keeps_only_the_streaming_quantised_one(
    tmp_path, spec
):
    cmd = wake.train_command(Layout(tmp_path), spec, tmp_path / "p.yaml", resume=False)
    assert cmd[1:3] == ["-m", "microwakeword.model_train_eval"]
    assert f"--training_config={tmp_path / 'p.yaml'}" in cmd
    assert "--restore_checkpoint=0" in cmd and "--test_tflite_streaming_quantized=1" in cmd
    assert cmd[cmd.index("mixednet") :] == spec["model"]
    assert "--restore_checkpoint=1" in wake.train_command(
        Layout(tmp_path), spec, tmp_path / "p", resume=True
    )


def test_the_pinned_microwakeword_is_the_one_the_image_checks_out():
    dockerfile = (HERE / "Dockerfile").read_text()
    m = re.search(r"^ARG MICROWAKEWORD_COMMIT=([0-9a-f]{40})$", dockerfile, re.M)
    assert m and m[1] == wake.MICROWAKEWORD_COMMIT
    pins = (HERE / "requirements.txt").read_text()
    assert "microwakeword" not in pins.lower(), "installed, it would lack its audio package"


def test_the_image_copies_every_module_the_trainer_imports():
    dockerfile = (HERE / "Dockerfile").read_text()
    copied = set(re.search(r"^COPY (wake\.py .*) \./$", dockerfile, re.M)[1].split())
    modules = {p.name for p in HERE.glob("*.py") if not p.name.startswith(("test_", "conftest"))}
    assert modules - {"positives.py"} <= copied, "positives.py runs on the host, with Kokoro"
    assert "training.yaml" in copied
