# chorus-sat footprints

The footprints `parts.yaml` names as `chorus-sat:*`, for the parts KiCad's
libraries lack. `fp-lib-table` beside `parts.yaml` makes this the
`chorus-sat` library in any KiCad project opened from that folder.
`go test ./internal/board` holds each file's pads to its part's pad table,
and lists the ones not yet drawn.

| Footprint | Source | Licence | Changes |
|---|---|---|---|
| `LINK-PP_LPJG0926HENL` | [NabuCasa/yellow](https://github.com/NabuCasa/yellow/blob/c3d51b80e40ea01e34ef65c7d0aecc16a37980d4/Yellow.pretty/RJ45_LINK-PP_LPJG0926HENL_Horizontal.kicad_mod) at `c3d51b80`, the Home Assistant Yellow's magjack | CERN-OHL-W v2, [`cern_ohl_w_v2.txt`](cern_ohl_w_v2.txt); Copyright Nabu Casa 2023 | Renamed from `RJ45_LINK-PP_LPJG0926HENL_Horizontal`; the 3D model reference, to a file in the Yellow repository, removed |

A vendored footprint keeps its own licence; say where it came from, at which
commit, and what changed, here.
