// Command kicadnet checks a board's netlist data and writes what a PCB house
// takes from it: the KiCad netlist Pcbnew imports to lay the board out, and a
// BOM in JLCPCB's assembly columns. hardware/<board>/ is the input; the
// outputs sit next to it and are committed, so gen:check holds them.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/teagan42/chorus/internal/board"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("kicadnet", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "directory for the outputs (default: the board directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dirs := fs.Args()
	if len(dirs) == 0 {
		dirs = []string{"hardware/chorus-sat"}
	}
	for _, dir := range dirs {
		if err := generate(dir, *out); err != nil {
			fmt.Fprintf(stderr, "kicadnet: %v\n", err)
			return 1
		}
	}
	return 0
}

func generate(dir, out string) error {
	s, err := board.LoadSchematic(dir)
	if err != nil {
		return err
	}
	// A netlist that fails the checks would import cleanly and lay out a
	// board that cannot work; refuse it here rather than at the bench.
	if err := s.Check(); err != nil {
		return fmt.Errorf("%s does not check clean, so no netlist was written:\n%w", dir, err)
	}
	if out == "" {
		out = dir
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	files := map[string][]byte{
		s.Pins.Board + ".net": s.KiCadNetlist(),
		"bom.csv":             s.BOM(),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(out, name), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}
