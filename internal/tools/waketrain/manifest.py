"""The model manifest micro_wake_word loads, and every check that refuses one.

ESPHome reads a wake model as a JSON manifest naming a .tflite beside it. It
refuses at config time a manifest its MANIFEST_SCHEMA_V2 rejects, and the
device refuses at boot a model whose tensors are the wrong shape. Both sets
of checks are mirrored here from ESPHome 2026.7.2 (Taskfile.yml's
ESPHOME_VERSION), esphome/components/micro_wake_word/__init__.py and
streaming_model.cpp, so a model the satellites would reject never reaches
one (ADR-0078). Two more are Chorus's own: the cutoff must leave room for
the sensitivity select in esphome/packages/chorus-bridge.yaml, and the model
file must sit beside its manifest.
"""

from __future__ import annotations

import re
from urllib.parse import urlparse

# The schema refuses keys it does not name, at the top and under "micro".
TOP_KEYS = {
    "type",
    "model",
    "author",
    "version",
    "wake_word",
    "trained_languages",
    "website",
    "micro",
}
MICRO_KEYS = {
    "feature_step_size",
    "tensor_arena_size",
    "probability_cutoff",
    "sliding_window_size",
    "minimum_esphome_version",
}

# The preprocessor's features per slice (preprocessor_settings.h).
FEATURE_SIZE = 40

# chorus-bridge.yaml's "More sensitive" lowers the quantised cutoff by 8 in a
# uint8, so a cutoff below 8/255 would wrap to the least sensitive there is.
MIN_QUANTISED_CUTOFF = 8

TFLITE_IDENTIFIER = b"TFL3"

_VERSION = re.compile(r"^(\d+)\.(\d+)\.(\d+)$")


def quantised(cutoff: float) -> int:
    """The cutoff as the firmware holds it: int(cutoff * 255), as to_code does."""
    return int(cutoff * 255)


def _is_int(v: object) -> bool:
    return isinstance(v, int) and not isinstance(v, bool)


def _is_number(v: object) -> bool:
    return isinstance(v, int | float) and not isinstance(v, bool)


def version(v: str) -> tuple[int, int, int]:
    m = _VERSION.match(v)
    if not m:
        raise ValueError(f"{v!r} is not MAJOR.MINOR.PATCH")
    return int(m[1]), int(m[2]), int(m[3])


def problems(m: object, esphome_version: str) -> list[str]:
    """Every reason ESPHome, the device or Chorus would refuse manifest m.

    Empty means the satellites take it. esphome_version is the ESPHome the
    firmware is compiled with, which refuses a manifest asking for newer.
    """
    if not isinstance(m, dict):
        return ["the manifest is not a JSON object"]
    out = []
    if m.get("version") != 2:
        out.append(f"version is {m.get('version')!r}; a model this pipeline writes is version 2")
    for key in sorted(set(m) - TOP_KEYS):
        out.append(f"unknown key {key!r}: ESPHome's schema refuses it")
    if m.get("type") != "micro":
        out.append(f"type is {m.get('type')!r}, not 'micro'")
    for key in ("model", "author", "wake_word"):
        if not isinstance(m.get(key), str) or not m[key]:
            out.append(f"{key} must be a non-empty string")
    model = m.get("model")
    if isinstance(model, str) and model and ("/" in model or "\\" in model or model in (".", "..")):
        out.append(f"model {model!r} must be a file name beside the manifest")
    langs = m.get("trained_languages")
    if isinstance(langs, str):
        langs = [langs]
    if not isinstance(langs, list) or not langs or not all(isinstance(x, str) for x in langs):
        out.append("trained_languages must be a list of strings")
    if "website" in m:
        site = m["website"]
        u = urlparse(site) if isinstance(site, str) else None
        if u is None or u.scheme not in ("http", "https") or not u.netloc:
            out.append(f"website {site!r} is not an http(s) URL")
    micro = m.get("micro")
    if not isinstance(micro, dict):
        out.append("micro must be an object")
        return out
    for key in sorted(set(micro) - MICRO_KEYS):
        out.append(f"unknown key micro.{key!r}: ESPHome's schema refuses it")
    for key in sorted(MICRO_KEYS - set(micro)):
        out.append(f"micro.{key} is required")

    step = micro.get("feature_step_size")
    if "feature_step_size" in micro and not (_is_int(step) and 0 <= step <= 30):
        out.append(f"micro.feature_step_size {step!r} must be an integer from 0 to 30")
    arena = micro.get("tensor_arena_size")
    if "tensor_arena_size" in micro and not (_is_int(arena) and arena > 0):
        out.append(f"micro.tensor_arena_size {arena!r} must be a positive integer")
    window = micro.get("sliding_window_size")
    if "sliding_window_size" in micro and not (_is_int(window) and window > 0):
        out.append(f"micro.sliding_window_size {window!r} must be a positive integer")
    cutoff = micro.get("probability_cutoff")
    if "probability_cutoff" in micro:
        if not _is_number(cutoff) or not 0 < cutoff < 1:
            out.append(
                f"micro.probability_cutoff {cutoff!r} must be between 0 and 1: at 1 it never wakes"
            )
        elif quantised(cutoff) < MIN_QUANTISED_CUTOFF:
            out.append(
                f"micro.probability_cutoff {cutoff} quantises to {quantised(cutoff)}; the "
                f"sensitivity select lowers it by 8, so it must be at least "
                f"{MIN_QUANTISED_CUTOFF / 255:.3f}"
            )
    minimum = micro.get("minimum_esphome_version")
    if "minimum_esphome_version" in micro:
        try:
            if version(minimum) > version(esphome_version):
                out.append(
                    f"micro.minimum_esphome_version {minimum} is newer than ESPHome "
                    f"{esphome_version}, which the firmware is compiled with"
                )
        except (TypeError, ValueError):
            out.append(f"micro.minimum_esphome_version {minimum!r} is not MAJOR.MINOR.PATCH")
    return out


