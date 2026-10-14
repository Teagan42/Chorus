# 0092. The voice renders on this Mac's GPU, through a sidecar that is not a workspace member

- **Status:** accepted · refines [ADR-0032](0032-record-the-ear-and-the-voice-as-named-versions.md)
- **Source:** SPEC §10, §11 · CONTRIBUTING §5

`internal/provider/kokoro` was built against Kokoro-FastAPI's CPU image, which
renders at roughly a third of real time: measured 2026-10-07, 0.56-0.92 s for a
clause and 3.9 s for a paragraph, which is most of SPEC §11's budget spent
before the first word leaves the speaker. The same weights on MLX, measured
2026-10-13 on this Mac, render a clause in 0.21 s and a 10.9 s paragraph in
0.95 s -- about a tenth of the audio's own length.

**A second sidecar serving the same contract, not a second provider.**
`sidecars/kokoromlx` answers the part of `/v1/audio/speech` the Go client
already speaks, on the same port, at the same native 24 kHz the 3:2 resampler
is wired to. Pointing `KOKORO_URL` at it is the whole change: the models tier
passes against it unaltered, including the unknown-voice 400 and the rate
check. Nothing in Go moved, so there is no second code path to keep honest.

**It runs on the host and has no image.** MLX reaches Metal directly and
Docker Desktop on macOS passes no GPU through, so a container would be the CPU
build again at best. `task tts:serve` is the host process; `task tts:up` still
runs the image, which is what CI and anything that is not a Mac use. The two
share the port, so only one can be the voice at a time.

**It is not a uv workspace member.** kokoro-mlx needs misaki for English G2P,
which drags spacy and torch -- about a gigabyte -- into any environment that
resolves it. The workspace's one lockfile is shared by `task test:sidecars` and
CI on Linux, where there is no Metal and this cannot run at all, so the member
is excluded and keeps its own lockfile and interpreter. That is the same reason
`smartturn:corpus` is a standalone script.

## Alternatives rejected

**Replacing the image.** CI is Linux and the household's stack may not always
be this Mac. The Docker build stays the default and the portable one; MLX is
what a Mac can do instead.

**A Go provider talking to kokoro-mlx directly.** There is nothing to talk to:
it is an in-process Python library with no server, and Go cannot host it.

**Taking kokoro-mlx's streaming.** It splits a stream by sentence, and the
satellite already hands over one clause at a time
(`internal/satellite/chunk.go`). A paragraph that
is a single sentence yields one chunk, so the endpoint renders whole and
`stream` is accepted and ignored, as it effectively is upstream.

## Forecloses

The journal's TTS slot records `model/voice` and the model is `kokoro` either
way, because that is the one name the endpoint serves and the client sends
(ADR-0032). So a turn rendered here is indistinguishable in the log from one
rendered by the CPU image, and a latency regression traced through the journal
will not say which engine spoke. Changing it means changing what the client
sends, which is also what it records.

The weights are pinned by repo and revision rather than by digest, unlike the
other two sidecars' assets: a Hub revision is immutable and the Hub verifies
each blob as it lands, but there is no local check at boot that would catch a
`--model-dir` someone edited.
