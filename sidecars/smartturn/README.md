# smartturn

End-of-turn detection over HTTP: Smart Turn v3.2 (`smart-turn-v3.2-cpu`), the
successor to the Smart Turn v2 that SPEC §10's Endpointing row names, on ONNX
Runtime behind one endpoint. It is the model half of SPEC §4.5's semantic
endpointing; the Go client is `internal/provider/smartturn` and the endpointer
that asks it is `listen.Semantic`. Why it is this model, pinned this way, and
asked this way is ADR-0036.

## Contract

There is no standard end-of-turn HTTP API, so this is the repo's own. The Go
client and this README are its two copies; change both.

`POST /v1/turn`

- Request body: the turn so far as raw PCM, 16 kHz, signed 16-bit, mono,
  little-endian, no header -- the satellite's own format (`internal/bridge`).
  `Content-Type: audio/pcm` is required; anything else is a `415`. Any length
  is accepted; the model sees the last 8 s, padded with silence at the front
  when the turn is shorter.
- Response `200`, `application/json`:

  ```json norun
  {"complete": true, "probability": 0.981, "model": "smart-turn-v3.2-cpu"}
  ```

  `complete` is `probability > 0.5`, upstream's cut. `model` is declared on
  every reply so a client built for one checkpoint can refuse another.
- `400` with `{"detail": "..."}` for an empty body, an odd byte count, or
  fewer than 1600 samples (100 ms).
- `500` if the model fails on the audio or answers something that is not a
  probability.

`GET /health` answers `{"status": "ok", "model": ...}` once the model is
loaded. The port does not open before that, so a healthcheck on it is a
readiness check.

## The model

One file, `smart-turn-v3.2-cpu.onnx` (8.3 MiB), read out of pipecat's
`pipecat_ai-1.12.0` wheel on PyPI and pinned in `model.py` by the wheel's URL
and sha256 and the member's own sha256. `smartturn --download-only` fetches
the wheel, checks it, extracts the model, checks that, and keeps only the
model; a file already there is kept only if it matches, and the server
re-checks it at every boot. Nothing else is downloaded at any point: the
runtime is the `onnxruntime` CPU wheel, and the Whisper log-mel front end is
`whisper_features.py`, vendored from the same pipecat release under its
BSD 2-Clause licence.

## Running

From the repo root, which is the uv workspace root:

```sh norun
task smartturn:up                 # builds the image, bakes the model, serves on 127.0.0.1:8891
task test:models -- ./internal/provider/smartturn -smartturn-url http://127.0.0.1:8891
```

Without Docker:

```sh norun
uv sync --all-packages
uv run smartturn --download-only --model-dir .models/smartturn
uv run smartturn --model-dir .models/smartturn --port 8891
```

Configuration is environment or flags: `SMARTTURN_HOST`, `SMARTTURN_PORT`,
`SMARTTURN_MODEL_DIR`, `SMARTTURN_THREADS` (runtime threads; the CPU count
capped at four by default). On a dev box a verdict takes 90–180 ms, most of
it the feature extraction.

## Tests

```sh norun
uv sync --all-packages
uv run pytest sidecars/smartturn
```

These exercise the contract against a fake model and the pin against
`file://` wheels; they need neither the model nor the network. The real model
runs under `tests/test_real_model.py` when `SMARTTURN_MODEL_DIR` holds the asset,
and with `SMARTTURN_CORPUS` pointing at `<dir>/complete/*.wav` and
`<dir>/incomplete/*.wav` (16 kHz s16le mono) it checks finished turns score
above unfinished ones. That corpus is never committed (CONTRIBUTING §7).
`task smartturn:corpus` renders one on your machine: about twenty household
commands, finished and cut off mid-sentence, spoken by Chatterbox (or cloned
from a reference WAV with `-- --voice teagan.wav`) into `.corpus/turns`. It is
a standalone `uv run --script`, so torch and the Chatterbox weights stay out
of the workspace; the weights come from Hugging Face on first run. The
models tier of the Go tests covers the same ground over HTTP and, with
`-smartturn-wavs`, prints every take's verdict and what it cost. `task test`
runs nothing in Python.
