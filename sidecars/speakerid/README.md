# speakerid

Speaker embeddings over HTTP: SpeechBrain's ECAPA-TDNN
(`speechbrain/spkrec-ecapa-voxceleb`, 192 dims) behind one endpoint. It is
the model half of SPEC §5 and the Speaker ID row of SPEC §10; the Go client
is `internal/provider/speakerid` and the matching lives in
`internal/identity`. Why the contract looks like this is ADR-0025.

## Contract

There is no standard speaker-embedding HTTP API, so this is the repo's own.
The Go client and this README are its two copies; change both.

`POST /v1/embed`

- Request body: raw PCM, 16 kHz, signed 16-bit, mono, little-endian, no
  header -- the satellite's own format (`internal/bridge`).
  `Content-Type: audio/pcm` is required; anything else is a `415`.
- Response `200`, `application/json`:

  ```json norun
  {"embedding": [0.0123, ...], "dim": 192, "model": "speechbrain/spkrec-ecapa-voxceleb"}
  ```

  `embedding` is L2-normalised, so a cosine against it is a dot product.
  `dim` and `model` are declared on every reply so a client built for one
  checkpoint can refuse another.
- `400` with `{"detail": "..."}` for an empty body, an odd byte count, or
  fewer than 1600 samples (100 ms).
- `500` if the model fails on the audio.

`GET /health` answers `{"status": "ok", "model": ..., "dim": ...}` once the
model is loaded. The port does not open before that, so a healthcheck on it
is a readiness check.

## Running

From the repo root, which is the uv workspace root:

```sh norun
task speakerid:up                 # builds the image, bakes the checkpoint, serves on 127.0.0.1:8890
task test:models -- ./internal/provider/speakerid -speakerid-url http://127.0.0.1:8890
```

Without Docker:

```sh norun
uv sync --all-packages
uv run --package speakerid speakerid --model-dir ./.models/ecapa --port 8890
```

Configuration is environment or flags: `SPEAKERID_HOST`, `SPEAKERID_PORT`,
`SPEAKERID_MODEL_DIR`, `SPEAKERID_MODEL_REVISION` (a Hub commit; `main`
until one is recorded), `SPEAKERID_DEVICE` (`cuda` when available, else
`cpu`).

The `torch` pinned here is the PyPI build, which carries CUDA. It runs on a
CPU-only box too, just large; SPEC §13 puts model sidecars on the GPU host.

## Tests

```sh norun
uv sync --all-packages
uv run pytest sidecars/speakerid
```

These exercise the contract against a fake embedder and need neither the
checkpoint nor a GPU. The real model is only exercised by the models tier
above, which is the test that would catch a checkpoint changing its width.
`task test` runs nothing in Python.
