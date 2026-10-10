# chorus-sat footprints

The footprints `parts.yaml` names as `chorus-sat:*`, for the parts KiCad's
libraries lack. `fp-lib-table` beside `parts.yaml` makes this the
`chorus-sat` library in any KiCad project opened from that folder.
`go test ./internal/board` holds each file's pads to its part's pad table,
and fails while any footprint `parts.yaml` names is missing.

| Footprint | Source | Licence | Changes |
|---|---|---|---|
| `LINK-PP_LPJG0926HENL` | [NabuCasa/yellow](https://github.com/NabuCasa/yellow/blob/c3d51b80e40ea01e34ef65c7d0aecc16a37980d4/Yellow.pretty/RJ45_LINK-PP_LPJG0926HENL_Horizontal.kicad_mod) at `c3d51b80`, the Home Assistant Yellow's magjack | CERN-OHL-W v2, [`cern_ohl_w_v2.txt`](cern_ohl_w_v2.txt); Copyright Nabu Casa 2023 | Renamed from `RJ45_LINK-PP_LPJG0926HENL_Horizontal`; the 3D model reference, to a file in the Yellow repository, removed |
| `SK6812MINI-E_DatasheetPads` | Drawn here: the geometry of KiCad's `LED_SK6812MINI-E_3.2x2.8mm_P1.5mm_ReverseMount` from [kicad-footprints](https://gitlab.com/kicad/libraries/kicad-footprints/-/blob/51f8a59ed7d5f3bab9bb296c7b767bb033f3ba4c/LED_SMD.pretty/LED_SK6812MINI-E_3.2x2.8mm_P1.5mm_ReverseMount.kicad_mod) at `51f8a59e`, which KiCad 9 does not ship | CC-BY-SA 4.0 with the KiCad libraries' exception for designs that use it | Pads renumbered to OPSCO's datasheet (KiCad 1, 2, 3, 4 are datasheet 3, 4, 1, 2); saved in KiCad 8 format so KiCad 8 and 9 read it; pin-1 triangle and 3D model removed |
| `XMOS_QFN-60_7x7mm_P0.4mm_VDD-Bars` | SnapEDA, `IC_XU316-1024-QF60A-C32`, downloaded 2026-10-10 | SnapEDA's terms, which the download does not carry | Renamed. SnapEDA labels it QF60A; its symbol's 65 pins and the footprint's 7 × 7 mm, 0.4 mm pitch, four VDD bars and centre ground match the QF60B pad table pin for pin, and its -I32 twin is identical |
| `Texas_RYA0030A_VQFN-HR-30` | Ultra Librarian for TI, `VQFN-HR_RYA_TEX` (KiCad 6 export), downloaded 2026-10-10 | Ultra Librarian's terms, which the download does not carry | Renamed; its symbol's 30 pins match the pad table |
| `CUI_CMM-4030DT` | SnapEDA, `MIC_CMM-4030DT-261280-TR`, downloaded 2026-10-10 | SnapEDA's terms, which the download does not carry | Renamed; its symbol (1 VDD, 2 L/R, 3 CLOCK, 4 DATA, 5–8 GND) matches the pad table |
| `Silvertel_Ag9912-MTB` | SnapEDA, `CONV_AG9912-MTB`, downloaded 2026-10-10 | SnapEDA's terms, which the download does not carry | Renamed; its symbol (1 +VDC, 2 −VDC, 3 ADJ, 4 VIN+, 5 VIN−) matches the pad table |

The vendors' STEP models are not committed; download them beside the
footprints to see the board in 3D.

Pad positions on the SK6812MINI-E are the mirror of the datasheet's top-view
drawing, because that drawing shows the lens side and a reverse-mount part's
pads are seen from the other. The chamfered corner is the body's, at GND. The
ring sits on the top face, so its LEDs go on B.Cu, shining up through each
cutout.

A vendored footprint keeps its own licence; say where it came from, at which
commit, and what changed, here.
