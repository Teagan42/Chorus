# speakerid

Speaker embeddings over HTTP: TitaNet-L (`nemo_en_titanet_large`, 192 dims),
one of the two models SPEC §10 names, as the ONNX export sherpa-onnx
publishes, behind one endpoint. It is the model half of SPEC §5 and the
Speaker ID row of SPEC §10; the Go client is `internal/provider/speakerid`
and the matching lives in `internal/identity`. Why the contract looks like
this is ADR-0025; why the backend is ONNX and how the model is pinned is
ADR-0029.

## Contract

There is no standard speaker-embedding HTTP API, so this is the repo's own.
The Go client and this README are its two copies; change both.

`POST /v1/embed`

- Request body: raw PCM, 16 kHz, signed 16-bit, mono, little-endian, no
  header -- the satellite's own format (`internal/bridge`).
  `Content-Type: audio/pcm` is required; anything else is a `415`.
- Response `200`, `application/json`:

  ```json norun
  {"embedding": [0.0123, ...], "dim": 192, "model": "nemo_en_titanet_large"}
  ```

  `embedding` is L2-normalised, so a cosine against it is a dot product.
  `dim` and `model` are declared on every reply so a client built for one
  model can refuse another.
- `400` with `{"detail": "..."}` for an empty body, an odd byte count, or
  fewer than 1600 samples (100 ms).
- `500` if the model fails on the audio.

`GET /health` answers `{"status": "ok", "model": ..., "dim": ...}` once the
model is loaded. The port does not open before that, so a healthcheck on it
is a readiness check.

## The model

One file, `nemo_en_titanet_large.onnx` (97 MiB), from the sherpa-onnx
`speaker-recongition-models` release (upstream's spelling), pinned in
`embedder.py` by URL and sha256. `speakerid --download-only` fetches it into
the model directory and verifies the digest; a file already there is kept
only if it matches, and the server re-checks it at every boot. Nothing else
is downloaded at any point: the runtime is the `sherpa-onnx` wheel, CPU only.

## Running

From the repo root, which is the uv workspace root:

```sh norun
task speakerid:up                 # builds the image, bakes the model, serves on 127.0.0.1:8890
task test:models -- ./internal/provider/speakerid -speakerid-url http://127.0.0.1:8890
```

Without Docker:

```sh norun
uv sync --all-packages
uv run speakerid --download-only --model-dir .models/speakerid
uv run speakerid --model-dir .models/speakerid --port 8890
```

Configuration is environment or flags: `SPEAKERID_HOST`, `SPEAKERID_PORT`,
`SPEAKERID_MODEL_DIR`, `SPEAKERID_THREADS` (runtime threads; the CPU count
capped at four by default). On a dev box an utterance of a few seconds
embeds in about 100 ms.

## Tests

```sh norun
uv sync --all-packages
uv run pytest sidecars/speakerid
```

These exercise the contract against a fake embedder and the asset pin
against `file://` sources; they need neither the model nor the network. The
real model runs under `tests/test_model.py` when `SPEAKERID_MODEL_DIR` holds
the asset, and skips otherwise. Its voices are the household fixture's
(`internal/reviewui/household`): Teagan, Alice and Alan, in the Kokoro voices
the review UI demo plays. `SPEAKERID_CORPUS` swaps in `<dir>/<speaker>/*.wav`
(16 kHz s16le mono) of your own, which is never committed (CONTRIBUTING §7).
`task test:sidecars` fetches the pinned model and runs all of it, with
ruff; CI runs that task on every push. The models tier of the Go tests covers the same ground
over HTTP, and with `-speakerid-wavs` runs a real enrollment and
identification pass, which is where the thresholds in `internal/identity`
come from. That pass measures; `cmd/enroll` (`task enroll -- add ...`) is
how a household actually enrolls, writing the `identities.yaml` chorusd
reads. `task test` runs nothing in Python.
