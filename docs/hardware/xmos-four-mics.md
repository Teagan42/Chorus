# The XMOS firmware for four raw mics

The Satellite1's XU316 hears all four of the HAT's mics but decimates two of
them. [ADR-0061](../adr/0061-direction-of-arrival-uses-the-kits-four-mics-and-is-estimated-on-the-satellite.md)
needs all four, raw, beside the playback reference, so the satellite runs a
derivative of FutureProofHomes' firmware. This page is that derivative's
specification. It changes:

- how many mics are decimated;
- the size of the voice pipeline, which stops following the mic count;
- how the output is packed onto the I2S link the ESP32 already reads.

Its processed output does not change, so voiceprints enrolled on the stock
firmware still match (SPEC §5).

Every source is pinned to a commit, and a claim marked *inferred* is
reasoned, not stated by a source:

| Tag | Repository at commit |
|---|---|
| SXF | FutureProofHomes/Satellite1-XMOS `bb411c7` |
| MA5 | xmos/lib_mic_array `e4996bd` (v5.3.0, what SXF builds) |
| SLV | xmos/sln_voice `a24cd57` |
| SHW | FutureProofHomes/Satellite1-Hardware `2eb08ff` |

## What the stock firmware does

- **It captures eight inputs and keeps two.** PDM receive reads all eight
  DDR inputs of port 4D, and the decimator keeps inputs 4 and 5:
  `MIC_COUNT=2`, `MIC_MAPPING "4, 5"` (SXF
  `bsp_config/SATELLITE1/SATELLITE1.cmake:35-43, 55-61`).
- **Input *k* is pin *k* on one clock edge, and *k* + 4 the same pin on the
  other** (the comment at `SATELLITE1.cmake:35-40`). Pin 0 is X1D16,
  `MIC_DATA_1`, and pin 1 is X1D17, `MIC_DATA_2`. Pins 2 and 3 are not
  connected to mics.
- **Each data line carries two mics**, one with SEL low and one high (SHW
  `hat/rev6.1hatSCH.pdf` p5): MK1 and MK2 on `MIC_DATA_2`, MK3 and MK4 on
  `MIC_DATA_1`. Inputs 4 and 5 take one mic from each line on the same edge,
  so the stock firmware hears an opposite pair, 64.2 mm apart. Which pair,
  MK2/MK4 or MK1/MK3, depends on the edge, and that is measured, not read
  (below).
- **The pipeline is sized by the mic count.** `appconfAUDIO_PIPELINE_CHANNELS`
  is `MIC_ARRAY_CONFIG_MIC_COUNT` (SXF `src/app_conf.h:25`). It sizes each
  pipeline frame (`audio_pipelines/reference/fixed_delay/audio_pipeline_dsp.h:41-51`)
  and the I2S stack buffers (`src/main.c:64, 93, 113, 205`).
- **The output repeats itself.** The XU316 is I2S master at 48 kHz with
  64-bit frames (`src/app_conf.h:148-149`). Each 16 kHz sample of the two
  processed channels goes out three times (`src/main.c:207-236`), and the
  ESP32 keeps every third (Satellite1-ESPHome
  `esphome/components/satellite1/microphone/sat1_microphone.cpp:222-237`).
  Two thirds of the link is duplicates.

## What the derivative changes

### Four mics decimated, two still cancelled

| Setting | Stock | Derivative |
|---|---|---|
| `MIC_ARRAY_CONFIG_MIC_COUNT` | 2 | 4 |
| `MIC_MAPPING` | `"4, 5"` | `"4, 5, 0, 1"` |
| `appconfAUDIO_PIPELINE_CHANNELS` | `MIC_COUNT` | 2, fixed |
| Echo canceller, interference canceller | inputs 4, 5 | inputs 4, 5 |

- **The mapping keeps the stock pair first.** The pipeline takes decimated
  channels 0 and 1, which are inputs 4 and 5 as before. The echo canceller
  and the interference canceller therefore see exactly the mics they see
  today, and the processed channels are the stock firmware's.
- **The pipeline stays at two channels.** The echo canceller is compiled for
  at most two mics (fwk_voice `modules/lib_aec/api/aec_defines.h:30`), and
  sizing every pipeline frame for four would double it on both tiles for
  nothing. The two extra decimated channels bypass the pipeline and go
  straight to the output (below).
- **One decimator thread still carries it.** In ISR mode, which the FPH
  wrapper uses (SXF `modules/fph/rtos_mic_array/vanilla/mic_array_vanilla.cpp:131-139`),
  lib_mic_array measures 22.0 MIPS for two mics and 43.7 MIPS for four
  (MA5 `doc/programming_guide/src/resource_usage.rst:118-137`). Four fits
  inside the 75 MIPS one thread is guaranteed with all eight of tile 1's
  threads running at 600 MHz (*inferred*). Eight mics would not have fitted,
  which is part of why ADR-0053's eight needed a second thread.
