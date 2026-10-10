"""Build a KiCad 9 project from a board's generated netlist, ready to open or import.

Runs inside the KiCad image, where `pcbnew` is importable:

    python3 internal/tools/kicadboard/build.py hardware/chorus-main dist/hardware

It does what Pcbnew's File > Import > Netlist does, and then checks it did:

- every component gets its footprint, from the board's own library or KiCad's;
- every pad the netlist names gets its net, and no other pad gets one;
- parts are grouped by sheet, off to the side, for a person to place;
- a board under a bought kit gets the kit's outline and mounting holes, and
  its mate placed and locked where the kit's plug comes down.

It writes `<board>.kicad_pro`, `<board>.kicad_pcb`, the board's footprint
library and `fp-lib-table`, `bom.csv` and the netlist into one folder. It
then zips that folder's files the way KiCad's own *Archive Project* does,
which is what EasyEDA Pro's KiCad import asks for. The rest of placement and
all of routing remain a person's work: there is no track here.
"""

from __future__ import annotations

import argparse
import math
import os
import re
import shutil
import sys
import zipfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import netlist as nl  # noqa: E402
import pcbnew  # noqa: E402  # only inside the KiCad image
import yaml  # noqa: E402  # PyYAML ships in the KiCad image

STOCK_DIRS = [
    os.environ.get("KICAD9_FOOTPRINT_DIR", ""),
    "/usr/share/kicad/footprints",
]

# Four layers, as the doc orders it from JLCPCB (docs/hardware/README.md).
COPPER_LAYERS = 4

MM = pcbnew.FromMM

# Where the parts pile starts, and how wide a sheet's group may grow before
# it wraps, in mm. Off any plausible outline, on the page.
ORIGIN = (20.0, 20.0)
GROUP_WIDTH = 60.0
GAP = 1.5
SHEETS_PER_ROW = 3

# Where a kit's board outline is centred, in mm; the pile then starts to its
# right, so the two never overlap.
OUTLINE_CENTRE = (70.0, 70.0)


def board_name(board_dir: Path) -> str:
    return board_dir.resolve().name


def library_paths(board_dir: Path) -> dict[str, Path]:
    """The board's own libraries, from its fp-lib-table."""
    table = board_dir / "fp-lib-table"
    libs: dict[str, Path] = {}
    if not table.exists():
        return libs
    text = table.read_text(encoding="utf-8")
    for name, uri in re.findall(r'\(lib \(name "([^"]+)"\)\(type "[^"]+"\)\(uri "([^"]+)"\)', text):
        libs[name] = Path(uri.replace("${KIPRJMOD}", str(board_dir.resolve())))
    return libs


def stock_library(lib: str) -> Path:
    for d in STOCK_DIRS:
        if d and (Path(d) / f"{lib}.pretty").is_dir():
            return Path(d) / f"{lib}.pretty"
    raise FileNotFoundError(f"KiCad's footprint library {lib} is not installed")


def load_footprint(comp: nl.Component, own: dict[str, Path]) -> pcbnew.FOOTPRINT:
    lib = own.get(comp.library) or stock_library(comp.library)
    io = pcbnew.PCB_IO_MGR.PluginFind(pcbnew.PCB_IO_MGR.KICAD_SEXP)
    fp = io.FootprintLoad(str(lib), comp.footprint_name)
    if fp is None:
        raise FileNotFoundError(f"{comp.ref}: {comp.footprint} is not in {lib}")
    fp.SetFPID(pcbnew.LIB_ID(comp.library, comp.footprint_name))
    return fp


def kit_mechanical(board_dir: Path) -> dict | None:
    """The kit's mechanical fit, when pins.yaml names a kit; else None."""
    pins = yaml.safe_load((board_dir / "pins.yaml").read_text(encoding="utf-8"))
    kit = pins.get("expansion")
    if not kit:
        return None
    return yaml.safe_load((board_dir / kit).read_text(encoding="utf-8")).get("mechanical")


