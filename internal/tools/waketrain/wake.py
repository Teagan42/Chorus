"""The wake-word trainer's entry point: fetch, features, train, evaluate, validate.

Run by the Taskfile in the container the Dockerfile beside this builds, with
the household's .wake directory mounted at /wake (layout.py):

    task wake:train       # fetch, features, train, then validate the model
    task wake:evaluate    # score it against the deployed model

validate needs nothing but the manifest and runs on the host too.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import shutil
import subprocess
import sys
import tempfile
import urllib.request
import zipfile
from collections.abc import Callable
from pathlib import Path

import manifest
import trainconfig
from layout import NEGATIVE_DATASETS, NEGATIVE_DATASETS_URL, Layout

# The Dockerfile checks microWakeWord out at this commit; a test keeps them in step.
MICROWAKEWORD_COMMIT = "4665173cd35f1cff9a61e06fc427f124766c488e"

# Where model_train_eval leaves the quantised streaming model, under train_dir.
STREAMING_MODEL = Path("tflite_stream_state_internal_quant") / "stream_state_internal_quant.tflite"


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def train_command(lay: Layout, spec: dict, params_path: Path, resume: bool) -> list[str]:
    """model_train_eval, training then converting and testing the quantised streaming model."""
    return [
        sys.executable,
        "-m",
        "microwakeword.model_train_eval",
        f"--training_config={params_path}",
        "--train=1",
        f"--restore_checkpoint={int(resume)}",
        "--test_tf_nonstreaming=0",
        "--test_tflite_nonstreaming=0",
        "--test_tflite_nonstreaming_quantized=0",
        "--test_tflite_streaming=0",
        "--test_tflite_streaming_quantized=1",
        "--use_weights=best_weights",
        *spec["model"],
    ]


def provenance(lay: Layout, spec: dict, params: dict) -> dict:
    """What a model was trained from, kept beside it: the manifest's schema has no room."""

    def described(p: Path) -> dict | None:
        if not p.exists():
            return None
        m = json.loads(p.read_text())
        return {"sha256": sha256(p), "clips": len(m.get("clips", []))}

    fetched = lay.negative_datasets / "fetched.json"
    return {
        "microwakeword_commit": MICROWAKEWORD_COMMIT,
        "corpus": described(lay.corpus / "manifest.json"),
        "positives": described(lay.positives / "manifest.json"),
        "negative_datasets": json.loads(fetched.read_text()) if fetched.exists() else None,
        "training_parameters": params,
        "model_flags": spec["model"],
    }


def export(
    lay: Layout,
    spec: dict,
    trained: Path,
    esphome_version: str,
    params: dict,
    tensors: Callable[[bytes], list[str]] | None = None,
) -> Path:
    """The trained model, as the satellites load it, in lay.model.

    Refused, and nothing written, if ESPHome or the device would reject it.
    """
    data = trained.read_bytes()
    m = manifest.build(spec["manifest"])
    problems = manifest.problems(m, esphome_version) + manifest.model_problems(data)
    if tensors is not None and not problems:
        problems += tensors(data)
    if problems:
        raise SystemExit(
            f"{trained}: the satellites would refuse this model: " + "; ".join(problems)
        )
    lay.model.mkdir(parents=True, exist_ok=True)
    (lay.model / m["model"]).write_bytes(data)
    name = Path(m["model"]).with_suffix(".json").name
    (lay.model / name).write_text(json.dumps(m, indent=2) + "\n")
    p = provenance(lay, spec, params)
    p["model_sha256"] = hashlib.sha256(data).hexdigest()
    (lay.model / "provenance.json").write_text(json.dumps(p, indent=2) + "\n")
    return lay.model / name


def interpreter_tensors(data: bytes) -> list[str]:  # pragma: no cover - the container's
    from ai_edge_litert.interpreter import Interpreter

    it = Interpreter(model_content=data)
    it.allocate_tensors()
    return manifest.tensor_problems(it.get_input_details(), it.get_output_details())


def cmd_fetch(lay: Layout, _: argparse.Namespace) -> None:  # pragma: no cover - network
    """microWakeWord's pre-generated negatives, recorded by digest: the source is unpinned."""
    lay.negative_datasets.mkdir(parents=True, exist_ok=True)
    record = lay.negative_datasets / "fetched.json"
    fetched = json.loads(record.read_text()) if record.exists() else {}
    for name in NEGATIVE_DATASETS:
        if (lay.negative_datasets / name).is_dir():
            continue
        url = f"{NEGATIVE_DATASETS_URL}{name}.zip"
        print(f"fetching {url}")
        with tempfile.TemporaryDirectory() as tmp:
            z = Path(tmp) / f"{name}.zip"
            urllib.request.urlretrieve(url, z)
            fetched[name] = {"url": url, "sha256": sha256(z), "bytes": z.stat().st_size}
            with zipfile.ZipFile(z) as zf:
                zf.extractall(lay.negative_datasets)
        record.write_text(json.dumps(fetched, indent=2) + "\n")


