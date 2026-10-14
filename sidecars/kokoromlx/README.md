# kokoromlx

The voice SPEC §10 names -- Kokoro-82M -- rendered on this Mac's GPU through
[kokoro-mlx](https://github.com/gabrimatic/kokoro-mlx) instead of a container's
CPU. It serves the part of Kokoro-FastAPI's speech API that the Go client
`internal/provider/kokoro` speaks, on the same port, so pointing `KOKORO_URL`
at it is the whole change. Why it exists and why it is not a workspace member
is ADR-0092.

Measured here, 2026-10-13: a clause renders in 0.28 s against the CPU image's
0.56-0.92 s, about a tenth of the audio's own length. The audio is identical in
kind -- the same weights, the same 24 kHz -- so nothing downstream changes.

Apple Silicon only. MLX is Metal and Docker Desktop on macOS passes no GPU
through, so this runs on the host and has no image.

## Contract

The subset the daemon uses, and the rest is a 400. This README and the Go
client are its two copies; change both.

`POST /v1/audio/speech`

- Request body, `application/json`:

  ```json norun
  {"model": "kokoro", "input": "Okay.", "voice": "af_heart", "response_format": "pcm", "speed": 1.0, "stream": false}
  ```

  `model` is `kokoro`, the one name Kokoro-FastAPI serves -- which also means
  the journal's TTS slot cannot tell this engine from that one. `voice` is one
  of the 54 below. `speed` is a multiplier within 0.25-4. `stream` is accepted
  and ignored: kokoro-mlx splits a stream by sentence and the satellite already
  hands over one clause at a time (`internal/satellite/chunk.go`).
- Response `200`, `audio/pcm`: raw samples, 24 kHz, signed 16-bit, mono,
  little-endian, no header. The Go client resamples 3:2 to the device's 16 kHz.
  `response_format: "wav"` answers `audio/wav` instead, for a person with curl;
  unlike upstream's, its data chunk declares the length it carries.
- `400` with `{"detail": "..."}` for another model, an unknown voice, another
  format, a speed outside the range, blank text, or an input over 2000
  characters -- two minutes of speech, half the client's 30 s timeout.
- `500` if a render fails, because the satellite reads a dropped connection as
  the end of the utterance.

`GET /v1/audio/voices` answers `{"voices": [...]}`, which is what a configured
voice is worth checking against at startup.

`GET /health` answers `{"status": "ok", "model": "kokoro"}`. The port opens only
after the weights are loaded and one render has succeeded.

## The weights

`mlx-community/Kokoro-82M-bf16`, pinned in `voice.py` by its repo and revision:
a revision is immutable and the Hub checks each blob's digest as it lands, so
there is no second digest to keep. `kokoromlx --download-only` fetches the
config, the weights and the 54 voices as safetensors into `--model-dir`, 339
MiB; the `.pt` copies and the sample WAVs are the rest of the repo and nothing
reads them. The server loads from that directory, never the repo id: handed an
id, kokoro-mlx fetches the whole repo into the Hub's cache unpinned.

`en_core_web_sm` is a pinned dependency rather than a download, because
misaki's English G2P otherwise installs it into the running environment partway
through the first utterance.

## Running

From the repo root:

```sh norun
task tts:serve                    # fetches the weights, then serves on 127.0.0.1:8880
task test:models -- ./internal/provider/kokoro -kokoro-url http://127.0.0.1:8880
```

Loopback by default, unlike the containerised sidecars: this one runs on
somebody's Mac, where `0.0.0.0` would put the household's voice on the LAN.
`chorusd` in Compose reaches the host by gateway, so it needs `--host 0.0.0.0`
here and `KOKORO_URL: http://host.docker.internal:8880` there. Configuration is
environment or flags: `KOKOROMLX_HOST`, `KOKOROMLX_PORT`,
`KOKOROMLX_MODEL_DIR`.

`task tts:up` still runs the containerised CPU build on the same port, which is
what CI and anything that is not a Mac use. The two cannot run at once.

## Tests

```sh norun
task kokoromlx:test
```

The contract tests run against a fake and need neither MLX nor the weights nor
the network. `tests/test_real_model.py` renders on the GPU when
`KOKOROMLX_MODEL_DIR` holds them, and checks the thing worth checking: that a
clause comes out as speech at 24 kHz, faster than its own length. Nothing here
is in `task test` or `task test:sidecars` -- both run on Linux in CI, where
there is no Metal to fall back from.
