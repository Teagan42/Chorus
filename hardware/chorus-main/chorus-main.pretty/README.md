# chorus-main footprints

The footprints `parts.yaml` names as `chorus-main:*`, for the parts KiCad's
libraries lack. `fp-lib-table` beside `parts.yaml` makes this the
`chorus-main` library in any KiCad project opened from that folder.
`go test ./internal/board` holds each file's pads to its part's pad table,
and fails while any footprint `parts.yaml` names is missing.

| Footprint | Source | Licence | Changes |
|---|---|---|---|
| `Hirose_FX23L-80S-0.5SV` | Drawn here, from FutureProofHomes' [shoe rev 1 3D model](https://github.com/FutureProofHomes/Satellite1-Hardware/blob/2eb08ffaed8d9852d19b8acc86728d1af93d1c24/shoe/rev1shoe3D.step) at `2eb08ff`, measured with OpenCascade, then checked against Hirose's own model of the part | This repository's | See below |
| `LINK-PP_LPJG0926HENL` | [NabuCasa/yellow](https://github.com/NabuCasa/yellow/blob/c3d51b80e40ea01e34ef65c7d0aecc16a37980d4/Yellow.pretty/RJ45_LINK-PP_LPJG0926HENL_Horizontal.kicad_mod) at `c3d51b80`, the Home Assistant Yellow's magjack | CERN-OHL-W v2, [`cern_ohl_w_v2.txt`](cern_ohl_w_v2.txt); Copyright Nabu Casa 2023 | Renamed from `RJ45_LINK-PP_LPJG0926HENL_Horizontal`; the 3D model reference, to a file in the Yellow repository, removed |
| `Silvertel_Ag9912-MTB` | SnapEDA, `CONV_AG9912-MTB`, downloaded 2026-10-10 | [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/), as SnapEDA's FAQ licenses its CAD files; the design exception leaves boards made with it free | Renamed; its symbol (1 +VDC, 2 −VDC, 3 ADJ, 4 VIN+, 5 VIN−) matches the pad table |

The last two came over from rev A's `chorus-sat.pretty` unchanged.

## The FX23L-80S-0.5SV receptacle

In KiCad's coordinates (mm, y down, from the part's centre):

| Pads | Where |
|---|---|
| 1-40 | y = +4.95, from x = +9.75 (pad 1) to −9.75 (pad 40), 0.5 mm pitch; 0.3 × 1.6 mm |
| 41-80 | y = −4.95, from x = +9.75 (pad 41) to −9.75 (pad 80) |
| MH1, MH3 | x = −12.25, y = +4.675 and −4.675; 1.2 mm plated holes, 1.8 mm lands |
| MH2, MH4 | x = +12.25, y = +4.675 and −4.675 |
| MP1, MP2 | x = +17.65 and −17.65, y = 0; 1.9 × 3.0 mm fixing tabs |

It was first drawn from FutureProofHomes' shoe rev 1 3D model, then checked
against Hirose's own model of the part (`FX23L-80S-0.5SV.step`, Creo export
of 2016-02-29) and SnapEDA's KiCad footprint for it, both from Hirose's
download page. In Hirose's model:

- the signal tails are 0.2 mm wide and run from y ±4.45 to ±5.25; each pad
  runs 0.3 mm inside the heel and 0.5 mm past the toe;
- the power contacts' tails are 0.4 × 0.15 mm at (±12.25, ±4.675), 1.35 mm
  below the seating plane. The hole and land are those of Hirose's drawing
  for the FX23 power contacts, 1.2 mm and 1.6-1.8 mm, for a 1.6 mm board;
- the fixing tabs are flat, 1.27 × 2.7 mm at x ±17.5, so they are SMD pads.

SnapEDA's footprint is this one turned 180°, with the same pad order: pad 1
next to pad 41 at one end, pad 40 next to pad 80 at the other. The signal
pads' numbering also agrees with FutureProofHomes' boards: the shoe's
silkscreen pin-1 dot at the +x end of the row nearer the board's centre;
that row routed out in full, as pads 1-40 are on the HAT's schematic; and
the Satellite1 protoboard's breakout labels, "1" and "41", both at the +x
end, one beside each row. The HAT's own J7 has its pin-1 dot at the same
end. LCSC lists the part as C2911203.

**The power contacts are not settled.** Hirose's model has no nets, and
FutureProofHomes' MH1-MH4 are their own names. The −x pair sits in the
shoe's ground pour, so it is drawn as MH1 and MH3, the HAT's grounds; MH2
(5 V) is beside pad 1 and MH4 (VBUS) beside pad 41. Hirose's drawing numbers
its power contacts the other way, No. 1 beside pad 1, and if
FutureProofHomes followed it the grounds are at the pin-1 end. Drawn that
way round, this board would short PoE and the HAT's VBUS to ground. So
`parts.yaml` marks the part `unverified` until a meter settles it.

Turn an unpowered HAT face down (Core off, nothing plugged in), USB-C ports
toward you. J7's pin-1 dot is at its right end, by the QR code. The four
power contacts are J7's through-hole pins, two at each end of the plug. Set
the meter to continuity, and touch each one against:

- a ground (a USB-C port's shell): the two at one end beep. This board
  expects the left end, by the speaker connector;
- the header's 5V pin, marked on the silkscreen: one of the other two beeps.
  This board expects the one at the right end on the header's side.

The fourth is VBUS. Any other answer is a footprint change before ordering.

The vendors' STEP models are not committed; download them beside the
footprints to see the board in 3D.

A vendored footprint keeps its own licence; say where it came from, at which
commit, and what changed, here.