def cmd_features(lay: Layout, args: argparse.Namespace) -> None:  # pragma: no cover
    import features

    spec = trainconfig.load(args.config, args.esphome)
    features.run(features.plan(lay), spec, lay)


def cmd_train(lay: Layout, args: argparse.Namespace) -> None:  # pragma: no cover
    spec = trainconfig.load(args.config, args.esphome)
    if not args.skip_features:
        cmd_features(lay, args)
    params = trainconfig.render(spec, lay, trainconfig.present(lay, spec))
    params_path = lay.root / "training_parameters.yaml"
    params_path.write_text(trainconfig.dump(params))
    if lay.train.exists() and not args.resume:
        shutil.rmtree(lay.train)
    subprocess.run(train_command(lay, spec, params_path, args.resume), check=True)
    out = export(lay, spec, lay.train / STREAMING_MODEL, args.esphome, params, interpreter_tensors)
    print(f"wrote {out}; score it with task wake:evaluate")


def cmd_evaluate(lay: Layout, args: argparse.Namespace) -> None:  # pragma: no cover
    import evaluate

    negatives, positives = evaluate.held_out(lay, args.split)
    candidate = args.candidate or str(lay.model / "hey_eddie.json")
    runs = []
    for source in [candidate] + ([] if args.deployed == "none" else [args.deployed]):
        path = evaluate.fetch(source, lay.root / "deployed")
        m, data, problems = evaluate.load(path, args.esphome)
        if problems and source == candidate:
            raise SystemExit(f"{source}: the satellites would refuse it: " + "; ".join(problems))
        runner = evaluate.Runner(data, m["micro"]["feature_step_size"])
        runs.append((source, m, evaluate.run_model(runner, negatives, positives)))
    r = evaluate.report(runs[0], runs[1] if len(runs) > 1 else None, args.tolerance, args.split)
    lay.evaluation.write_text(json.dumps(r, indent=2) + "\n")
    print(json.dumps({k: r[k] for k in ("ships", "why")}, indent=2))
    if not r["ships"]:
        sys.exit(1)


def cmd_validate(_: Layout, args: argparse.Namespace) -> None:
    m = json.loads(Path(args.manifest).read_text())
    problems = manifest.problems(m, args.esphome)
    model = Path(args.manifest).parent / str(m.get("model", ""))
    if model.is_file():
        problems += manifest.model_problems(model.read_bytes())
    else:
        problems.append(f"{model} is not beside the manifest")
    for p in problems:
        print(p, file=sys.stderr)
    if problems:
        sys.exit(1)
    print(f"{args.manifest}: the satellites take it")


def main(argv: list[str] | None = None) -> None:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--root", type=Path, default=Path("/wake"), help="the .wake directory")
    ap.add_argument("--esphome", required=True, help="the ESPHome the firmware is compiled with")
    ap.add_argument("--config", type=Path, default=trainconfig.CHECKED_IN)
    sub = ap.add_subparsers(dest="command", required=True)
    sub.add_parser("fetch", help="microWakeWord's negative spectrograms")
    sub.add_parser("features", help="spectrograms of the positives and the corpus")
    t = sub.add_parser("train", help="features, training, and the model as the satellites load it")
    t.add_argument("--resume", action="store_true", help="continue the last run's checkpoint")
    t.add_argument("--skip-features", action="store_true")
    e = sub.add_parser("evaluate", help="score the model against the deployed one")
    e.add_argument("--candidate", help="a manifest; default the trained model's")
    e.add_argument("--deployed", required=True, help="the deployed manifest's URL or path, or none")
    e.add_argument(
        "--tolerance", type=float, default=0.0, help="false rejects allowed above deployed"
    )
    e.add_argument("--split", default="testing")
    v = sub.add_parser("validate", help="would the satellites take this manifest and model")
    v.add_argument("manifest", type=Path)
    args = ap.parse_args(argv)
    commands = {
        "fetch": cmd_fetch,
        "features": cmd_features,
        "train": cmd_train,
        "evaluate": cmd_evaluate,
        "validate": cmd_validate,
    }
    commands[args.command](Layout(args.root), args)


if __name__ == "__main__":
    main()
