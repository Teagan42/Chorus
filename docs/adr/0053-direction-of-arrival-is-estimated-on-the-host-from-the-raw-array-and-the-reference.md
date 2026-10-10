# 0053. Direction of arrival is estimated on the host, from the raw array and the reference

- **Status:** proposed
- **Source:** SPEC §3.2, §3.3.2, §4.3, §5, §8, §9 · ADR-0010, ADR-0033,
  ADR-0047, ADR-0050 · FutureProofHomes/Satellite1-XMOS `bb411c7`,
  xmos/fwk_voice `e230d55`, xmos/lib_mic_array `e4996bd` (v5.3.0),
  xmos/sln_voice `a24cd57`, espressif/esp-idf `f5c3654` (v5.4.2),
  FutureProofHomes/Satellite1-ESPHome `46511ed`

ADR-0047 put eight mics on the rev A board so a voice's direction could help
the barge-in gate (SPEC §4.3) and attribution (SPEC §5), and left open where
the estimator runs. The barge-in gate acts while the satellite is speaking.
That is exactly when every mic hears the satellite's own speaker loudest, so
whichever side estimates direction must cancel or mask that echo on every
channel it uses.

The XU316 cannot do that for eight channels. Its echo canceller is compiled
for at most two mic channels (fwk_voice `modules/lib_aec/api/aec_defines.h:30`).
Two channels already cost about 275 KB and 96 MIPS (sln_voice
`doc/programming_guide/04_extending.rst:21-22`), out of 512 KB per tile. The
host can do it. It already holds what the satellite played, and with one
change to the I2S packing (below) it holds that signal on the same sample
clock as the mics.

So the satellite sends its eight decimated mics and its playback reference.
The host estimates direction from them, where the journal records the inputs
and a better estimator can be re-run over history (SPEC §8, §9). The
estimator's arithmetic was never the obstacle: an eight-mic SRP-PHAT at
16 kHz comes to roughly 10–25 MIPS (inferred). Getting eight echo-free
channels was the obstacle.

## What the satellite sends

**The I2S link stays as it is.** The XU316 is I2S master at 48 kHz with
64-bit frames (Satellite1-XMOS `src/app_conf.h:148-149`). Today it repeats
each 16 kHz sample of its two processed channels three times, and the ESP32
keeps every third (Satellite1-ESPHome
`esphome/components/satellite1/microphone/sat1_microphone.cpp:222-237`). That
is 192 bits per 16 kHz sample period, two-thirds of them duplicates.

Repacked at 16 bits per channel, the same 192 bits carry:

- the two processed channels, unchanged in content, so voiceprints still
  transfer (ADR-0047);
- the eight raw mics;
- the XU316's 16 kHz reference.

That is 176 bits, with 16 left for a sync tag. sln_voice ships the same trick
at six channels, using LSB tags as the frame marker (`examples/ffva/src/main.c:192-212`).

The rejected transports are TDM and a second data line, for these reasons:

- The ESP32-S3 caps a TDM frame at 128 bits (esp-idf
  `components/hal/esp32s3/include/hal/i2s_ll.h:37`).
- fwk_io has no TDM master.
- Rev A has one free tile-1 pin, X1D09, and the ESP32's second I2S
  controller is already `AMP_SENSE`'s.

**The reference is already aligned.** It is the ESP32's playback as the
XU316 itself downsampled it for its echo canceller (Satellite1-XMOS
`src/main.c:54-135`). The XU316 pairs it with each mic frame at zero offset
(`src/app_conf.h:39`), so mics and reference share one clock and one
decimator. Reconstructing that alignment on the host from `played` reports
would be a guess about timing, which ADR-0033 already refused once.

**On the wire it is one new frame, `0x06 array`, device to host:**

```text
first_sample:u64   index of this frame's first sample on the 16 kHz clock,
                   counted from the hello, so a dropped chunk is a gap
samples[]          s16le, interleaved: mics in hello order, then processed
                   channel 0, then the reference
```

Processed channel 0 rides in the array frame as well as in its own `mic`
frames, which carry no index. That way the voice activity the estimator gates on
and the utterance window kept beside it (below) are indexed on the same
samples as the mics. A dropped `mic` chunk then cannot shift which raw
samples a speech decision applies to.

