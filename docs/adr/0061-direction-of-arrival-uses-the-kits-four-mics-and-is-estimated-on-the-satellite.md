# 0061. Direction of arrival uses the kit's four mics and is estimated on the satellite

- **Status:** proposed
- **Source:** SPEC §3.2, §3.3.2, §4.3, §5, §8 · ADR-0053, ADR-0055 ·
  FutureProofHomes/Satellite1-XMOS `bb411c7`, Satellite1-Hardware `2eb08ff`,
  xmos/fwk_voice `e230d55`, xmos/lib_mic_array `e4996bd` (v5.3.0),
  espressif/esp-dsp `3c8ac0f`

Amends [ADR-0053](0053-direction-of-arrival-is-estimated-on-the-host-from-the-raw-array-and-the-reference.md).
ADR-0055 made every satellite a bought Satellite1 kit. That leaves ADR-0053
with four mics instead of eight, and most of the household's satellites on
Wi-Fi, where ADR-0053 never asks for the array at all. So two of its choices
change and the rest stand.

**Four mics, not eight.** The HAT's mics sit on a 32.08 mm radius at 90°
steps, in the board's top view (Satellite1-Hardware `hat/rev6.1hat3D.step`,
as ADR-0053 read it):

- Adjacent mics are 45.4 mm apart, so spatial aliasing starts at about 3.8 kHz.
- Opposite mics are 64.2 mm apart. The largest delay is 187 µs, about three
  samples at 16 kHz, so integer-lag GCC is too coarse.

The estimator is SRP-PHAT in the frequency domain, which steers with
fractional delays as phase shifts:

- band 300–3,800 Hz;
- a 5° azimuth grid, 72 steps;
- a 512-point window, every 32 ms.

A planar array gives the full circle of azimuth and no elevation.
Reverberant rooms should give about ±10–20° (inferred; the bench measures
it).

**Estimated on the satellite, not the host.** ADR-0053 put the estimator on
the host and foreclosed a direction on any uplink that cannot carry the
array, and that is now most satellites. The four mics, two processed
channels and the reference come to about 1.5 Mbit/s. On Wi-Fi, airtime is
the measured limit already (SPEC §3.3.2). The ESP32-S3 has room for the
work. Per 32 ms hop it does four FFTs, six cross-spectra and 72 steering
directions over about 112 bins: 0.3–0.64 million cycles, or 4–8 % of one
240 MHz core. That figure is inferred from esp-dsp's S3 benchmarks (esp-dsp
`docs/esp-dsp-benchmarks.rst:16, 38, 47, 60`) and is to be profiled beside
`micro_wake_word` and `chorus_bridge`. The satellite sends a bearing, not
audio. The estimator works the same on Wi-Fi and on the main board's
Ethernet.

**The array frame stays, and is opt-in.** ADR-0053's `0x06 array` frame
carries four mics instead of eight. The host asks for it only where the link
can carry it: the main board's Ethernet, or a satellite being tuned. There,
the inputs are journalled as ADR-0053 says. A better estimator can then be
re-run over them, and the host's SRP-PHAT is how the on-device one is tuned
and checked.

## What the satellite does

- **The XU316 runs a derivative firmware.** It decimates all four mics. It
  echo-cancels the same two as today, and packs both processed channels, the
  four raw mics and its 16 kHz reference into the existing I2S link with a
  sync word ([the specification](../hardware/xmos-four-mics.md)). Its
  processed audio is the stock firmware's, so voiceprints still match
  (SPEC §5).
- **`chorus_bridge` reads the link itself.** It locks on the sync word and
  hands processed channel 0 to `micro_wake_word` and the uplink as before. It
  runs SRP-PHAT on the four mics with the reference beside them, on the same
  16 kHz clock.
- **Its own speaker is masked.** During barge-in, the satellite's speaker
  arrives at all four mics almost at once, so it adds energy at every
  bearing and peaks at none. The estimator drops time-frequency bins where
  the reference dominates. It reports a bearing only from frames that
  processed channel 0's voice activity marks as speech. How well the
  masking works is characterised on the living-room kit with the speaker in
  its enclosure.

## On the wire

Everything is additive; the protocol version stays 2.

- **The hello's array geometry** (ADR-0053) carries four mics, one per
  decimated input, in input order. The positions come from a tap test on
  the bench, not from the schematic. After the geometry, it carries the
  estimator's name as `len:u8` followed by that many bytes of UTF-8.
- **`mic_enable`'s `flags` bit 2 asks for direction frames.** Bit 1 still
  asks for array frames.
- **New frame `0x07 direction`, device to host:**

  ```text
  first_sample:u64  index of the window's first sample on the 16 kHz clock,
                    counted from the hello, as the array frame's is
  samples:u16       the window's length
  azimuth:u16       hundredths of a degree in the board's top view, from +x
                    toward +y
  confidence:u8     the peak's share of the steered response, 0-255
  ```

  `flags` bit 0 marks a window where masking removed most of the band.

- **The host journals each direction as an event naming its estimator**,
  as ADR-0053 planned. The on-device estimator and the host's carry
  different names, so their outputs can be compared over the same samples.
  What consumes a direction still gets its own ADR. The first consumer is
  the barge-in gate (SPEC §4.3), which learns each room's loud bearings,
  such as the television, as a prior from the candidates it already
  rejects.

The status stays proposed until three things hold:

- the derivative is built and tile 1's free memory is measured;
- the estimator is profiled on a kit beside `micro_wake_word`;
- the living-room kit's bearings are checked against a measured talker
  position, with and without its own speech playing.

## Alternatives rejected

- **The host only, as ADR-0053 wrote it.** Every satellite on Wi-Fi gets no
  direction, and on Wi-Fi the array costs the airtime SPEC §3.3.2 measured
  as the limit.
- **The XU316.** Its pipeline already fills tile 1 (ADR-0053). fwk_voice has
  no beamformer or direction estimate to read out: its interference
  canceller's filter spans two mics and adapts toward the noise, not the
  talker (fwk_voice `modules/lib_ic/api/ic_state.h:204-206`). And every
  change to the estimator would be a firmware release.
- **Raw mics at 48 kHz.** Four of them fill the whole link, for bandwidth
  the array aliases above 3.8 kHz anyway.

## Forecloses

- **Re-running a better estimator over a Wi-Fi satellite's history.** Its
  journal holds bearings, not the audio they came from. Only satellites that
  sent array frames can be re-estimated.
- **The stock XMOS release and FutureProofHomes' microphone component** on a
  satellite that estimates direction, as ADR-0053 already said.
- **Elevation, and more than four mics.** Neither is possible on the HAT
  (ADR-0055).
