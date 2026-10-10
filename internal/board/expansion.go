package board

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kit is a bought board's expansion connector: every pad of the plug a
// board underneath mates with, and what the kit puts on each. The kit is
// not ours to change, so this is what a main board is checked against, the
// way a board with its own module is checked against the module's datasheet.
type Kit struct {
	Kit       string `yaml:"kit"`
	Connector string `yaml:"connector"` // the plug's reference on the kit
	Plug      string `yaml:"plug"`      // what the plug is
	// Mate is the parts.yaml part that mates with the plug, pad for pad. A
	// main board places exactly one.
	Mate       string      `yaml:"mate"`
	Mechanical *Mechanical `yaml:"mechanical"`
	Pads       []KitPad    `yaml:"pads"`
}

// Mechanical is where a board underneath must put its outline, its
// mounting holes and the mate, in mm from the outline's centre, y down. The
// KiCad build reads it; the checks only hold it to the mate.
type Mechanical struct {
	Outline struct {
		Diameter float64 `yaml:"diameter"`
		FlatY    float64 `yaml:"flat_y"` // a straight edge across the circle at this y; 0 is none
	} `yaml:"outline"`
	Holes []struct {
		X        float64 `yaml:"x"`
		Y        float64 `yaml:"y"`
		Diameter float64 `yaml:"diameter"`
	} `yaml:"holes"`
	Mate struct {
		X        float64 `yaml:"x"`
		Y        float64 `yaml:"y"`
		Rotation float64 `yaml:"rotation"`
	} `yaml:"mate"`
}

// KitPad is one pad of the kit's plug.
type KitPad struct {
	Pad   string `yaml:"pad"`
	Label string `yaml:"label"` // the kit's own name; "" is unconnected on the kit
	Net   string `yaml:"net"`   // what Chorus's sheets call what the kit puts there
	GPIO  *int   `yaml:"gpio"`  // the module GPIO behind the pad
	// Use is what the kit does with the pad. A GPIO pad with none is one
	// nothing on the kit drives or reads, which a main board may take.
	Use        string `yaml:"use"`
	Satellite1 string `yaml:"satellite1"`
}

// free is a GPIO pad the kit leaves for a board underneath.
func (p KitPad) free() bool { return p.GPIO != nil && p.Use == "" }

// LoadKit reads a kit file. Unknown fields are errors, as in a pin map.
func LoadKit(path string) (Kit, error) {
	var k Kit
	if err := decodeStrict(path, &k); err != nil {
		return Kit{}, err
	}
	return k, nil
}

// Check reports a kit file that contradicts itself: a pad the kit leaves
// unconnected that still says what is on it, a pad with a label and nothing
// said about it, or one GPIO on two pads.
func (k Kit) Check() error {
	var errs []error
	if k.Mate == "" {
		errs = append(errs, errors.New("the kit names no mate: which part a main board fits to its plug"))
	}
	pads := map[string]bool{}
	gpios := map[int]string{}
	for _, p := range k.Pads {
		where := "pad " + p.Pad
		if pads[p.Pad] {
			errs = append(errs, fmt.Errorf("%s is listed twice", where))
		}
		pads[p.Pad] = true
		if p.Label == "" {
			if p.Net != "" || p.GPIO != nil || p.Use != "" || p.Satellite1 != "" {
				errs = append(errs, fmt.Errorf("%s is unconnected on the kit, so it carries no net, gpio, use or satellite1", where))
			}
			continue
		}
		where += " (" + p.Label + ")"
		switch {
		case p.Net == "" && !p.free():
			errs = append(errs, fmt.Errorf("%s has neither a net nor a free GPIO; say what the kit puts there", where))
		case p.Net != "" && p.Use == "":
			errs = append(errs, fmt.Errorf("%s carries %s but does not say what the kit uses it for", where, p.Net))
		case p.Net != "" && !netName.MatchString(p.Net):
			errs = append(errs, fmt.Errorf("%s: net %q is not a schematic label", where, p.Net))
		}
		if p.Satellite1 != "" && p.GPIO == nil {
			errs = append(errs, fmt.Errorf("%s names %s in satellite1.yaml but no gpio", where, p.Satellite1))
		}
		if p.GPIO != nil {
			if other, ok := gpios[*p.GPIO]; ok {
				errs = append(errs, fmt.Errorf("GPIO%d is on both pad %s and pad %s", *p.GPIO, other, p.Pad))
			}
			gpios[*p.GPIO] = p.Pad
		}
	}
	return errors.Join(errs...)
}

// Mirrors reports every pad whose satellite1 path, read from the ESPHome
// config at path, names a different GPIO than the kit file does.
func (k Kit) Mirrors(path string) error {
	var errs []error
	for _, p := range k.Pads {
		if p.Satellite1 == "" || p.GPIO == nil {
			continue
		}
		got, err := LookupGPIO(path, p.Satellite1)
		if err != nil {
			errs = append(errs, fmt.Errorf("pad %s (%s): %w", p.Pad, p.Label, err))
			continue
		}
		if got != *p.GPIO {
			errs = append(errs, fmt.Errorf("pad %s (%s) is GPIO%d here but GPIO%d at %s in %s", p.Pad, p.Label, *p.GPIO, got, p.Satellite1, path))
		}
	}
	return errors.Join(errs...)
}

// Pad returns the kit's pad, or false when the plug has none by that number.
func (k Kit) Pad(pad string) (KitPad, bool) {
	for _, p := range k.Pads {
		if p.Pad == pad {
			return p, true
		}
	}
	return KitPad{}, false
}

