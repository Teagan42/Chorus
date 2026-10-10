// Package board checks a satellite board's pin map against the module it is
// built on and against the Satellite1 wiring it promises to mirror.
// hardware/chorus-sat/pins.yaml is the map; docs/hardware/README.md, the why.
package board

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Board is one revision of a satellite PCB, as its pin map declares it.
type Board struct {
	Board    string `yaml:"board"`
	Revision string `yaml:"revision"`
	Module   string `yaml:"module"`
	Pins     []Pin  `yaml:"pins"`
}

// Pin is one module GPIO and the schematic net it carries.
type Pin struct {
	GPIO       int    `yaml:"gpio"`
	Net        string `yaml:"net"`
	Dir        string `yaml:"dir"`
	Peer       string `yaml:"peer"`
	Satellite1 string `yaml:"satellite1"`
	Strap      string `yaml:"strap"`
}

// module is what a pin map may and may not use on one ESP32-S3 module.
type module struct {
	reserved map[int]string // never usable, with why
	fixed    map[int]string // usable only for this net
	straps   []int          // usable only with a stated reason
}

// moduleFor names the parts a map may use. Pins are from Espressif's
// ESP32-S3-WROOM-1 datasheet; GPIO22-25 do not exist on the chip.
func moduleFor(name string) (module, bool) {
	switch name {
	case "ESP32-S3-WROOM-1-N16R8":
		return module{
			reserved: reservedN16R8(),
			fixed:    map[int]string{19: "USB_DN", 20: "USB_DP"},
			straps:   []int{0, 3, 46},
		}, true
	}
	return module{}, false
}

func reservedN16R8() map[int]string {
	r := map[int]string{
		45: "the strap that picks the flash supply voltage",
	}
	for g := 22; g <= 25; g++ {
		r[g] = "no such GPIO on the ESP32-S3"
	}
	for g := 26; g <= 32; g++ {
		r[g] = "wired to the in-module flash"
	}
	for g := 33; g <= 37; g++ {
		r[g] = "wired to the in-module octal PSRAM"
	}
	return r
}

var label = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Load reads a pin map. Unknown fields and modules are errors: a typo in a
// map nobody runs is a board nobody can build.
func Load(path string) (Board, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Board{}, fmt.Errorf("read pin map %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var b Board
	if err := dec.Decode(&b); err != nil {
		return Board{}, fmt.Errorf("parse pin map %s: %w", path, err)
	}
	if _, ok := moduleFor(b.Module); !ok {
		return Board{}, fmt.Errorf("pin map %s: module %q is not one this check knows", path, b.Module)
	}
	return b, nil
}

// Net returns the pin carrying net, or nil when the board has no such net.
func (b Board) Net(net string) *Pin {
	for i := range b.Pins {
		if b.Pins[i].Net == net {
			return &b.Pins[i]
		}
	}
	return nil
}

// Check reports every pin the module cannot give, or that two nets claim.
func (b Board) Check() error {
	m, _ := moduleFor(b.Module)
	var errs []error
	byGPIO := map[int]string{}
	byNet := map[string]int{}
	for _, p := range b.Pins {
		where := fmt.Sprintf("%s on GPIO%d", p.Net, p.GPIO)
		if net, ok := m.fixed[p.GPIO]; ok && p.Net != net {
			errs = append(errs, fmt.Errorf("%s: native USB is the flashing and log path; this pin carries only %s", where, net))
		}
		if why, ok := m.reserved[p.GPIO]; ok {
			errs = append(errs, fmt.Errorf("%s: no such pin to spare, it is %s", where, why))
		} else if p.GPIO < 0 || p.GPIO > 48 {
			errs = append(errs, fmt.Errorf("%s: no such GPIO on the ESP32-S3", where))
		}
		if slices.Contains(m.straps, p.GPIO) && strings.TrimSpace(p.Strap) == "" {
			errs = append(errs, fmt.Errorf("%s: a strapping pin needs a strap: reason saying why boot is unaffected", where))
		}
		if !label.MatchString(p.Net) {
			errs = append(errs, fmt.Errorf("%s: net %q is not a schematic label (upper case, digits, underscores)", where, p.Net))
		}
		if !slices.Contains([]string{"in", "out", "io"}, p.Dir) {
			errs = append(errs, fmt.Errorf("%s: dir %q is not in, out or io", where, p.Dir))
		}
		if strings.TrimSpace(p.Peer) == "" {
			errs = append(errs, fmt.Errorf("%s: no peer says what is on the other end", where))
		}
		if other, ok := byGPIO[p.GPIO]; ok {
			errs = append(errs, fmt.Errorf("GPIO%d carries both %s and %s", p.GPIO, other, p.Net))
		}
		if first, ok := byNet[p.Net]; ok {
			errs = append(errs, fmt.Errorf("%s is on both GPIO%d and GPIO%d", p.Net, first, p.GPIO))
		}
		byGPIO[p.GPIO] = p.Net
		byNet[p.Net] = p.GPIO
	}
	return errors.Join(errs...)
}

// Mirrors reports every pin whose satellite1 path, read from the ESPHome
// config at path, names a different GPIO than the map does.
func (b Board) Mirrors(path string) error {
	var errs []error
	for _, p := range b.Pins {
		if p.Satellite1 == "" {
			continue
		}
		got, err := LookupGPIO(path, p.Satellite1)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Net, err))
			continue
		}
		if got != p.GPIO {
			errs = append(errs, fmt.Errorf("%s is on GPIO%d here but GPIO%d at %s in %s", p.Net, p.GPIO, got, p.Satellite1, path))
		}
	}
	return errors.Join(errs...)
}

// LookupGPIO reads the GPIO at a dotted path such as i2s_audio[0].i2s_bclk_pin
// in an ESPHome config. It walks nodes, so !secret and !lambda tags are inert.
func LookupGPIO(path, at string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	n := &doc
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	walked := ""
	for _, step := range strings.Split(at, ".") {
		key, index, hasIndex, err := parseStep(step)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", at, err)
		}
		walked = strings.TrimPrefix(walked+"."+key, ".")
		if n = child(n, key); n == nil {
			return 0, fmt.Errorf("%s has no %s", path, walked)
		}
		if hasIndex {
			walked += fmt.Sprintf("[%d]", index)
			if n.Kind != yaml.SequenceNode || index >= len(n.Content) {
				return 0, fmt.Errorf("%s has no %s", path, walked)
			}
			n = n.Content[index]
		}
	}
	return parseGPIO(n.Value)
}

func parseStep(step string) (key string, index int, hasIndex bool, err error) {
	open := strings.IndexByte(step, '[')
	if open < 0 {
		return step, 0, false, nil
	}
	if !strings.HasSuffix(step, "]") {
		return "", 0, false, fmt.Errorf("step %q has an unclosed index", step)
	}
	index, err = strconv.Atoi(step[open+1 : len(step)-1])
	if err != nil || index < 0 {
		return "", 0, false, fmt.Errorf("step %q has a bad index", step)
	}
	return step[:open], index, true, nil
}

func child(n *yaml.Node, key string) *yaml.Node {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// parseGPIO accepts ESPHome's spellings of a pin: 8, GPIO8 and GPIO08.
func parseGPIO(v string) (int, error) {
	g, err := strconv.Atoi(strings.TrimPrefix(v, "GPIO"))
	if err != nil {
		return 0, fmt.Errorf("%q is not a GPIO", v)
	}
	return g, nil
}
