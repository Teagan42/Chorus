"""Read the KiCad netlist `task gen:hardware` writes.

Only what a board needs: each component's footprint, fields and sheet, and
which pad each net reaches. Stdlib only, so it reads the same inside the
KiCad image (Python 3.11) as on a workstation.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path


def parse_sexpr(text: str) -> list:
    """Parse one s-expression into nested lists of strings.

    Quoted and bare atoms both come back as str; KiCad's netlist quotes
    everything that is data, so nothing here needs to tell them apart.
    """
    stack: list[list] = [[]]
    i, n = 0, len(text)
    while i < n:
        c = text[i]
        if c == "(":
            stack.append([])
            i += 1
        elif c == ")":
            if len(stack) == 1:
                raise ValueError(f"unbalanced ')' at offset {i}")
            done = stack.pop()
            stack[-1].append(done)
            i += 1
        elif c == '"':
            i += 1
            out = []
            while i < n and text[i] != '"':
                if text[i] == "\\" and i + 1 < n:
                    i += 1
                out.append(text[i])
                i += 1
            if i >= n:
                raise ValueError("unterminated string")
            stack[-1].append("".join(out))
            i += 1
        elif c.isspace():
            i += 1
        else:
            j = i
            while j < n and not text[j].isspace() and text[j] not in '()"':
                j += 1
            stack[-1].append(text[i:j])
            i = j
    if len(stack) != 1 or len(stack[0]) != 1:
        raise ValueError("expected exactly one top-level expression")
    return stack[0][0]


def _child(node: list, key: str) -> list | None:
    for item in node[1:]:
        if isinstance(item, list) and item and item[0] == key:
            return item
    return None


def _children(node: list, key: str) -> list[list]:
    return [item for item in node[1:] if isinstance(item, list) and item and item[0] == key]


def _value(node: list, key: str, default: str = "") -> str:
    c = _child(node, key)
    return c[1] if c is not None and len(c) > 1 else default


@dataclass
class Component:
    ref: str
    value: str
    footprint: str  # "library:name"
    datasheet: str
    fields: dict[str, str]
    sheet: str  # sheet name, e.g. "mics"
    sheet_tstamps: str  # "/<sheet uuid>/"
    tstamp: str  # the component's own uuid

    @property
    def library(self) -> str:
        return self.footprint.split(":", 1)[0]

    @property
    def footprint_name(self) -> str:
        return self.footprint.split(":", 1)[1]

    @property
    def path(self) -> str:
        """The KIID path Pcbnew matches on when it updates from a netlist."""
        return self.sheet_tstamps + self.tstamp

    @property
    def dnp(self) -> bool:
        return "DNP" in self.fields

    @property
    def back(self) -> bool:
        """Mounted on B.Cu, as parts.yaml's side: back says."""
        return self.fields.get("Side") == "back"


@dataclass
class Sheet:
    name: str  # "/mics/"
    title: str


@dataclass
class Netlist:
    title: str
    sheets: list[Sheet]
    components: list[Component]
    # net name -> [(ref, pin)], in the order the netlist lists them
    nets: dict[str, list[tuple[str, str]]] = field(default_factory=dict)

    def pad_nets(self) -> dict[tuple[str, str], str]:
        """(ref, pin) -> net name. A pad on two nets is a netlist error."""
        out: dict[tuple[str, str], str] = {}
        for name, nodes in self.nets.items():
            for node in nodes:
                if node in out:
                    raise ValueError(f"{node[0]} pad {node[1]} is on {out[node]} and {name}")
                out[node] = name
        return out


def load(path: str | Path) -> Netlist:
    tree = parse_sexpr(Path(path).read_text(encoding="utf-8"))
    if not tree or tree[0] != "export":
        raise ValueError(f"{path}: not a KiCad netlist export")
    design = _child(tree, "design")
    sheets = []
    title = ""
    for s in _children(design, "sheet") if design else []:
        tb = _child(s, "title_block")
        t = _value(tb, "title") if tb else ""
        if _value(s, "name") == "/":
            title = t
        else:
            sheets.append(Sheet(name=_value(s, "name"), title=t))

    comps = []
    for c in _children(_child(tree, "components") or ["components"], "comp"):
        fields = {}
        fl = _child(c, "fields")
        for f in _children(fl, "field") if fl else []:
            fields[_value(f, "name")] = f[2] if len(f) > 2 else ""
        sp = _child(c, "sheetpath")
        prop = {_value(p, "name"): _value(p, "value") for p in _children(c, "property")}
        comps.append(
            Component(
                ref=_value(c, "ref"),
                value=_value(c, "value"),
                footprint=_value(c, "footprint"),
                datasheet=_value(c, "datasheet"),
                fields=fields,
                sheet=prop.get("Sheetname", ""),
                sheet_tstamps=_value(sp, "tstamps", "/") if sp else "/",
                tstamp=_value(c, "tstamps"),
            )
        )

    nets: dict[str, list[tuple[str, str]]] = {}
    for n in _children(_child(tree, "nets") or ["nets"], "net"):
        nets[_value(n, "name")] = [
            (_value(node, "ref"), _value(node, "pin")) for node in _children(n, "node")
        ]
    return Netlist(title=title, sheets=sheets, components=comps, nets=nets)