// checkExpansion holds the main board's receptacle to the kit's plug pad by
// pad, in both directions: the receptacle has the plug's pads; a pad the kit
// leaves unconnected stays unconnected; a pad the kit uses is unconnected
// here or on the kit's net, and every kit ground is grounded; a free GPIO
// pad is unconnected or carries the pins.yaml net for that GPIO. A net
// named for one the kit carries must reach it, so a name cannot claim a
// signal the board never gets.
func (s Schematic) checkExpansion(nets map[string][]Node, refs map[string]placed) []error {
	k := s.Kit
	var errs []error
	var mates []string
	for _, ref := range sortedKeys(refs) {
		if refs[ref].Part == k.Mate {
			mates = append(mates, ref)
		}
	}
	if len(mates) != 1 {
		return []error{fmt.Errorf("%s mates one %s; the sheets place %d (%s)", s.Pins.Expansion, k.Mate, len(mates), strings.Join(mates, ", "))}
	}
	ref := mates[0]
	where := func(pad string) string { return ref + "." + pad }

	have := map[string]bool{}
	for _, p := range s.Parts[k.Mate].Pins {
		have[p.Pad] = true
		if _, ok := k.Pad(p.Pad); !ok {
			errs = append(errs, fmt.Errorf("parts.yaml: %s has pad %s, which %s %s does not", k.Mate, p.Pad, k.Connector, s.Pins.Expansion))
		}
	}
	for _, p := range k.Pads {
		if !have[p.Pad] {
			errs = append(errs, fmt.Errorf("parts.yaml: %s lacks pad %s (%s), which %s has", k.Mate, p.Pad, p.Label, k.Connector))
		}
	}

	onPad := map[string]string{}
	for name, nodes := range nets {
		for _, n := range nodes {
			if n.Ref == ref {
				onPad[n.Pad] = name
			}
		}
	}
	byPad := map[string]Pin{}
	for _, pin := range s.Pins.Pins {
		byPad[pin.Pad] = pin
	}
	reaches := map[string]bool{}
	for _, p := range k.Pads {
		got, wired := onPad[p.Pad]
		if wired && got == p.Net {
			reaches[p.Net] = true
		}
		switch {
		case p.Label == "":
			if wired {
				errs = append(errs, fmt.Errorf("%s is on %s, but %s pad %s is unconnected on the kit", where(p.Pad), got, k.Connector, p.Pad))
			}
		case p.Net == "GND":
			if got != "GND" {
				errs = append(errs, fmt.Errorf("%s is on %q; the kit grounds %s pad %s, so it is GND here too", where(p.Pad), got, k.Connector, p.Pad))
			}
		case !p.free():
			if wired && got != p.Net {
				errs = append(errs, fmt.Errorf("%s is on %s, but the kit puts %s there (%s)", where(p.Pad), got, p.Net, p.Use))
			}
		default:
			pin, listed := byPad[p.Pad]
			if wired && (!listed || pin.Net != got) {
				errs = append(errs, fmt.Errorf("%s (GPIO%d) carries %s, which pins.yaml does not put on pad %s", where(p.Pad), *p.GPIO, got, p.Pad))
			}
		}
	}
	for _, p := range k.Pads {
		if p.Net == "" || p.Net == "GND" || reaches[p.Net] {
			continue
		}
		if _, named := nets[p.Net]; named {
			reaches[p.Net] = true // reported once
			errs = append(errs, fmt.Errorf("%s is the kit's name for %s pad %s, but this board's %s never reaches it", p.Net, k.Connector, p.Pad, p.Net))
		}
	}

	for _, pin := range s.Pins.Pins {
		p, ok := k.Pad(pin.Pad)
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("pins.yaml puts %s on pad %s, which %s does not have", pin.Net, pin.Pad, k.Connector))
			continue
		case p.GPIO == nil || *p.GPIO != pin.GPIO:
			errs = append(errs, fmt.Errorf("pins.yaml puts %s on GPIO%d at pad %s, but the kit's pad %s (%s) is %s", pin.Net, pin.GPIO, pin.Pad, pin.Pad, p.Label, kitGPIO(p)))
		case p.Use != "":
			errs = append(errs, fmt.Errorf("pins.yaml takes GPIO%d (pad %s) for %s, but the kit already uses it: %s", pin.GPIO, pin.Pad, pin.Net, p.Use))
		}
		if got := onPad[pin.Pad]; got != pin.Net {
			errs = append(errs, fmt.Errorf("%s is on %q; pins.yaml says %s", where(pin.Pad), got, pin.Net))
		}
	}
	return errs
}

func kitGPIO(p KitPad) string {
	if p.GPIO == nil {
		return "no GPIO"
	}
	return fmt.Sprintf("GPIO%d", *p.GPIO)
}

// ConfigGPIOs lists every GPIO an ESPHome config sets, with the path of each
// setting, so a board can prove it takes none the firmware already drives.
// A pin is any value spelled GPIOn, or a number under a key ending in _pin.
func ConfigGPIOs(path string) (map[int][]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := map[int][]string{}
	var walk func(n *yaml.Node, at, key string)
	walk = func(n *yaml.Node, at, key string) {
		switch n.Kind {
		case yaml.DocumentNode:
			for _, c := range n.Content {
				walk(c, at, key)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i].Value
				walk(n.Content[i+1], strings.TrimPrefix(at+"."+k, "."), k)
			}
		case yaml.SequenceNode:
			for i, c := range n.Content {
				walk(c, fmt.Sprintf("%s[%d]", at, i), key)
			}
		case yaml.ScalarNode:
			if !strings.HasPrefix(n.Value, "GPIO") && !strings.HasSuffix(key, "_pin") {
				return
			}
			if g, err := parseGPIO(n.Value); err == nil {
				out[g] = append(out[g], at)
			}
		}
	}
	walk(&doc, "", "")
	return out, nil
}
