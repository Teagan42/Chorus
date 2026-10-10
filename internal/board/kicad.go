package board

import (
	"bytes"
	"crypto/sha1"
	"encoding/csv"
	"fmt"
	"slices"
	"strings"
)

// KiCadNetlist renders the schematic as a KiCad netlist (export version "E",
// what Eeschema 7 and 8 write), which Pcbnew's File > Import > Netlist reads
// to place every footprint already wired. It is generated, so it carries no
// date, and every timestamp is a UUID derived from the board and reference:
// the same sheets always produce the same bytes, and gen:check can hold it.
func (s Schematic) KiCadNetlist() []byte {
	nets, _ := s.Nets()
	refs := s.refs(new([]error))
	board := s.Pins.Board

	var b bytes.Buffer
	b.WriteString("(export (version \"E\")\n")
	fmt.Fprintf(&b, "  (design\n    (source %s)\n    (tool \"chorus internal/tools/kicadnet\")\n", q("hardware/"+board))
	fmt.Fprintf(&b, "    (sheet (number \"1\") (name \"/\") (tstamps \"/\")\n      %s)", s.titleBlock(board, "pins.yaml"))
	for i, sh := range s.Sheets {
		fmt.Fprintf(&b, "\n    (sheet (number %s) (name %s) (tstamps %s)\n      %s)",
			q(fmt.Sprint(i+2)), q("/"+sh.Sheet+"/"), q("/"+s.uuid("sheet/"+sh.Sheet)+"/"), s.titleBlock(sh.Title, "sheets/"+sh.Sheet+".yaml"))
	}
	b.WriteString(")\n")

	b.WriteString("  (components")
	order := sortedKeys(refs)
	slices.SortFunc(order, func(a, c string) int { return strings.Compare(natural(a), natural(c)) })
	for _, ref := range order {
		inst := refs[ref]
		part := s.Parts[inst.Part]
		lib, sym, _ := strings.Cut(part.Symbol, ":")
		fmt.Fprintf(&b, "\n    (comp (ref %s)\n      (value %s)\n      (footprint %s)\n", q(ref), q(s.valueOf(inst)), q(part.Footprint))
		if part.Datasheet != "" {
			fmt.Fprintf(&b, "      (datasheet %s)\n", q(part.Datasheet))
		}
		var fields [][2]string
		if part.MPN != "" {
			fields = append(fields, [2]string{"MPN", part.MPN})
		}
		if inst.Value != "" {
			fields = append(fields, [2]string{"Part", inst.Part})
		}
		if n := s.lcsc(inst.Instance); n != "" {
			fields = append(fields, [2]string{"LCSC", n})
		}
		if inst.DNP {
			fields = append(fields, [2]string{"DNP", "DNP"})
		}
		if part.Side != "" {
			fields = append(fields, [2]string{"Side", part.Side})
		}
		if inst.Note != "" {
			fields = append(fields, [2]string{"Note", inst.Note})
		}
		if s.Kit != nil && inst.Part == s.Kit.Mate {
			fields = append(fields, [2]string{"Mates", s.Kit.Connector + " of " + s.Kit.Kit})
		}
		if part.Unverified != "" {
			fields = append(fields, [2]string{"Unverified", strings.TrimSpace(part.Unverified)})
		}
		if len(fields) > 0 {
			b.WriteString("      (fields")
			for _, f := range fields {
				fmt.Fprintf(&b, "\n        (field (name %s) %s)", q(f[0]), q(f[1]))
			}
			b.WriteString(")\n")
		}
		fmt.Fprintf(&b, "      (libsource (lib %s) (part %s) (description \"\"))\n", q(lib), q(sym))
		fmt.Fprintf(&b, "      (property (name \"Sheetname\") (value %s))\n", q(inst.sheet))
		fmt.Fprintf(&b, "      (sheetpath (names %s) (tstamps %s))\n", q("/"+inst.sheet+"/"), q("/"+s.uuid("sheet/"+inst.sheet)+"/"))
		fmt.Fprintf(&b, "      (tstamps %s))", q(s.uuid("ref/"+ref)))
	}
	b.WriteString(")\n  (nets")
	for i, name := range sortedKeys(nets) {
		fmt.Fprintf(&b, "\n    (net (code %s) (name %s)", q(fmt.Sprint(i+1)), q(name))
		for _, n := range nets[name] {
			fmt.Fprintf(&b, "\n      (node (ref %s) (pin %s)", q(n.Ref), q(n.Pad))
			if n.Name != n.Pad {
				fmt.Fprintf(&b, " (pinfunction %s)", q(n.Name))
			}
			fmt.Fprintf(&b, " (pintype %s))", q(n.Type))
		}
		b.WriteString(")")
	}
	b.WriteString("))\n")
	return b.Bytes()
}

// BOM renders the parts list in the columns JLCPCB's assembly upload reads:
// one row per part and value, its references in natural order. Unfitted
// parts are left off; hand-soldered ones stay on, with no number.
func (s Schematic) BOM() []byte {
	refs := s.refs(new([]error))
	type line struct {
		comment, footprint, lcsc, mpn string
		refs                          []string
	}
	lines := map[string]*line{}
	for ref, inst := range refs {
		if inst.DNP {
			continue
		}
		part := s.Parts[inst.Part]
		key := inst.Part + "\x00" + inst.Value
		l, ok := lines[key]
		if !ok {
			l = &line{comment: s.valueOf(inst), footprint: part.Footprint, lcsc: s.lcsc(inst.Instance), mpn: part.MPN}
			lines[key] = l
		}
		l.refs = append(l.refs, ref)
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"Comment", "Designator", "Footprint", "LCSC Part #", "Manufacturer Part"})
	for _, key := range sortedKeys(lines) {
		l := lines[key]
		slices.SortFunc(l.refs, func(a, c string) int { return strings.Compare(natural(a), natural(c)) })
		_ = w.Write([]string{l.comment, strings.Join(l.refs, ","), l.footprint, l.lcsc, l.mpn})
	}
	w.Flush()
	return b.Bytes()
}

// titleBlock is written in full, every field KiCad writes, because readers
// of the format (kinparse among them) require the lot.
func (s Schematic) titleBlock(title, source string) string {
	return fmt.Sprintf("(title_block (title %s) (company \"\") (rev %s) (date \"\") (source %s)\n        (comment (number \"1\") (value %s)))",
		q(title), q(s.Pins.Revision), q(source), q("generated from hardware/"+s.Pins.Board+" by task gen:hardware; edit the YAML, not this"))
}

// valueOf is what a part is called on the netlist and the BOM: its value,
// or the maker's part number, or the library's name for it.
func (s Schematic) valueOf(inst placed) string {
	if inst.Value != "" {
		return inst.Value
	}
	if mpn := s.Parts[inst.Part].MPN; mpn != "" {
		return mpn
	}
	return inst.Part
}

// uuid is a name-based (version 5 style) UUID under the board's name, so a
// reference keeps its timestamp across regenerations and Pcbnew keeps the
// footprint it already placed for it.
func (s Schematic) uuid(name string) string {
	h := sha1.Sum([]byte("chorus/" + s.Pins.Board + "/" + name))
	h[6] = h[6]&0x0f | 0x50
	h[8] = h[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// q quotes a string as KiCad's s-expression reader expects.
func q(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}
