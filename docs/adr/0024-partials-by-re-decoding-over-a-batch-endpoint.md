# 0024. Produce STT partials by re-decoding a growing buffer over a batch endpoint

- **Status:** accepted
- **Source:** SPEC §4.3, §4.5, §10, §11 · commits `c643c95`, `230a3f0`

The barge-in gate's third stage reads an STT partial (SPEC §4.3) and
endpointing runs over STT partials (SPEC §4.5), but the de facto self-hosted
STT contract, OpenAI's `POST /v1/audio/transcriptions`, is batch: one upload,
one transcript. So the seam is a batch `Transcriber` and partials come from an
adapter over it: `stt.Utterance` accumulates device PCM and re-decodes the
whole buffer so far every half second of audio, then decodes it once more on
`Finish` for the final. This is how the whisper_streaming family of systems
produces partials over a batch model, and it is what lets any provider behind
that contract fill the seam without the endpoint knowing it is streaming.

The cadence is a byte count, not a timer. A timer makes the cadence a property
of wall-clock time, so a stalled uplink keeps re-decoding the same audio for
the same partial, and a test needs a clock to drive it at all (CONTRIBUTING
§1). Counted in audio, the cadence is a property of what was said: no new
audio, no new partial, and a test writes half a second of bytes and watches one
decode happen. Re-decodes coalesce rather than queue, because every decode
reads the whole buffer anyway and a queue of them could only lag further behind
the speaker. The buffer is bounded at 30 s: a re-decode is quadratic in the
utterance's length, and past the ~20 s silence backstop an utterance is a stuck
mic or the television, not a turn.

The server pinned is speaches at `ghcr.io/speaches-ai/speaches:0.9.0-rc.1-cpu`,
serving the ONNX export `istupakov/parakeet-tdt-0.6b-v2-onnx`. It is the first
tag carrying Parakeet, added in that release, and as of 2026-10-08 the last
tag published, which is why a pre-release is pinned: `latest-cpu` moves. The
package is named after the server, not the model, as `kokoro` and `ollama` are.

What is and is not proven about SPEC §10's Parakeet row, as of this record:

- Established from the server's source at the pinned tag: the Parakeet
  executor's registry filter matches `istupakov/parakeet-tdt`, its model files
  are the ONNX export's, the transcription route serves it with
  `response_format` `json` or `text` only, and it refuses streaming. The
  `json` answer carries `text` and nothing else, so `stt.Result` carries text
  and nothing else; the row's word timestamps are not reachable through this
  server today.
- Not established: no decode has been run against a real sidecar from this
  change, so the CPU image's decode cost — what the partial cadence and the
  final's share of SPEC §11's budget both depend on — is unmeasured. The
  models tier logs it on every run. The Hugging Face repository the model id
  names was not reachable from the environment this was written in; the id
  follows the filter and the author's own `-v3-onnx` sibling the server's
  pull request names.
- The row's "streaming" claim is Parakeet's own capability, and this server
  does not expose it. Partials come from the adapter above regardless of
  provider, so nothing in the orchestrator is waiting on it.

## Alternatives rejected

NVIDIA's NIM serves the same multipart route and would prove the row directly,
but it needs an NGC key, a GPU and `--shm-size`, and the only tag its deploy
page names is `latest`. A dev box has none of those, and SPEC §13 pins
sidecars.

A streaming STT protocol of our own (WebSocket, gRPC) was rejected for phase
1. It would bind the seam to one server's protocol where the batch route is
served by every self-hosted STT server, and the adapter already gives the gate
what it needs. ADR-0014 says the same about the turn engine.

Returning the last partial from a cancelled utterance was rejected. A session
that ended no longer wants the turn, a partial is not the utterance, and a
clean error is the one answer that cannot be mistaken for a transcript.

## Forecloses

Nothing in the orchestrator, by design: a provider that streams natively can
fill `stt.Transcriber` and its partials would replace the adapter's behind the
same `Partials` channel. What it does fix is the cost model — a partial is a
whole decode — so the cadence cannot tighten past what one decode of the
buffer costs, which is the number the models tier exists to report.
