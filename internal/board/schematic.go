package board

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Schematic is a board's netlist as data: the parts it may place
// (parts.yaml), the sheets that wire them (sheets/*.yaml), and the pin map
// the ESP32 module must agree with (pins.yaml). There is no KiCad in CI, so
// this is what the checks read and what the KiCad netlist is generated from.
type Schematic struct {
	Dir    string
	Pins   Board
	Parts  map[string]Part
	Sheets []Sheet
}

// Part is one orderable part and every pad on its footprint.
type Part struct {
	Symbol    string `yaml:"symbol"`    // KiCad lib:symbol, for the eventual drawn schematic
	Footprint string `yaml:"footprint"` // KiCad lib:footprint
	Datasheet string `yaml:"datasheet"`
	Pinout    string `yaml:"pinout"` // where the pad table below was read from
	MPN       string `yaml:"mpn"`    // the manufacturer's part number
	// LCSC is the part's number in JLCPCB's assembly catalogue. A part
	// bought by value, a resistor or capacitor, lists the values the board
	// may use instead, each with its number, or "" to let JLCPCB match the
	// value and footprint from its basic library.
	LCSC   string            `yaml:"lcsc"`
	Values map[string]string `yaml:"values"`
	// Hand says why JLCPCB cannot place this part, when it cannot: it goes
	// on the BOM without a number and is soldered after assembly.
	Hand string    `yaml:"hand"`
	Pins []PartPin `yaml:"pins"`
}

// PartPin is one pad. Pads sharing a name are one pin: wiring the name wires
// every pad, which is how the ground pads under a module are kept together.
type PartPin struct {
	Pad  string `yaml:"pad"`
	Name string `yaml:"name"`
	Type string `yaml:"type"` // a KiCad electrical type
	GPIO *int   `yaml:"gpio"` // set on a module's GPIO pads, checked against pins.yaml
}

// Sheet is one page of the schematic.
type Sheet struct {
	Sheet string              `yaml:"sheet"`
	Title string              `yaml:"title"`
	Parts map[string]Instance `yaml:"parts"`
	// Power names the rails this sheet makes and what makes them. A rail
	// leaves a regulator through an inductor or a connector pin, which carry
	// no electrical type, so the check takes the sheet's word for it.
	Power map[string]string   `yaml:"power"`
	Nets  map[string][]string `yaml:"nets"`
	NC    []string            `yaml:"nc"`
}

// Instance is one placed part.
type Instance struct {
	Part  string `yaml:"part"`
	Value string `yaml:"value"`
	Note  string `yaml:"note"`
	// DNP is a footprint left empty: on the netlist, off the BOM.
	DNP bool `yaml:"dnp"`
}

// lcsc is the catalogue number JLCPCB places this instance from.
func (s Schematic) lcsc(inst Instance) string {
	part := s.Parts[inst.Part]
	if part.Values != nil {
		return part.Values[inst.Value]
	}
	return part.LCSC
}

// Node is one pad on a net.
type Node struct {
	Ref, Pad, Name, Type string
}

// pinTypes are KiCad's electrical types, so the generated netlist carries
// them through to its ERC unchanged.
var pinTypes = []string{
	"input", "output", "bidirectional", "tri_state", "passive", "free",
	"unspecified", "power_in", "power_out", "open_collector", "open_emitter",
	"no_connect",
}

var (
	netName = regexp.MustCompile(`^[+]?[A-Z0-9][A-Z0-9_]*$`)
	refName = regexp.MustCompile(`^[A-Z]+[0-9]+$`)
	pinName = regexp.MustCompile(`^[A-Za-z0-9_+\-]+$`)
)

// LoadSchematic reads pins.yaml, parts.yaml and sheets/*.yaml under dir.
func LoadSchematic(dir string) (Schematic, error) {
	s := Schematic{Dir: dir}
	var err error
	if s.Pins, err = Load(filepath.Join(dir, "pins.yaml")); err != nil {
		return Schematic{}, err
	}
	var lib struct {
		Parts map[string]Part `yaml:"parts"`
	}
	if err := decodeStrict(filepath.Join(dir, "parts.yaml"), &lib); err != nil {
		return Schematic{}, err
	}
	s.Parts = lib.Parts
	paths, err := filepath.Glob(filepath.Join(dir, "sheets", "*.yaml"))
	if err != nil {
		return Schematic{}, err
	}
	if len(paths) == 0 {
		return Schematic{}, fmt.Errorf("%s: no sheets/*.yaml", dir)
	}
	sort.Strings(paths)
	for _, p := range paths {
		var sh Sheet
		if err := decodeStrict(p, &sh); err != nil {
			return Schematic{}, err
		}
		if want := strings.TrimSuffix(filepath.Base(p), ".yaml"); sh.Sheet != want {
			return Schematic{}, fmt.Errorf("%s: sheet is named %q; the file says %q", p, sh.Sheet, want)
		}
		s.Sheets = append(s.Sheets, sh)
	}
	return s, nil
}

