package board

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A footprint named <board>:<name> is the board's own, drawn or vendored
// into <board>.pretty beside parts.yaml, the library the board's
// fp-lib-table names. Pcbnew matches a netlist node to a footprint pad by
// number and drops the net of a node whose pad it cannot find, so a pad
// table that disagrees with its footprint is a board with a missing wire.

var padRE = regexp.MustCompile(`\(pad\s+"?([^"\s)]*)"?\s`)

// footprintFile is where a part's own footprint lives, or "" for a footprint
// from KiCad's libraries.
func (s Schematic) footprintFile(footprint string) string {
	lib, name, ok := strings.Cut(footprint, ":")
	if !ok || lib != s.Pins.Board {
		return ""
	}
	return filepath.Join(s.Dir, s.Pins.Board+".pretty", name+".kicad_mod")
}

// footprintPads reads the numbered pads of a .kicad_mod. Unnumbered pads are
// mounting holes and carry no net.
func footprintPads(path string) (map[string]bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pads := map[string]bool{}
	for _, m := range padRE.FindAllSubmatch(src, -1) {
		if pad := string(m[1]); pad != "" {
			pads[pad] = true
		}
	}
	return pads, nil
}

// checkFootprints holds every drawn board footprint to its part's pad table,
// both ways. A footprint not yet drawn is not an error here; Undrawn lists it.
func (s Schematic) checkFootprints() []error {
	var errs []error
	for _, key := range sortedKeys(s.Parts) {
		part := s.Parts[key]
		path := s.footprintFile(part.Footprint)
		if path == "" {
			continue
		}
		pads, err := footprintPads(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("parts.yaml: %s: %w", key, err))
			continue
		}
		listed := map[string]bool{}
		for _, p := range part.Pins {
			listed[p.Pad] = true
			if !pads[p.Pad] {
				errs = append(errs, fmt.Errorf("parts.yaml: %s lists pad %s (%s), which footprint %s does not have", key, p.Pad, p.Name, part.Footprint))
			}
		}
		for _, pad := range sortedKeys(pads) {
			if !listed[pad] {
				errs = append(errs, fmt.Errorf("parts.yaml: footprint %s has pad %s, which %s does not list", part.Footprint, pad, key))
			}
		}
	}
	return errs
}

// Undrawn lists the board footprints parts.yaml names that are not yet in
// the board's library: the layout cannot start until it is empty.
func (s Schematic) Undrawn() []string {
	var out []string
	for _, part := range s.Parts {
		path := s.footprintFile(part.Footprint)
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); os.IsNotExist(err) && !slices.Contains(out, part.Footprint) {
			out = append(out, part.Footprint)
		}
	}
	slices.Sort(out)
	return out
}
