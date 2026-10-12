"""The manifest checks: what the satellites take, and every way they refuse one."""

import copy

import manifest
import numpy as np
import pytest

ESPHOME = "2026.7.2"

# esphome/micro-wake-word-models' models/v2/okay_nabu.json, which ESPHome
# ships as a default: whatever the checks say, they must take this.
OKAY_NABU = {
    "type": "micro",
    "wake_word": "Okay Nabu",
    "author": "Kevin Ahrendt",
    "website": "https://www.kevinahrendt.com/",
    "model": "okay_nabu.tflite",
    "trained_languages": ["en"],
    "version": 2,
    "micro": {
        "probability_cutoff": 0.97,
        "feature_step_size": 10,
        "sliding_window_size": 5,
        "tensor_arena_size": 26080,
        "minimum_esphome_version": "2024.7.0",
    },
}

SPEC = {
    "wake_word": "Hey Eddie",
    "author": "Chorus wake-word retraining",
    "website": "https://teagan42.github.io/Chorus/",
    "model": "hey_eddie.tflite",
    "trained_languages": ["en"],
    "probability_cutoff": 0.97,
    "feature_step_size": 10,
    "sliding_window_size": 5,
    "tensor_arena_size": 30000,
    "minimum_esphome_version": "2024.7.0",
}


def edited(path: str, value: object) -> dict:
    m = copy.deepcopy(OKAY_NABU)
    *parents, key = path.split(".")
    node = m
    for p in parents:
        node = node[p]
    if value is DELETE:
        del node[key]
    else:
        node[key] = value
    return m


DELETE = object()


def test_a_stock_model_the_firmware_ships_passes():
    assert manifest.problems(OKAY_NABU, ESPHOME) == []


def test_the_manifest_built_from_the_training_config_passes():
    built = manifest.build(SPEC)
    assert manifest.problems(built, ESPHOME) == []
    assert built["micro"]["probability_cutoff"] == 0.97
    assert built["model"] == "hey_eddie.tflite"


@pytest.mark.parametrize(
    ("path", "value", "says"),
    [
        ("version", 1, "version"),
        ("type", "openwakeword", "type"),
        ("provenance", {"corpus": "x"}, "unknown key 'provenance'"),
        ("micro.notes", "trained on Thursday", "unknown key micro.'notes'"),
        ("model", "../hey_eddie.tflite", "beside the manifest"),
        ("model", "", "model"),
        ("author", DELETE, "author"),
        ("wake_word", 7, "wake_word"),
        ("trained_languages", [], "trained_languages"),
        ("website", "ftp://example.org", "website"),
        ("micro.feature_step_size", 40, "feature_step_size"),
        ("micro.feature_step_size", 10.5, "feature_step_size"),
        ("micro.tensor_arena_size", 0, "tensor_arena_size"),
        ("micro.sliding_window_size", 0, "sliding_window_size"),
        ("micro.sliding_window_size", True, "sliding_window_size"),
        ("micro.probability_cutoff", 1.0, "never wakes"),
        ("micro.probability_cutoff", "0.97", "probability_cutoff"),
        ("micro.probability_cutoff", 0.02, "sensitivity select"),
        ("micro.minimum_esphome_version", "2027.1.0", "newer than ESPHome"),
        ("micro.minimum_esphome_version", "2024.7", "MAJOR.MINOR.PATCH"),
        ("micro.tensor_arena_size", DELETE, "micro.tensor_arena_size is required"),
    ],
)
def test_a_manifest_the_satellites_would_refuse_is_refused(path, value, says):
    got = manifest.problems(edited(path, value), ESPHOME)
    assert got, f"{path}={value!r} passed"
    assert any(says in p for p in got), got


def test_one_language_given_as_a_string_is_taken_as_esphome_takes_it():
    assert manifest.problems(edited("trained_languages", "en"), ESPHOME) == []


def test_a_cutoff_is_quantised_as_the_firmware_holds_it():
    assert manifest.quantised(0.97) == 247
    assert manifest.quantised(8 / 255) == 8
    assert manifest.problems(edited("micro.probability_cutoff", 8 / 255), ESPHOME) == []


def test_a_file_that_is_not_a_tflite_model_is_refused():
    flatbuffer = b"\x1c\x00\x00\x00TFL3" + b"\x00" * 64
    assert manifest.model_problems(flatbuffer) == []
    assert manifest.model_problems(b"") != []
    assert manifest.model_problems(b"<html>not found</html>") != []


def test_tensors_the_device_would_refuse_are_refused():
    good_in = [{"shape": np.array([1, 3, 40]), "dtype": np.int8}]
    good_out = [{"shape": np.array([1, 1]), "dtype": np.uint8}]
    assert manifest.tensor_problems(good_in, good_out) == []
    assert manifest.tensor_problems([{"shape": [1, 3, 40], "dtype": np.float32}], good_out)
    assert manifest.tensor_problems([{"shape": [1, 3, 49], "dtype": np.int8}], good_out)
    assert manifest.tensor_problems(good_in, [{"shape": [1, 2], "dtype": np.uint8}])
    assert manifest.tensor_problems(good_in, [{"shape": [1, 1], "dtype": np.int8}])


def test_a_version_1_manifest_is_read_as_esphome_converts_it():
    v1 = {
        "type": "micro",
        "wake_word": "Hey Eddie",
        "author": "someone",
        "website": "https://example.org/",
        "model": "hey_eddie.tflite",
        "version": 1,
        "micro": {"probability_cutoff": 0.9, "sliding_window_average_size": 10},
    }
    v2 = manifest.as_v2(v1)
    assert v2["version"] == 2
    assert v2["micro"]["sliding_window_size"] == 10
    assert v2["micro"]["feature_step_size"] == 20
    assert v2["micro"]["tensor_arena_size"] == 45672
    assert "sliding_window_average_size" not in v2["micro"]
    assert manifest.as_v2(OKAY_NABU) is OKAY_NABU
