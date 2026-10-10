# chorus-main footprints

The footprints `parts.yaml` names as `chorus-main:*`, for the parts KiCad's
libraries lack. `fp-lib-table` beside `parts.yaml` makes this the
`chorus-main` library in any KiCad project opened from that folder.
`go test ./internal/board` holds each file's pads to its part's pad table,
and fails while any footprint `parts.yaml` names is missing.

| Footprint | Source | Licence | Changes |
|---|---|---|---|
| `Hirose_FX23L-80S-0.5SV` | Drawn here, from FutureProofHomes' [shoe rev 1 3D model](https://github.com/FutureProofHomes/Satellite1-Hardware/blob/2eb08ffaed8d9852d19b8acc86728d1af93d1c24/shoe/rev1shoe3D.step) at `2eb08ff`, measured with OpenCascade | This repository's | See below |
| `LINK-PP_LPJG0926HENL` | [NabuCasa/yellow](https://github.com/NabuCasa/yellow/blob/c3d51b80e40ea01e34ef65c7d0aecc16a37980d4/Yellow.pretty/RJ45_LINK-PP_LPJG0926HENL_Horizontal.kicad_mod) at `c3d51b80`, the Home Assistant Yellow's magjack | CERN-OHL-W v2, [`cern_ohl_w_v2.txt`](cern_ohl_w_v2.txt); Copyright Nabu Casa 2023 | Renamed from `RJ45_LINK-PP_LPJG0926HENL_Horizontal`; the 3D model reference, to a file in the Yellow repository, removed |
| `Silvertel_Ag9912-MTB` | SnapEDA, `CONV_AG9912-MTB`, downloaded 2026-10-10 | [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/), as SnapEDA's FAQ licenses its CAD files; the design exception leaves boards made with it free | Renamed; its symbol (1 +VDC, 2 −VDC, 3 ADJ, 4 VIN+, 5 VIN−) matches the pad table |

The last two came over from rev A's `chorus-sat.pretty` unchanged.

## The FX23L-80S-0.5SV receptacle

In KiCad's coordinates (mm, y down, from the part's centre):

| Pads | Where |
|---|---|
| 1-40 | y = +5.05, from x = +9.75 (pad 1) to −9.75 (pad 40), 0.5 mm pitch; 0.3 × 1.5 mm |
| 41-80 | y = −5.05, from x = +9.75 (pad 41) to −9.75 (pad 80) |
| MH1, MH3 | x = −12.25, y = +4.675 and −4.675; 1.2 mm plated holes |
| MH2, MH4 | x = +12.25, y = +4.675 and −4.675 |
| MP1, MP2 | x = +17.65 and −17.65, y = 0; 1.9 × 3.0 mm fixing tabs |

The tails sit at y ±4.45 to ±5.25 in the model; each pad runs 0.15 mm inside
the heel and 0.55 mm past the toe. The holes are the shoe board's own, read
from the same model. The silkscreen dot is pin 1.

The signal pads' numbering is read from FutureProofHomes' boards, not
guessed: the shoe's silkscreen pin-1 dot at the +x end of the row nearer the
board's centre; that row routed out in full, as pads 1-40 are on the HAT's
schematic; and the Satellite1 protoboard's breakout labels, "1" and "41",
both at the +x end, one beside each row.

**The power contacts are not.** The −x pair sits in the shoe's ground pour,
so it is drawn as MH1 and MH3, both ground on the HAT. Which of the +x pair
is MH2 (5 V) and which MH4 (VBUS) is a guess. `parts.yaml` marks the part
`unverified` until Hirose's or a vendor's footprint, or a meter on a powered
HAT, settles it; [the design](../../../docs/hardware/README.md#the-receptacle)
says how.

The vendors' STEP models are not committed; download them beside the
footprints to see the board in 3D.

A vendored footprint keeps its own licence; say where it came from, at which
commit, and what changed, here.
