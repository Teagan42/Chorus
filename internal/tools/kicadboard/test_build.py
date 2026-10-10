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
CHORUS_SAT = REPO / "hardware" / "chorus-sat"


@unittest.skipIf(pcbnew is None, "needs KiCad's pcbnew; run in the KiCad image")
class BuildChorusSatTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        import build

        cls.build = build
        cls.tmp = Path(tempfile.mkdtemp())
        cls.zip = build.build(CHORUS_SAT, cls.tmp)
        cls.board = pcbnew.LoadBoard(str(cls.tmp / "chorus-sat" / "chorus-sat.kicad_pcb"))
        cls.fps = {fp.GetReference(): fp for fp in cls.board.GetFootprints()}

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp)

    def test_the_zip_holds_a_project_kicad_and_easyeda_can_open(self):
        names = set(zipfile.ZipFile(self.zip).namelist())
        for want in (
            "chorus-sat.kicad_pro",
            "chorus-sat.kicad_pcb",
            "fp-lib-table",
            "chorus-sat.pretty/Texas_RYA0030A_VQFN-HR-30.kicad_mod",
            "bom.csv",
            "chorus-sat.net",
        ):
            self.assertIn(want, names)

    def test_the_tas2780_gets_its_footprint_with_nothing_downloaded(self):
        u50 = self.fps["U50"]
        self.assertEqual(u50.GetFPIDAsString(), "chorus-sat:Texas_RYA0030A_VQFN-HR-30")
        self.assertEqual(len({p.GetNumber() for p in u50.Pads()}), 30)

    def test_the_xu316_core_supply_reaches_its_vdd_bar(self):
        nets = {p.GetNumber(): p.GetNetname() for p in self.fps["U30"].Pads()}
        self.assertEqual(nets["61"], "0V9")

    def test_the_ring_is_on_the_back_and_the_module_on_the_front(self):
        for i in range(71, 83):
            self.assertTrue(self.fps[f"D{i}"].IsFlipped(), f"D{i}")
        self.assertFalse(self.fps["U1"].IsFlipped())

    def test_the_lcsc_number_rides_along_unprinted(self):
        u1 = self.fps["U1"]
        self.assertEqual(u1.GetFieldText("LCSC"), "C2913202")
        self.assertFalse(u1.GetFieldByName("LCSC").IsVisible())

    def test_unfitted_parts_are_off_the_bom_and_the_placement_file(self):
        tp = self.fps["TP70"]
        self.assertTrue(tp.IsDNP() and tp.IsExcludedFromBOM() and tp.IsExcludedFromPosFiles())

    def test_the_project_carries_four_layers_and_fab_rules(self):
        self.assertEqual(self.board.GetCopperLayerCount(), 4)
        ds = self.board.GetDesignSettings()
        self.assertEqual(pcbnew.ToMM(ds.m_MinClearance), 0.1)
        self.assertEqual(pcbnew.ToMM(ds.m_MinThroughDrill), 0.2)

    def test_a_netlist_naming_a_pad_the_footprint_lacks_fails_the_build(self):
        board = self.tmp / "broken" / "chorus-sat"
        shutil.copytree(CHORUS_SAT, board)
        net = board / "chorus-sat.net"
        text = net.read_text(encoding="utf-8")
        node = '(node (ref "U30") (pin "61")'
        self.assertIn(node, text)
        net.write_text(text.replace(node, '(node (ref "U30") (pin "99")', 1), encoding="utf-8")
        with self.assertRaises(SystemExit) as caught:
            self.build.build(board, self.tmp / "broken-out")
        self.assertIn("U30 has no pad 99, which the netlist puts on 0V9", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