def build(board_dir: Path, out_dir: Path) -> Path:
    name = board_name(board_dir)
    net = nl.load(board_dir / f"{name}.net")
    own = library_paths(board_dir)
    pad_nets = net.pad_nets()
    mech = kit_mechanical(board_dir)

    project = out_dir / name
    if project.exists():
        shutil.rmtree(project)
    project.mkdir(parents=True)

    board = pcbnew.NewBoard(str(project / f"{name}.kicad_pcb"))
    board.SetCopperLayerCount(COPPER_LAYERS)
    fab_rules(board)
    tb = board.GetTitleBlock()
    tb.SetTitle(net.title or name)
    tb.SetRevision("A")
    tb.SetComment(0, f"generated from hardware/{name}; edit the YAML, not this")

    nets: dict[str, pcbnew.NETINFO_ITEM] = {}
    for n in net.nets:
        item = pcbnew.NETINFO_ITEM(board, n)
        board.Add(item)
        nets[n] = item

    titles = {s.name.strip("/"): s.title for s in net.sheets}
    groups: dict[str, list[pcbnew.FOOTPRINT]] = {}
    for comp in sorted(net.components, key=lambda c: (c.sheet, ref_key(c.ref))):
        fp = load_footprint(comp, own)
        fp.SetReference(comp.ref)
        fp.SetValue(comp.value)
        fp.SetPath(pcbnew.KIID_PATH(comp.path))
        fp.SetSheetname(comp.sheet)
        fp.SetSheetfile(f"sheets/{comp.sheet}.yaml")
        if comp.datasheet:
            fp.SetField("Datasheet", comp.datasheet)
        for k, v in comp.fields.items():
            if k not in ("DNP", "Side"):
                fp.SetField(k, v)
                # Carried for the BOM and EasyEDA's LCSC match, not printed.
                fp.GetFieldByName(k).SetVisible(False)
        if comp.dnp:
            fp.SetDNP(True)
            fp.SetExcludedFromBOM(True)
            fp.SetExcludedFromPosFiles(True)
        for pad in fp.Pads():
            n = pad_nets.get((comp.ref, pad.GetNumber()))
            if n is not None:
                pad.SetNet(nets[n])
        board.Add(fp)
        if comp.back:
            # Flipped once it is on the board: the flip maps layers through
            # the board's stackup, and a footprint with no board crashes it.
            fp.Flip(fp.GetPosition(), pcbnew.FLIP_DIRECTION_LEFT_RIGHT)
        groups.setdefault(comp.sheet, []).append(fp)

    origin = ORIGIN
    if mech:
        outline(board, mech)
        mate = [fp for fps in groups.values() for fp in fps if fp.HasField("Mates")]
        if len(mate) != 1:
            raise SystemExit(f"{name}: want one footprint marked Mates, found {len(mate)}")
        groups = {k: [fp for fp in v if fp not in mate] for k, v in groups.items()}
        place_mate(mate[0], mech)
        radius = mech["outline"]["diameter"] / 2
        origin = (OUTLINE_CENTRE[0] + radius + 20.0, ORIGIN[1])
    place(board, groups, titles, origin)
    board.BuildConnectivity()

    pcb_path = project / f"{name}.kicad_pcb"
    pcbnew.SaveBoard(str(pcb_path), board)

    for path in own.values():
        shutil.copytree(path, project / path.name)
    shutil.copy(board_dir / "fp-lib-table", project / "fp-lib-table")
    for extra in (f"{name}.net", "bom.csv"):
        if (board_dir / extra).exists():
            shutil.copy(board_dir / extra, project / extra)

    problems = verify(pcb_path, net, own, mech)
    if problems:
        raise SystemExit(f"{pcb_path}:\n  " + "\n  ".join(problems))

    return archive(project, out_dir / f"{name}-kicad.zip")


def ref_key(ref: str) -> tuple[str, int]:
    m = re.match(r"([A-Za-z_]+)(\d*)", ref)
    if not m:
        return (ref, 0)
    return (m.group(1), int(m.group(2) or 0))


