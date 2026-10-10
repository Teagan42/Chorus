"""The netlist reader, against the board's own generated netlist.

Stdlib unittest, so it runs in the KiCad image as it is:

    python3 -m unittest discover -s internal/tools/kicadboard
"""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import netlist as nl  # noqa: E402

REPO = Path(__file__).resolve().parents[3]
CHORUS_SAT = REPO / "hardware" / "chorus-sat" / "chorus-sat.net"


class ParseSexprTest(unittest.TestCase):
    def test_a_note_with_quotes_and_backslashes_reads_back_whole(self):
        tree = nl.parse_sexpr(r'(field (name "Note") "the \"hallway\" jig, C:\\bench")')
        self.assertEqual(tree, ["field", ["name", "Note"], 'the "hallway" jig, C:\\bench'])

    def test_an_unbalanced_netlist_is_refused(self):
        with self.assertRaises(ValueError):
            nl.parse_sexpr('(export (version "E")')
        with self.assertRaises(ValueError):
            nl.parse_sexpr('(export (version "E")))')


class ChorusSatNetlistTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.net = nl.load(CHORUS_SAT)
        cls.by_ref = {c.ref: c for c in cls.net.components}

    def test_every_part_and_net_is_read(self):
        text = CHORUS_SAT.read_text(encoding="utf-8")
        self.assertEqual(len(self.net.components), text.count("(comp (ref "))
        self.assertEqual(len(self.net.nets), text.count("(net (code "))
        self.assertEqual(self.net.title, "chorus-sat")

    def test_the_xu316_keeps_its_footprint_sheet_and_path(self):
        u30 = self.by_ref["U30"]
        self.assertEqual(u30.footprint, "chorus-sat:XMOS_QFN-60_7x7mm_P0.4mm_VDD-Bars")
        self.assertEqual((u30.library, u30.sheet), ("chorus-sat", "voice_dsp"))
        self.assertEqual(u30.fields["LCSC"], "C7397517")
        # Pcbnew matches a footprint to its part by this path on every update.
        self.assertTrue(u30.path.startswith("/") and u30.path.endswith(u30.tstamp))
        self.assertEqual(u30.path.count("/"), 2)

    def test_the_ring_is_on_the_back_and_the_mics_on_the_front(self):
        for i in range(71, 83):
            self.assertTrue(self.by_ref[f"D{i}"].back, f"D{i}")
        for i in range(1, 9):
            self.assertFalse(self.by_ref[f"MK{i}"].back, f"MK{i}")

    def test_unfitted_test_points_are_marked(self):
        self.assertTrue(self.by_ref["TP70"].dnp)
        self.assertFalse(self.by_ref["U1"].dnp)

    def test_each_pad_is_on_one_net(self):
        pads = self.net.pad_nets()
        self.assertEqual(pads[("U30", "61")], "0V9")
        self.assertEqual(sum(len(n) for n in self.net.nets.values()), len(pads))

    def test_a_pad_on_two_nets_is_an_error(self):
        net = nl.Netlist(
            title="hallway",
            sheets=[],
            components=[],
            nets={"GND": [("R1", "2")], "MUTE_SENSE": [("R1", "2")]},
        )
        with self.assertRaisesRegex(ValueError, "R1 pad 2 is on GND and MUTE_SENSE"):
            net.pad_nets()


if __name__ == "__main__":
    unittest.main()