- **About 8 KB more on tile 1** for the decimator's buffers, which scale with
  the mic count (fwk_rtos `modules/drivers/mic_array/api/rtos_mic_array.h:65`;
  *inferred* size). Tile 1's free memory is not published; the build below
  measures it before anything ships.

### The output, repacked

The link stays what it is: the XU316 is master, 48 kHz, two 32-bit slots per
frame. Nothing on the ESP32 changes its clocking. What changes is what the
three frames of each 16 kHz sample period carry. Each 32-bit slot holds two
signed 16-bit samples, high half first:

| 48 kHz frame | Left, high | Left, low | Right, high | Right, low |
|---|---|---|---|---|
| 0 | sync word | processed 0 | processed 1 | reference |
| 1 | mic, input 4 | mic, input 5 | mic, input 0 | mic, input 1 |
| 2 | 0 | 0 | 0 | 0 |

- **Processed 0 and 1** are the stock firmware's two outputs: echo
  cancellation, interference cancellation, noise suppression, and gain
  control on channel 0 only.
- **The reference** is what the ESP32 played, as the XU316 downsampled it for
  its own echo canceller and paired it with each mic frame at zero offset
  (SXF `src/main.c:54-135`, `src/app_conf.h:39`). Mics and reference share one
  clock and one decimator, so no timing is reconstructed later.
- **The mics** are decimated to 16 kHz and not processed further, in input
  order. The geometry for each input is stated by the satellite, not
  assumed (ADR-0061).
- **The sync word** is `0xC4` in its high byte and a period counter in its
  low byte, counting up and wrapping at 255. A reader locks when eight
  periods in a row carry `0xC4` and a counter one above the last; one
  mismatch drops the lock. A sample of real audio can carry `0xC4`, but not
  eight times in a row with a counter, three frames apart (*inferred*).
- **Frame 2 is zero**, reserved. It makes 112 bits of samples and a 16-bit
  tag out of the 192 bits each period has. sln_voice ships the same packing
  idea at six channels, with tag bits marking the frame (SLV
  `examples/ffva/src/main.c:192-212`).

Raw mics stay at 16 kHz. At 48 kHz four mics would fill the whole link, and
direction finding has no use for anything above 3.8 kHz on this array
(ADR-0061).

### What it breaks on the ESP32

`microphone: platform: satellite1` assumes three copies of each sample and
keeps every third. On the derivative that would hand `micro_wake_word` a mix
of sync words and raw mics. So the derivative pairs with a `chorus_bridge`
that reads the I2S frames itself, locks on the sync word, and hands
processed channel 0 to `micro_wake_word` and the uplink as before, as
ADR-0053 already planned. A Satellite1 still on the stock firmware keeps
`satellite1.yaml` as it is.

## Building and checking it

1. **Build with `DEBUG_PRINT_ENABLE=1`** (SXF `firmware.cmake:32`) and read
   `mem_analysis()`'s heap minimum on each tile over xscope (SXF
   `src/main.c:275-284`). The derivative ships only if tile 1 keeps headroom.
2. **Tap test, on the bench.** Scratch each mic's port in turn and note which
   input moves. This settles which mic each input is, and so which pair the
   stock firmware hears. The schematic and FutureProofHomes' compass labels
   suggest MK2/MK4 on inputs 4 and 5, but only weakly. The answer goes into
   the geometry the satellite states, and `esphome/` carries it.
3. **Lock and stay locked.** Stream for an hour on the living-room kit with
   the television on; the reader must not drop its lock.
4. **Same processed audio.** Record a household phrase through the stock and
   the derivative firmware from the same spot. Processed channel 0 must
   match within the AGC's own variation, and the speaker-ID sidecar must
   score the two takes as the same person.

## Licence

Satellite1-XMOS and the XMOS libraries it links are under the XMOS Public
Licence v1 (SXF `LICENSE.md`). A modified build distributed to anyone must:

- keep XMOS's notices and include the licence (§2.1(a), (b));
- mark each modified file as changed, with the date (§2.2(b));
- carry a prominent notice, in the code and its documentation, of where the
  original source is available (§2.3).

Publishing the modified source is optional, but if it is published it is
under the same licence. The licence also limits commercial use to XMOS
silicon (§2.1.2, §2.2(c)), which a Satellite1 is. FreeRTOS is MIT. The voice
activity model's TensorFlow Lite runtime ships in a wheel whose licence is
unclear (`xmos-ai-tools` 1.3.1), so check that before handing a binary to
anyone. This is not legal advice.

## Sources

The research behind this page, with every citation pinned to a commit, is in
the project's shared files: `hardware-research/handoff-open-questions.md`
and `hardware-research/doa-xu316-capacity.md`.
