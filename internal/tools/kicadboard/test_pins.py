"""The KiCad image a person builds with is the one CI publishes from.

Taskfile.yml's KICAD_IMAGE and the Hardware workflow's container are two
copies of one pin; this holds them equal, so `task hardware:kicad` cannot
drift from the chorus-main-kicad artifact.
"""

from __future__ import annotations

import re
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]


def pin(path: Path, pattern: str) -> str:
    found = re.findall(pattern, path.read_text(encoding="utf-8"), re.MULTILINE)
    if len(found) != 1:
        raise AssertionError(f"{path.name}: want one KiCad image pin, found {found}")
    return found[0]


class KiCadImagePinTest(unittest.TestCase):
    def test_the_task_and_the_workflow_build_in_the_same_image(self):
        task = pin(REPO / "Taskfile.yml", r"^\s*KICAD_IMAGE:\s*(\S+)\s*$")
        ci = pin(REPO / ".github/workflows/hardware.yml", r"^\s*image:\s*(\S+)\s*$")
        self.assertEqual(task, ci)
        self.assertRegex(task, r"^kicad/kicad:9\.0\.5@sha256:[0-9a-f]{64}$")


if __name__ == "__main__":
    unittest.main()