def model_problems(data: bytes) -> list[str]:
    """What is wrong with a .tflite before an interpreter is asked."""
    if len(data) < 8:
        return ["the model file is empty or truncated"]
    if data[4:8] != TFLITE_IDENTIFIER:
        return [f"the model file is not a TFLite flatbuffer (identifier {data[4:8]!r})"]
    return []


def tensor_problems(inputs: list[dict], outputs: list[dict]) -> list[str]:
    """streaming_model.cpp's checks on a loaded model's tensors.

    inputs and outputs are a TFLite interpreter's get_input_details() and
    get_output_details(): the device wants int8 input of shape
    [1, stride, 40] and one uint8 output of shape [1, 1].
    """
    out = []
    if not inputs:
        return ["the model has no input tensor"]
    shape = [int(x) for x in inputs[0]["shape"]]
    if len(shape) != 3 or shape[0] != 1 or shape[2] != FEATURE_SIZE:
        out.append(f"input shape {shape} is not [1, stride, {FEATURE_SIZE}]")
    if _dtype(inputs[0]) != "int8":
        out.append(f"input is {_dtype(inputs[0])}, not int8: the model is not quantised")
    if not outputs:
        return out + ["the model has no output tensor"]
    oshape = [int(x) for x in outputs[0]["shape"]]
    if oshape != [1, 1]:
        out.append(f"output shape {oshape} is not [1, 1]")
    if _dtype(outputs[0]) != "uint8":
        out.append(f"output is {_dtype(outputs[0])}, not uint8")
    return out


def _dtype(detail: dict) -> str:
    d = detail["dtype"]
    return getattr(d, "__name__", str(d))


def as_v2(m: dict) -> dict:
    """A version 1 manifest as ESPHome reads it, for scoring a deployed model.

    The defaults are ESPHome's _convert_manifest_v1_to_v2: Inception models
    took a 20 ms step, and the arena they need.
    """
    if m.get("version") != 1:
        return m
    micro = dict(m["micro"])
    micro["sliding_window_size"] = micro.pop("sliding_window_average_size")
    micro.setdefault("tensor_arena_size", 45672)
    micro.setdefault("feature_step_size", 20)
    return {**m, "version": 2, "trained_languages": ["en"], "micro": micro}


def build(spec: dict) -> dict:
    """The version 2 manifest for a trained model, from training.yaml's manifest section."""
    return {
        "type": "micro",
        "wake_word": spec["wake_word"],
        "author": spec["author"],
        "website": spec["website"],
        "model": spec["model"],
        "trained_languages": list(spec["trained_languages"]),
        "version": 2,
        "micro": {
            "probability_cutoff": spec["probability_cutoff"],
            "feature_step_size": spec["feature_step_size"],
            "sliding_window_size": spec["sliding_window_size"],
            "tensor_arena_size": spec["tensor_arena_size"],
            "minimum_esphome_version": spec["minimum_esphome_version"],
        },
    }
