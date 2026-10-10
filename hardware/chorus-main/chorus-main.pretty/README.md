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
| MH1, MH3 | y = +4.675, x = +12.25 and −12.25: both ground, on the pads 1-40 row; 1.2 mm plated holes, 1.8 mm lands |
| MH2 | x = +12.25, y = −4.675, beside pad 41: the HAT's 5 V |
| MH4 | x = −12.25, y = −4.675, beside pad 80: the HAT's VBUS |
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

**The power contacts were measured, not read.** Hirose's model has no nets,
and FutureProofHomes' MH1-MH4 are their own names. Neither of the readings
their boards and Hirose's drawing suggested was right: both put the grounds
at one end. On 2026-10-10 Teagan put a meter on one of the household's HATs,
unpowered, face down with its USB-C ports toward them and J7's pin-1 dot at
the right, by the QR code:

- the two power contacts on the header's side, one at each end, beep to
  ground. That row is the plug's pads 1-40, so here MH1 and MH3 are the
  two holes on the +y row;
- of the two on the USB side, the one at the QR-code end beeps to the
  header's 5 V pin: MH2, beside pad 41;
- the other, at the speaker-connector end, is VBUS: MH4, beside pad 80.

A HAT revision that moves them is a new kit file and a new measurement.

The vendors' STEP models are not committed; download them beside the
footprints to see the board in 3D.

A vendored footprint keeps its own licence; say where it came from, at which
commit, and what changed, here.
