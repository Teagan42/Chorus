"""The board builder, end to end, inside the KiCad image.

Skipped where `pcbnew` cannot be imported; CI runs it in kicad/kicad:9.0.5
(.github/workflows/hardware.yml), as `task hardware:kicad` does locally.
"""

from __future__ import annotations

import shutil
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

try:
    import pcbnew
except ImportError:  # outside the KiCad image
    pcbnew = None

REPO = Path(__file__).resolve().parents[3]
CHORUS_MAIN = REPO / "hardware" / "chorus-main"


@unittest.skipIf(pcbnew is None, "needs KiCad's pcbnew; run in the KiCad image")
class BuildChorusMainTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        import build

        cls.build = build
        cls.tmp = Path(tempfile.mkdtemp())
        cls.zip = build.build(CHORUS_MAIN, cls.tmp)
        cls.board = pcbnew.LoadBoard(str(cls.tmp / "chorus-main" / "chorus-main.kicad_pcb"))
        cls.fps = {fp.GetReference(): fp for fp in cls.board.GetFootprints()}

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp)

    def test_the_zip_holds_a_project_kicad_and_easyeda_can_open(self):
        names = set(zipfile.ZipFile(self.zip).namelist())
        for want in (
            "chorus-main.kicad_pro",
            "chorus-main.kicad_pcb",
            "fp-lib-table",
            "chorus-main.pretty/Hirose_FX23L-80S-0.5SV.kicad_mod",
            "bom.csv",
            "chorus-main.net",
        ):
            self.assertIn(want, names)

    def test_the_receptacle_gets_its_footprint_with_nothing_downloaded(self):
        j1 = self.fps["J1"]
        self.assertEqual(j1.GetFPIDAsString(), "chorus-main:Hirose_FX23L-80S-0.5SV")
        self.assertEqual(len({p.GetNumber() for p in j1.Pads()}), 86)

    def test_the_receptacle_sits_where_the_hats_plug_comes_down(self):
        j1 = self.fps["J1"]
        cx, cy = self.build.OUTLINE_CENTRE
        self.assertEqual((pcbnew.ToMM(j1.GetPosition().x) - cx, pcbnew.ToMM(j1.GetPosition().y) - cy), (0.0, -10.0))
        self.assertTrue(j1.IsLocked())
        self.assertFalse(j1.IsFlipped())

    def test_the_kit_carries_vbus_and_ground_to_the_receptacle(self):
        nets = {p.GetNumber(): p.GetNetname() for p in self.fps["J1"].Pads()}
        self.assertEqual((nets["MH1"], nets["MH2"], nets["MH3"], nets["MH4"]), ("GND", "5V0", "GND", "VBUS"))
        self.assertEqual(nets["37"], "ETH_CS_N")
        self.assertEqual(nets["24"], "", "the XMOS select stays the HAT's")

    def test_the_outline_is_the_kits_with_its_four_mounting_holes(self):
        edges = [d for d in self.board.GetDrawings() if d.GetLayer() == pcbnew.Edge_Cuts]
        holes = [d for d in edges if d.GetShape() == pcbnew.SHAPE_T_CIRCLE]
        self.assertEqual(len(holes), 4)
        for h in holes:
            self.assertAlmostEqual(pcbnew.ToMM(h.GetRadius()), 1.6, places=3)
        box = self.board.GetBoardEdgesBoundingBox()
        # The box includes the 0.1 mm line on Edge.Cuts.
        self.assertAlmostEqual(pcbnew.ToMM(box.GetWidth()), 88.1, places=3)
        self.assertAlmostEqual(pcbnew.ToMM(box.GetHeight()), 86.1, places=3)

    def test_the_tall_parts_are_on_the_back_below_the_core(self):
        for ref in ("J60", "U80", "C500", "J2"):
            self.assertTrue(self.fps[ref].IsFlipped(), ref)
        for ref in ("U60", "U81", "J1"):
            self.assertFalse(self.fps[ref].IsFlipped(), ref)

    def test_the_lcsc_number_and_the_unverified_note_ride_along_unprinted(self):
        u60 = self.fps["U60"]
        self.assertEqual(u60.GetFieldText("LCSC"), "C32843")
        self.assertFalse(u60.GetFieldByName("LCSC").IsVisible())
        self.assertIn("MH1-MH4", self.fps["J1"].GetFieldText("Unverified"))

    def test_the_project_carries_four_layers_and_fab_rules(self):
        self.assertEqual(self.board.GetCopperLayerCount(), 4)
        ds = self.board.GetDesignSettings()
        self.assertEqual(pcbnew.ToMM(ds.m_MinClearance), 0.1)
        self.assertEqual(pcbnew.ToMM(ds.m_MinThroughDrill), 0.2)

    def test_a_netlist_naming_a_pad_the_footprint_lacks_fails_the_build(self):
        board = self.tmp / "broken" / "chorus-main"
        shutil.copytree(CHORUS_MAIN, board)
        net = board / "chorus-main.net"
        text = net.read_text(encoding="utf-8")
        node = '(node (ref "J1") (pin "MH4")'
        self.assertIn(node, text)
        net.write_text(text.replace(node, '(node (ref "J1") (pin "MH5")', 1), encoding="utf-8")
        with self.assertRaises(SystemExit) as caught:
            self.build.build(board, self.tmp / "broken-out")
        self.assertIn("J1 has no pad MH5, which the netlist puts on VBUS", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
