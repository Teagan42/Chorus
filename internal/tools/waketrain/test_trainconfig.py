"""The checked-in training config, and what it renders into for microWakeWord."""

import os
from pathlib import Path

import pytest
import trainconfig
import yaml
from layout import Layout

ESPHOME = "2026.7.2"
GOLDEN = Path(__file__).resolve().parent / "testdata" / "training_parameters.golden.yaml"
ALL = {
    "positives",
    "household_confirmed",
    "household_auto",
    "speech",
    "dinner_party",
    "no_speech",
    "dinner_party_eval",
}


@pytest.fixture
def spec() -> dict:
    return trainconfig.load(trainconfig.CHECKED_IN, ESPHOME)


def test_the_checked_in_config_renders_as_its_golden_parameters(spec):
    got = trainconfig.dump(trainconfig.render(spec, Layout(Path("/wake")), ALL))
    if os.environ.get("WAKETRAIN_UPDATE"):
        GOLDEN.write_text(got)
    assert got == GOLDEN.read_text(), "run with WAKETRAIN_UPDATE=1 to accept the change"


def test_a_feature_set_with_no_spectrograms_is_left_out(spec):
    params = trainconfig.render(spec, Layout(Path("/wake")), {"positives", "speech"})
    dirs = [f["features_dir"] for f in params["features"]]
    assert dirs == ["/wake/features/positives", "/wake/negative_datasets/speech"]
    assert [f["truth"] for f in params["features"]] == [True, False]


def test_training_on_nothing_to_learn_from_is_refused(spec):
    lay = Layout(Path("/wake"))
    with pytest.raises(trainconfig.ConfigError, match="positive"):
        trainconfig.render(spec, lay, {"household_auto", "speech"})
    with pytest.raises(trainconfig.ConfigError, match="negative"):
        trainconfig.render(spec, lay, {"positives"})


def test_present_finds_the_sets_whose_spectrograms_exist(tmp_path, spec):
    lay = Layout(tmp_path)
    (lay.features / "positives" / "training" / "wakeword_mmap").mkdir(parents=True)
    (lay.household_features("auto") / "testing_ambient" / "household_mmap").mkdir(parents=True)
    (lay.negative_datasets / "speech" / "training" / "speech_mmap").mkdir(parents=True)
    (lay.negative_datasets / "no_speech").mkdir(parents=True)
    assert trainconfig.present(lay, spec) == {"positives", "household_auto", "speech"}


def write(tmp_path: Path, spec: dict) -> Path:
    p = tmp_path / "training.yaml"
    p.write_text(yaml.safe_dump(spec))
    return p


def test_a_config_whose_model_the_firmware_would_refuse_is_refused(tmp_path, spec):
    spec["manifest"]["probability_cutoff"] = 1.0
    with pytest.raises(trainconfig.ConfigError, match="never wakes"):
        trainconfig.load(write(tmp_path, spec), ESPHOME)


def test_a_config_asking_for_a_newer_esphome_than_the_firmware_is_refused(tmp_path, spec):
    spec["manifest"]["minimum_esphome_version"] = "2026.9.0"
    with pytest.raises(trainconfig.ConfigError, match="newer than ESPHome"):
        trainconfig.load(write(tmp_path, spec), ESPHOME)


def test_a_manifest_step_other_than_the_features_is_refused(tmp_path, spec):
    spec["manifest"]["feature_step_size"] = 20
    with pytest.raises(trainconfig.ConfigError, match="window_step_ms"):
        trainconfig.load(write(tmp_path, spec), ESPHOME)


def test_an_unknown_feature_set_is_refused(tmp_path, spec):
    spec["feature_sets"]["podcasts"] = {
        "sampling_weight": 1.0,
        "penalty_weight": 1.0,
        "truncation_strategy": "random",
    }
    with pytest.raises(trainconfig.ConfigError, match="podcasts"):
        trainconfig.load(write(tmp_path, spec), ESPHOME)
