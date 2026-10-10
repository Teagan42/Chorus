package board

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sexp is a parsed s-expression: an atom, or a list of them.
type sexp struct {
	atom string
	list []*sexp
}

// parseSexp reads one s-expression the way KiCad's DSNLEXER does: quoted
// atoms with backslash escapes, bare atoms, nothing after the last paren.
func parseSexp(src string) (*sexp, error) {
	pos := 0
	var read func() (*sexp, error)
	skip := func() {
		for pos < len(src) && strings.ContainsRune(" \t\r\n", rune(src[pos])) {
			pos++
		}
	}
	read = func() (*sexp, error) {
		skip()
		if pos >= len(src) {
			return nil, fmt.Errorf("unexpected end at %d", pos)
		}
		switch src[pos] {
		case '(':
			pos++
			n := &sexp{list: []*sexp{}}
			for {
				skip()
				if pos >= len(src) {
					return nil, fmt.Errorf("unclosed list")
				}
				if src[pos] == ')' {
					pos++
					return n, nil
				}
				c, err := read()
				if err != nil {
					return nil, err
				}
				n.list = append(n.list, c)
			}
		case ')':
			return nil, fmt.Errorf("stray ) at %d", pos)
		case '"':
			pos++
			var b strings.Builder
			for pos < len(src) && src[pos] != '"' {
				if src[pos] == '\\' && pos+1 < len(src) {
					pos++
				}
				b.WriteByte(src[pos])
				pos++
			}
			if pos >= len(src) {
				return nil, fmt.Errorf("unclosed string")
			}
			pos++
			return &sexp{atom: b.String()}, nil
		default:
			start := pos
			for pos < len(src) && !strings.ContainsRune(" \t\r\n()", rune(src[pos])) {
				pos++
			}
			return &sexp{atom: src[start:pos]}, nil
		}
	}
	n, err := read()
	if err != nil {
		return nil, err
	}
	skip()
	if pos != len(src) {
		return nil, fmt.Errorf("trailing input at %d", pos)
	}
	return n, nil
}

func (n *sexp) head() string {
	if len(n.list) == 0 || n.list[0].list != nil {
		return ""
	}
	return n.list[0].atom
}

// all returns the children of n whose head is name.
func (n *sexp) all(name string) []*sexp {
	var out []*sexp
	for _, c := range n.list {
		if c.head() == name {
			out = append(out, c)
		}
	}
	return out
}

func (n *sexp) one(name string) *sexp {
	if c := n.all(name); len(c) > 0 {
		return c[0]
	}
	return &sexp{}
}

// value is the first atom after the head: "U1" in (ref "U1").
func (n *sexp) value() string {
	if len(n.list) < 2 {
		return ""
	}
	return n.list[1].atom
}

// netsOf reads a KiCad netlist back into net name -> "REF.PAD" list.
func netsOf(t *testing.T, netlist []byte) (map[string][]string, *sexp) {
	t.Helper()
	root, err := parseSexp(string(netlist))
	if err != nil {
		t.Fatalf("netlist does not parse: %v", err)
	}
	if root.head() != "export" || root.one("version").value() != "E" {
		t.Fatalf("not a version E export: %s", netlist[:min(len(netlist), 60)])
	}
	out := map[string][]string{}
	for _, net := range root.one("nets").all("net") {
		name := net.one("name").value()
		for _, node := range net.all("node") {
			out[name] = append(out[name], node.one("ref").value()+"."+node.one("pin").value())
		}
	}
	return out, root
}

func TestTheHallwayNetlistIsWhatPcbnewImports(t *testing.T) {
	s := loadSchematic(t, hallway(t))
	nets, root := netsOf(t, s.KiCadNetlist())

	if got := strings.Join(nets["LED_DATA"], " "); got != "U1.22 U2.2" {
		t.Errorf("LED_DATA = %s, want module pad 22 (GPIO14) to buffer pad 2", got)
	}
	if got := strings.Join(nets["GND"], " "); got != "D1.1 J1.3 SW1.2 U1.1 U1.40 U2.1 U2.3" {
		t.Errorf("GND = %s", got)
	}
	if _, ok := nets["SW1.3"]; ok {
		t.Error("a no-connect pad became a net")
	}

	comps := root.one("components").all("comp")
	var refs []string
	for _, c := range comps {
		refs = append(refs, c.one("ref").value())
	}
	if got := strings.Join(refs, " "); got != "D1 J1 R1 SW1 U1 U2" {
		t.Errorf("components = %s", got)
	}
	for _, c := range comps {
		if c.one("ref").value() != "R1" {
			continue
		}
		if c.one("value").value() != "10k" || c.one("footprint").value() != "Resistor_SMD:R_0402_1005Metric" {
			t.Errorf("R1 = value %q footprint %q", c.one("value").value(), c.one("footprint").value())
		}
		if got := c.one("sheetpath").one("names").value(); got != "/mcu/" {
			t.Errorf("R1 sheet = %q, want /mcu/", got)
		}
		if lib := c.one("libsource"); lib.one("lib").value() != "Device" || lib.one("part").value() != "R" {
			t.Errorf("R1 libsource = %v", lib)
		}
	}
}