def at(x: float, y: float) -> pcbnew.VECTOR2I:
    """A point given from the kit's outline centre, in mm."""
    return pcbnew.VECTOR2I(MM(OUTLINE_CENTRE[0] + x), MM(OUTLINE_CENTRE[1] + y))


def outline(board, mech: dict) -> None:
    """Draw the kit's outline and mounting holes on Edge.Cuts.

    The outline is a circle, cut straight across at flat_y when the kit has
    a flat; a hole is a circle inside it, which KiCad cuts out.
    """
    r = mech["outline"]["diameter"] / 2
    flat = mech["outline"].get("flat_y") or 0.0

    def edge(shape):
        shape.SetLayer(pcbnew.Edge_Cuts)
        shape.SetWidth(MM(0.1))
        board.Add(shape)

    if not flat:
        c = pcbnew.PCB_SHAPE(board, pcbnew.SHAPE_T_CIRCLE)
        c.SetCenter(at(0, 0))
        c.SetEnd(at(r, 0))
        edge(c)
    else:
        half = math.sqrt(r * r - flat * flat)
        line = pcbnew.PCB_SHAPE(board, pcbnew.SHAPE_T_SEGMENT)
        line.SetStart(at(-half, flat))
        line.SetEnd(at(half, flat))
        edge(line)
        arc = pcbnew.PCB_SHAPE(board, pcbnew.SHAPE_T_ARC)
        # The long way round, from one end of the flat to the other.
        mid = (0.0, r if flat < 0 else -r)
        arc.SetArcGeometry(at(half, flat), at(*mid), at(-half, flat))
        edge(arc)
    for h in mech.get("holes", []):
        c = pcbnew.PCB_SHAPE(board, pcbnew.SHAPE_T_CIRCLE)
        c.SetCenter(at(h["x"], h["y"]))
        c.SetEnd(at(h["x"] + h["diameter"] / 2, h["y"]))
        edge(c)


def place_mate(fp, mech: dict) -> None:
    """Put the mate where the kit's plug comes down, and lock it there."""
    m = mech["mate"]
    fp.SetPosition(at(m["x"], m["y"]))
    fp.SetOrientationDegrees(m.get("rotation", 0))
    fp.SetLocked(True)


def place(board, groups: dict[str, list], titles: dict[str, str], origin=ORIGIN) -> None:
    """Pile each sheet's parts in its own block, three blocks to a row.

    A block wraps at GROUP_WIDTH and is labelled with its sheet's title on
    a user layer, so the pile says which part of the circuit it is.
    """
    x0, y0 = origin
    row_bottom = y0
    for i, sheet in enumerate(sorted(g for g in groups if groups[g])):
        if i and i % SHEETS_PER_ROW == 0:
            x0, y0 = origin[0], row_bottom + 10.0
        x, y, row_h, right, bottom = x0, y0 + 6.0, 0.0, x0, y0
        text = f"{sheet}: {titles.get(sheet, '')}"
        label = pcbnew.PCB_TEXT(board)
        label.SetText(text)
        label.SetLayer(pcbnew.Cmts_User)
        label.SetPosition(pcbnew.VECTOR2I(MM(x0), MM(y0)))
        label.SetHorizJustify(pcbnew.GR_TEXT_H_ALIGN_LEFT)
        board.Add(label)
        for fp in groups[sheet]:
            box = fp.GetBoundingBox(False)
            w = pcbnew.ToMM(box.GetWidth())
            h = pcbnew.ToMM(box.GetHeight())
            if x > x0 and x + w > x0 + GROUP_WIDTH:
                x, y, row_h = x0, y + row_h + GAP, 0.0
            # Move so the box's top-left lands at (x, y).
            fp.Move(pcbnew.VECTOR2I(MM(x) - box.GetX(), MM(y) - box.GetY()))
            x += w + GAP
            row_h = max(row_h, h)
            right = max(right, x)
            bottom = max(bottom, y + h)
        row_bottom = max(row_bottom, bottom)
        # The label is 1 mm text, a little under 1 mm a character.
        x0 = max(right, x0 + GROUP_WIDTH, x0 + 0.9 * len(text)) + 10.0