func decodeStrict(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// placed is an instance with the sheet it sits on.
type placed struct {
	Instance
	sheet string
}

// Nets resolves every sheet's wiring into nets keyed by name. Labels are
// global: the same name on two sheets is one net, as a KiCad global label is.
// It also returns every reference it could not resolve.
func (s Schematic) Nets() (map[string][]Node, []error) {
	var errs []error
	refs := s.refs(&errs)
	nets := map[string][]Node{}
	seen := map[string]string{} // ref#pad -> net
	for _, sh := range s.Sheets {
		for _, name := range sortedKeys(sh.Nets) {
			if !netName.MatchString(name) {
				errs = append(errs, fmt.Errorf("sheet %s: net %q is not a label (upper case, digits, underscores, optional leading +)", sh.Sheet, name))
			}
			for _, conn := range sh.Nets[name] {
				for _, n := range s.resolve(sh.Sheet, conn, refs, &errs) {
					key := n.Ref + "#" + n.Pad
					if other, ok := seen[key]; ok && other != name {
						errs = append(errs, fmt.Errorf("%s is on both %s and %s", conn, other, name))
						continue
					} else if ok {
						continue
					}
					seen[key] = name
					nets[name] = append(nets[name], n)
				}
			}
		}
	}
	for name := range nets {
		slices.SortFunc(nets[name], func(a, b Node) int {
			return strings.Compare(sortKey(a.Ref, a.Pad), sortKey(b.Ref, b.Pad))
		})
	}
	return nets, errs
}

func (s Schematic) refs(errs *[]error) map[string]placed {
	refs := map[string]placed{}
	for _, sh := range s.Sheets {
		for _, ref := range sortedKeys(sh.Parts) {
			inst := sh.Parts[ref]
			if !refName.MatchString(ref) {
				*errs = append(*errs, fmt.Errorf("sheet %s: %q is not a reference designator like U1 or C12", sh.Sheet, ref))
			}
			if prev, ok := refs[ref]; ok {
				*errs = append(*errs, fmt.Errorf("%s is placed on both sheet %s and sheet %s", ref, prev.sheet, sh.Sheet))
				continue
			}
			if part, ok := s.Parts[inst.Part]; !ok {
				*errs = append(*errs, fmt.Errorf("sheet %s: %s is a %q, which parts.yaml does not define", sh.Sheet, ref, inst.Part))
			} else if part.Values != nil && inst.Value == "" {
				*errs = append(*errs, fmt.Errorf("sheet %s: %s is a %s with no value", sh.Sheet, ref, inst.Part))
			} else if _, listed := part.Values[inst.Value]; part.Values != nil && !listed {
				*errs = append(*errs, fmt.Errorf("sheet %s: %s is a %s of %s, a value parts.yaml does not list for it", sh.Sheet, ref, inst.Part, inst.Value))
			} else if part.Values == nil && inst.Value != "" {
				*errs = append(*errs, fmt.Errorf("sheet %s: %s gives value %s, but a %s is not bought by value", sh.Sheet, ref, inst.Value, inst.Part))
			}
			refs[ref] = placed{inst, sh.Sheet}
		}
	}
	return refs
}

// resolve turns "U1.IO4" into the pads it names.
func (s Schematic) resolve(sheet, conn string, refs map[string]placed, errs *[]error) []Node {
	ref, pin, ok := strings.Cut(conn, ".")
	if !ok {
		*errs = append(*errs, fmt.Errorf("sheet %s: %q is not REF.PIN", sheet, conn))
		return nil
	}
	inst, ok := refs[ref]
	if !ok {
		*errs = append(*errs, fmt.Errorf("sheet %s: %s wires %s, which no sheet places", sheet, conn, ref))
		return nil
	}
	part, ok := s.Parts[inst.Part]
	if !ok {
		return nil
	}
	var out []Node
	for _, p := range part.Pins {
		if p.Name == pin {
			out = append(out, Node{Ref: ref, Pad: p.Pad, Name: p.Name, Type: p.Type})
		}
	}
	if len(out) == 0 {
		*errs = append(*errs, fmt.Errorf("sheet %s: %s: a %s has no pin %s", sheet, conn, inst.Part, pin))
	}
	return out
}

// Check is the board's ERC: everything KiCad's would catch on a netlist,
// plus the ESP32 module wired exactly as pins.yaml says.
func (s Schematic) Check() error {
	errs := s.checkParts()
	if err := s.Pins.Check(); err != nil {
		errs = append(errs, fmt.Errorf("pins.yaml: %w", err))
	}
	nets, nerrs := s.Nets()
	errs = append(errs, nerrs...)
	refs := s.refs(new([]error))

	// Every pad is on one net or marked no-connect, so a forgotten pin is a
	// failing build rather than a floating input on the bench.
	onNet := map[string]string{}
	for name, nodes := range nets {
		for _, n := range nodes {
			onNet[n.Ref+"#"+n.Pad] = name
		}
	}
	nc := map[string]bool{}
	for _, sh := range s.Sheets {
		for _, conn := range sh.NC {
			for _, n := range s.resolve(sh.Sheet, conn, refs, &errs) {
				key := n.Ref + "#" + n.Pad
				if net, ok := onNet[key]; ok {
					errs = append(errs, fmt.Errorf("%s is marked nc but is on %s", conn, net))
				}
				nc[key] = true
			}
		}
	}
	for _, ref := range sortedKeys(refs) {
		part, ok := s.Parts[refs[ref].Part]
		if !ok {
			continue
		}
		for _, p := range part.Pins {
			key := ref + "#" + p.Pad
			_, wired := onNet[key]
			switch {
			case p.Type == "no_connect" && wired:
				errs = append(errs, fmt.Errorf("%s.%s (pad %s) is a no-connect pin wired to %s", ref, p.Name, p.Pad, onNet[key]))
			case p.Type == "no_connect", wired, nc[key]:
			default:
				errs = append(errs, fmt.Errorf("%s.%s (pad %s) is on no net and not marked nc", ref, p.Name, p.Pad))
			}
		}
	}

	rails := map[string]string{}
	for _, sh := range s.Sheets {
		for _, rail := range sortedKeys(sh.Power) {
			if prev, ok := rails[rail]; ok {
				errs = append(errs, fmt.Errorf("rail %s is made on both sheet %s and sheet %s", rail, prev, sh.Sheet))
			}
			rails[rail] = sh.Sheet
			if _, ok := nets[rail]; !ok {
				errs = append(errs, fmt.Errorf("sheet %s makes rail %s, which no pin is on", sh.Sheet, rail))
			}
		}
	}
	for _, name := range sortedKeys(nets) {
		errs = append(errs, checkNet(name, nets[name], rails)...)
	}
	errs = append(errs, s.checkModule(nets, refs)...)
	return errors.Join(errs...)
}

func checkNet(name string, nodes []Node, rails map[string]string) []error {
	var errs []error
	if len(nodes) < 2 {
		return []error{fmt.Errorf("%s has only %s; a net needs two ends", name, nodeList(nodes))}
	}
	var drivers, powered, supplies []Node
	listens := 0
	pins := map[string]bool{}
	for _, n := range nodes {
		// Pads sharing a pin name are one pin: a regulator's two OUT pads
		// are one output, not two fighting.
		first := !pins[n.Ref+"."+n.Name]
		pins[n.Ref+"."+n.Name] = true
		switch {
		case !first:
		case n.Type == "output" || n.Type == "power_out":
			drivers = append(drivers, n)
		}
		switch n.Type {
		case "input":
			listens++
		}
		switch n.Type {
		case "power_in":
			powered = append(powered, n)
		case "power_out":
			supplies = append(supplies, n)
		}
	}
	if len(drivers) > 1 {
		errs = append(errs, fmt.Errorf("%s is driven by %s at once", name, nodeList(drivers)))
	}
	if listens == len(nodes) {
		errs = append(errs, fmt.Errorf("%s has only inputs (%s); nothing drives it", name, nodeList(nodes)))
	}
	if _, isRail := rails[name]; len(powered) > 0 && len(supplies) == 0 && !isRail {
		errs = append(errs, fmt.Errorf("%s powers %s but nothing supplies it; name it under power: on the sheet that makes it", name, nodeList(powered)))
	}
	return errs
}

// checkModule holds the ESP32 module's GPIO pads to pins.yaml in both
// directions: a net the map lists must reach its pad, and a pad the map does
// not list must carry nothing.
func (s Schematic) checkModule(nets map[string][]Node, refs map[string]placed) []error {
	var errs []error
	var module []string
	for _, ref := range sortedKeys(refs) {
		if refs[ref].Part == s.Pins.Module {
			module = append(module, ref)
		}
	}
	if len(module) != 1 {
		return []error{fmt.Errorf("pins.yaml is for one %s; the sheets place %d (%s)", s.Pins.Module, len(module), strings.Join(module, ", "))}
	}
	ref := module[0]
	onPad := map[string]string{}
	for name, nodes := range nets {
		for _, n := range nodes {
			if n.Ref == ref {
				onPad[n.Pad] = name
			}
		}
	}
	gpioPad := map[int]PartPin{}
	for _, p := range s.Parts[s.Pins.Module].Pins {
		if p.GPIO != nil {
			gpioPad[*p.GPIO] = p
		}
	}
	listed := map[int]bool{}
	for _, pin := range s.Pins.Pins {
		listed[pin.GPIO] = true
		pad, ok := gpioPad[pin.GPIO]
		if !ok {
			errs = append(errs, fmt.Errorf("pins.yaml puts %s on GPIO%d; the %s has no pad for it", pin.Net, pin.GPIO, s.Pins.Module))
			continue
		}
		if got := onPad[pad.Pad]; got != pin.Net {
			errs = append(errs, fmt.Errorf("%s.%s (pad %s, GPIO%d) is on %q; pins.yaml says %s", ref, pad.Name, pad.Pad, pin.GPIO, got, pin.Net))
		}
	}
	for _, g := range sortedKeys(gpioPad) {
		pad := gpioPad[g]
		if net, ok := onPad[pad.Pad]; ok && !listed[g] {
			errs = append(errs, fmt.Errorf("%s.%s (GPIO%d) carries %s, which pins.yaml does not list", ref, pad.Name, g, net))
		}
	}
	return errs
}

func (s Schematic) checkParts() []error {
	var errs []error
	for _, name := range sortedKeys(s.Parts) {
		p := s.Parts[name]
		if p.Footprint == "" {
			errs = append(errs, fmt.Errorf("parts.yaml: %s has no footprint", name))
		}
		if p.Pinout == "" {
			errs = append(errs, fmt.Errorf("parts.yaml: %s does not say where its pinout was read from", name))
		}
		pads := map[string]bool{}
		for _, pin := range p.Pins {
			if pads[pin.Pad] {
				errs = append(errs, fmt.Errorf("parts.yaml: %s lists pad %s twice", name, pin.Pad))
			}
			pads[pin.Pad] = true
			if !pinName.MatchString(pin.Name) {
				errs = append(errs, fmt.Errorf("parts.yaml: %s pad %s: pin name %q has characters a REF.PIN cannot carry", name, pin.Pad, pin.Name))
			}
			if !slices.Contains(pinTypes, pin.Type) {
				errs = append(errs, fmt.Errorf("parts.yaml: %s pad %s: %q is not a KiCad pin type", name, pin.Pad, pin.Type))
			}
		}
		if p.LCSC == "" && p.Values == nil && p.Hand == "" {
			errs = append(errs, fmt.Errorf("parts.yaml: %s has no LCSC number; JLCPCB cannot place it, so give one or say under hand: why it is soldered by hand", name))
		}
		if p.LCSC != "" && p.Values != nil {
			errs = append(errs, fmt.Errorf("parts.yaml: %s has both an lcsc number and values; a part bought by value has one per value", name))
		}
		if len(p.Pins) == 0 {
			errs = append(errs, fmt.Errorf("parts.yaml: %s has no pins", name))
		}
	}
	return errs
}

func nodeList(nodes []Node) string {
	parts := make([]string, len(nodes))
	for i, n := range nodes {
		parts[i] = n.Ref + "." + n.Name
	}
	return strings.Join(parts, ", ")
}

// sortKey orders C2 before C10 and pad 2 before pad 10.
func sortKey(ref, pad string) string {
	return natural(ref) + "#" + natural(pad)
}

var digits = regexp.MustCompile(`[0-9]+`)

func natural(s string) string {
	return digits.ReplaceAllStringFunc(s, func(d string) string {
		return fmt.Sprintf("%08s", d)
	})
}

func sortedKeys[K interface{ ~string | ~int }, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