- The hello gains trailing bytes:
  - `array_mics:u8`;
  - then `x:i16, y:i16` per mic, in 0.1 mm in the board's top view.

  The device states its geometry just as it states its format.
- `mic_enable` gains `flags` bit 1, which asks for array frames. The host
  opts in per link, and only on one whose uplink can carry about 3.1 Mbit/s.
- A 32 ms frame is 10,248 bytes, well inside the 64 KiB payload limit.

**The protocol version stays 2.** Every part of this is additive:

- A host from before this ignores the hello's extra bytes (`ParseHello`
  reads seven).
- That older host never sets bit 1, so it never receives array frames.
- Firmware from before this reads only bit 0 of `mic_enable`
  (`chorus_bridge.cpp:420`) and never announces an array.

This is the case the README's skippable `length` field was written for.
ADR-0033's refusal applied to a change that altered an existing frame's
meaning; this change alters none.

## What the host does with it

- Each utterance's window of the array is kept as a blob beside it, as
  ADR-0050 keeps the second channel.
- The direction is an event that names its estimator, as STT and TTS
  identities are named (SPEC §8). A future on-device estimator would write
  the same event under its own name.
- The first estimator is SRP-PHAT over the eight mics. It down-weights the
  time-frequency bins the reference dominates, and it uses only frames that
  the processed channel 0 in the same frame marks as speech. If masking leaves
  the bench test biased toward the speaker, the next step is full
  multichannel echo cancellation on the host, from the same frame.
- What consumes the direction gets its own ADR. The first consumer is the
  barge-in gate, learning each room's television bearing from the rejected
  candidates it already logs (SPEC §4.3).

## Alternatives rejected

**Direction estimated on the XU316.**
- **Capacity:**
  - Decimating eight mics instead of two costs about 76 MIPS and a second
    tile-1 thread whichever side estimates (lib_mic_array
    `doc/programming_guide/src/resource_usage.rst:118-137`).
  - The estimator would fit in one of tile 0's two spare threads.
- **Echo:**
  - Without per-mic echo cancellation on all eight channels, the XU316
    would have to stop estimating whenever the reference carries energy.
  - Eight-channel echo cancellation would need about 320 KB and over 200
    MIPS (inferred from Satellite1-XMOS `aec/aec_memory_pool.h`), beside a
    pipeline that already fills the tile.
  - A gated estimator gives the barge-in gate nothing in the one situation
    it exists for.
- **Development:** every change to the estimator would need a firmware
  release and a reflash. The journal would hold its outputs but never its
  inputs, so the estimator could not be improved against past data.

**An XVF3800 instead.** It does per-mic echo cancellation, beamforming and
direction finding on four mics, but its firmware is closed. Swapping it in
changes the frontend, and ADR-0047 already rejected that because voiceprints
would need re-enrolling.

**DoA from the processed channels.** Its echo canceller already runs per mic
(Satellite1-XMOS `audio_pipelines/reference/fixed_delay/audio_pipeline_t1.c:110-130`),
but only on two mics, and the interference canceller then merges them into
one. One pair is a line, not a circle: it cannot tell front from back.

## Forecloses

- **Direction on a satellite whose uplink cannot carry 3.1 Mbit/s.** Rev A
  measures direction over Ethernet only. Its Wi-Fi build, and any
  Satellite1, never asks for array frames, because airtime is already the
  limit there (SPEC §3.3.2). Getting a direction on Wi-Fi would take the
  on-device estimator rejected above, writing the same event.
- **The stock XMOS release.** The satellite runs a derivative of the
  Satellite1 firmware. Its licence permits that, provided modified firmware
  carries a source-availability notice when distributed (ADR-0047). The
  derivative makes these changes:
  - eight mics at 800 MHz;
  - a pipeline whose channel count no longer follows the mic count;
  - the repacked I2S output.
- **FutureProofHomes' microphone component.** Unpacking the frame is
  `chorus_bridge`'s job, so `micro_wake_word` takes processed channel 0
  from the bridge rather than from `sat1_microphone`.