def fab_rules(board) -> None:
    """Design rules a JLCPCB four-layer board passes, not KiCad's defaults.

    KiCad's defaults flag parts that are fine to fab: the XU316's and the SPI
    muxes' 0.4 mm-pitch pads sit 0.185 mm apart, the ESP32 module's thermal
    vias are 0.2 mm, and the reverse-mount LEDs' windows put copper 0.35 mm
    from an edge. These values stay inside JLCPCB's standard multilayer
    process and still catch a real mistake. They live in the .kicad_pro,
    which is why KiCad writes that file rather than this script.
    """
    ds = board.GetDesignSettings()
    ds.m_MinClearance = MM(0.1)
    ds.m_MinThroughDrill = MM(0.2)
    ds.m_CopperEdgeClearance = MM(0.3)
    ds.m_TrackMinWidth = MM(0.1)
    ds.m_ViasMinSize = MM(0.4)
    default = ds.m_NetSettings.GetDefaultNetclass()
    default.SetClearance(MM(0.127))
    default.SetTrackWidth(MM(0.2))
    default.SetViaDiameter(MM(0.45))
    default.SetViaDrill(MM(0.2))


def verify(pcb_path: Path, net: nl.Netlist, own: dict[str, Path], mech: dict | None = None) -> list[str]:
    """Reload the saved board and hold it to the netlist, pad by pad, and
    the mate to where the kit's plug comes down."""
    board = pcbnew.LoadBoard(str(pcb_path))
    problems: list[str] = []
    if mech:
        want_at = at(mech["mate"]["x"], mech["mate"]["y"])
        for fp in board.GetFootprints():
            if fp.HasField("Mates") and (fp.GetPosition() != want_at or not fp.IsLocked()):
                problems.append(f"{fp.GetReference()} is not locked where the kit's plug comes down")
    want = net.pad_nets()
    by_ref = {fp.GetReference(): fp for fp in board.GetFootprints()}
    comps = {c.ref: c for c in net.components}

    for ref in sorted(set(comps) - set(by_ref)):
        problems.append(f"{ref} is in the netlist and not on the board")
    for ref in sorted(set(by_ref) - set(comps)):
        problems.append(f"{ref} is on the board and not in the netlist")

    for ref, fp in sorted(by_ref.items()):
        comp = comps.get(ref)
        if comp is None:
            continue
        if fp.GetFPIDAsString() != comp.footprint:
            problems.append(f"{ref} has {fp.GetFPIDAsString()}, want {comp.footprint}")
        if fp.IsFlipped() != comp.back:
            side = "B.Cu" if comp.back else "F.Cu"
            problems.append(f"{ref} is on the wrong side; parts.yaml puts it on {side}")
        if str(fp.GetPath().AsString()) != comp.path:
            problems.append(f"{ref} has path {fp.GetPath().AsString()}, want {comp.path}")
        numbers = set()
        for pad in fp.Pads():
            num = pad.GetNumber()
            if not num:
                continue
            numbers.add(num)
            got = pad.GetNetname()
            expect = want.get((ref, num), "")
            if got != expect:
                problems.append(
                    f"{ref} pad {num} is on {got or 'no net'}, want {expect or 'no net'}"
                )
        for (r, pin), n in want.items():
            if r == ref and pin not in numbers:
                problems.append(f"{ref} has no pad {pin}, which the netlist puts on {n}")
    return problems


def archive(project: Path, zip_path: Path) -> Path:
    """Zip the project's files at the archive's root, as KiCad's Archive Project does."""
    if zip_path.exists():
        zip_path.unlink()
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as z:
        for f in sorted(project.rglob("*")):
            if f.is_file():
                z.write(f, f.relative_to(project).as_posix())
    return zip_path


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("board", type=Path, help="board folder, e.g. hardware/chorus-main")
    p.add_argument("out", type=Path, help="where to write the project and its zip")
    a = p.parse_args(argv)
    a.out.mkdir(parents=True, exist_ok=True)
    zip_path = build(a.board, a.out)
    print(f"wrote {zip_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