// Pcbnew matches a footprint to its symbol by timestamp: a timestamp that
// changed on every run would re-place the whole board on every import.
func TestTheNetlistIsTheSameBytesEveryTime(t *testing.T) {
	a := loadSchematic(t, hallway(t)).KiCadNetlist()
	b := loadSchematic(t, hallway(t)).KiCadNetlist()
	if !bytes.Equal(a, b) {
		t.Fatal("two renders of one schematic differ")
	}
	// KiCad's readers want a date field in every title block; it stays empty.
	if bytes.Count(a, []byte("(date")) != bytes.Count(a, []byte(`(date "")`)) {
		t.Error("the netlist carries a date, so it changes on every regeneration")
	}
}

func TestAReferenceKeepsItsTimestampWhenAnotherPartIsAdded(t *testing.T) {
	stamp := func(netlist []byte, ref string) string {
		_, root := netsOf(t, netlist)
		for _, c := range root.one("components").all("comp") {
			if c.one("ref").value() == ref {
				return c.one("tstamps").value()
			}
		}
		t.Fatalf("%s not in netlist", ref)
		return ""
	}
	before := loadSchematic(t, hallway(t)).KiCadNetlist()
	after := loadSchematic(t, hallway(t,
		[3]string{"sheets/mcu.yaml", "R1: {part: R_0402, value: 10k}", "R1: {part: R_0402, value: 10k}\n  R2: {part: R_0402, value: 10k}"},
		[3]string{"sheets/mcu.yaml", "3V3: [U1.3V3, R1.1]", "3V3: [U1.3V3, R1.1, R2.1]"},
		[3]string{"sheets/mcu.yaml", "SW1.COM]", "SW1.COM, R2.2]"})).KiCadNetlist()
	if stamp(before, "R1") != stamp(after, "R1") {
		t.Error("R1's timestamp moved when R2 was added")
	}
	if stamp(after, "R1") == stamp(after, "R2") {
		t.Error("R1 and R2 share a timestamp")
	}
}

func TestQuotesAndBackslashesInANoteSurviveTheRoundTrip(t *testing.T) {
	note := `the "mute" switch; C:\not\a\path`
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/mcu.yaml", "note: mute; COM to ground cuts the mics", "note: '" + note + "'"}))
	_, root := netsOf(t, s.KiCadNetlist())
	for _, c := range root.one("components").all("comp") {
		if c.one("ref").value() != "SW1" {
			continue
		}
		for _, f := range c.one("fields").all("field") {
			if f.one("name").value() == "Note" && f.list[2].atom != note {
				t.Errorf("note = %q, want %q", f.list[2].atom, note)
			}
		}
	}
}

func TestTheBOMGroupsAPartAndValueOnOneLine(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/mcu.yaml", "R1: {part: R_0402, value: 10k}", "R1: {part: R_0402, value: 10k}\n  R10: {part: R_0402, value: 10k}\n  R2: {part: R_0402, value: 330R}"}))
	rows, err := csv.NewReader(bytes.NewReader(s.BOM())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"10k": {"R1,R10", "C25744"}, "330R": {"R2", "C25104"},
		"SK6812MINI-E": {"D1", "C5149201"}, "BENCH_HEADER": {"J1", ""},
	}
	if len(rows) != 1+7 {
		t.Errorf("BOM has %d lines, want a header and 7: %v", len(rows), rows)
	}
	for _, r := range rows[1:] {
		if w, ok := want[r[0]]; ok && (r[1] != w[0] || r[3] != w[1]) {
			t.Errorf("BOM %s = %v, want designators %s lcsc %q", r[0], r, w[0], w[1])
		}
	}
	for _, r := range rows[1:] {
		if r[0] == "SK6812MINI-E" && r[4] != "SK6812MINI-E" {
			t.Errorf("SK6812 line carries maker part %q", r[4])
		}
	}
	if strings.Join(rows[0], ",") != "Comment,Designator,Footprint,LCSC Part #,Manufacturer Part" {
		t.Errorf("header = %v", rows[0])
	}
}

func TestTheNetlistMatchesTheCheckedInGolden(t *testing.T) {
	got := loadSchematic(t, hallway(t)).KiCadNetlist()
	golden := filepath.Join("testdata", "hallway.net")
	if os.Getenv("CHORUS_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("netlist differs from %s; rerun with CHORUS_UPDATE_GOLDEN=1 and read the diff\n%s", golden, got)
	}
}

// The MCLK link is drawn but left empty: Pcbnew must still get its pads,
// and JLCPCB must not be asked to fit it.
func TestAnUnfittedPartIsOnTheNetlistAndOffTheBOM(t *testing.T) {
	s := loadSchematic(t, hallway(t,
		[3]string{"sheets/mcu.yaml", "R1: {part: R_0402, value: 10k}", "R1: {part: R_0402, value: 10k, dnp: true}"}))
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(s.BOM(), []byte("R1")) {
		t.Errorf("BOM lists the unfitted R1:\n%s", s.BOM())
	}
	nets, _ := netsOf(t, s.KiCadNetlist())
	if !strings.Contains(strings.Join(nets["MUTE_SENSE"], " "), "R1.2") {
		t.Error("the unfitted R1 lost its pads on the netlist")
	}
}
