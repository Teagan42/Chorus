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
CHORUS_MAIN = REPO / "hardware" / "chorus-main" / "chorus-main.net"


class ParseSexprTest(unittest.TestCase):
    def test_a_note_with_quotes_and_backslashes_reads_back_whole(self):
        tree = nl.parse_sexpr(r'(field (name "Note") "the \"hallway\" jig, C:\\bench")')
        self.assertEqual(tree, ["field", ["name", "Note"], 'the "hallway" jig, C:\\bench'])

    def test_an_unbalanced_netlist_is_refused(self):
        with self.assertRaises(ValueError):
            nl.parse_sexpr('(export (version "E")')
        with self.assertRaises(ValueError):
            nl.parse_sexpr('(export (version "E")))')


class ChorusMainNetlistTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.net = nl.load(CHORUS_MAIN)
        cls.by_ref = {c.ref: c for c in cls.net.components}

    def test_every_part_and_net_is_read(self):
        text = CHORUS_MAIN.read_text(encoding="utf-8")
        self.assertEqual(len(self.net.components), text.count("(comp (ref "))
        self.assertEqual(len(self.net.nets), text.count("(net (code "))
        self.assertEqual(self.net.title, "chorus-main")

    def test_the_receptacle_keeps_its_footprint_sheet_and_path(self):
        j1 = self.by_ref["J1"]
        self.assertEqual(j1.footprint, "chorus-main:Hirose_FX23L-80S-0.5SV")
        self.assertEqual((j1.library, j1.sheet), ("chorus-main", "connector"))
        self.assertTrue(j1.fields["Mates"].startswith("J7 of FutureProofHomes Satellite1"))
        # Pcbnew matches a footprint to its part by this path on every update.
        self.assertTrue(j1.path.startswith("/") and j1.path.endswith(j1.tstamp))
        self.assertEqual(j1.path.count("/"), 2)

    def test_the_magjack_is_on_the_back_and_the_w5500_on_the_front(self):
        self.assertTrue(self.by_ref["J60"].back)
        self.assertFalse(self.by_ref["U60"].back)

    def test_each_pad_is_on_one_net(self):
        pads = self.net.pad_nets()
        self.assertEqual(pads[("J1", "MH4")], "VBUS")
        self.assertEqual(pads[("D2", "1")], "VBUS")
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
