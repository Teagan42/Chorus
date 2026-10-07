# 0023. Take raw PCM from Kokoro and resample on the host

- **Status:** accepted
- **Source:** SPEC §10, §3.2 · commit `180884b`

Kokoro-82M renders at 24 kHz and its endpoint has no rate parameter, while the
device is fixed at 16 kHz — the XMOS pipeline and micro_wake_word both run
there and resampling on an ESP32 buys nothing (SPEC §3.2). So the host
resamples, and it does so with a filtered 3:2 polyphase FIR rather than by
dropping every third sample: plain decimation folds everything above 8 kHz back
into the band, landing a 10 kHz sibilant at 6 kHz in the middle of speech,
where no later processing can separate it from the voice.

The transfer is raw `pcm`, not `wav`. This endpoint writes a data-chunk length
of `0xFFFFFFFF` even on a complete non-streaming response, so the header is
something a reader has to know to ignore; the samples after it are the same
samples `pcm` returns on its own.

`stream: true` was measured and rejected, which is the part of SPEC §10's TTS
row that did not survive contact. On the CPU image nothing is flushed early:
the first byte of a paragraph arrives at 3.9 s, the same moment as the last,
against a §11 budget of ~700 ms to first audio. A clause costs 0.5–0.9 s. The
streaming the row promises would have to come from the GPU image to exist at
all, so first audio depends on the satellite's clause chunking
(`internal/satellite/chunk.go`) and not on the sidecar.

Per-word timings do exist, on `/dev/captioned_speech`, which returns them
beside the audio base64'd into JSON. They do not reopen SPEC §15 item 1: the
truncation point comes from the DAC, not from the synthesiser (ADR-0020), and
these would only refine which text a cut is attributed to. A `/dev/` endpoint
that forgoes the audio transfer to carry base64 is not the place to put the
speech path.

## Alternatives rejected

Asking the device to resample was rejected by SPEC §3.2 before this: the
firmware is fixed at 16 kHz in both directions and an ESP32 has better uses for
the cycles.

Linear interpolation was rejected over filtering. It is a one-line resampler
with the aliasing described above and a passband that sags, and the alternative
costs 33 multiply-adds per output sample — about a megaflop a second against a
16 kHz stream.

Piper as the phase-1 TTS was rejected for now. SPEC §10 keeps it as the
degraded fallback and nothing here forecloses it, but Kokoro's quality is the
reason the row names it first and the measured render cost is affordable per
clause.

## Forecloses

Sentence-level streaming out of this seam, for as long as `satellite.Synth`
returns a complete `[]byte`. Measured behaviour makes that free today, but a
GPU sidecar that does flush early would need the interface to carry a stream
before the satellite could take advantage of it.
