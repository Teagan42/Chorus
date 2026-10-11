// Command cannedvoice lists every segment of the daemon's canned lines with
// the file its built-in audio is kept in, as JSON on stdout. `task
// canned:voice` pipes it to Kokoro, which renders each one (ADR-0069).
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/teagan42/chorus/internal/provider/kokoro"
	"github.com/teagan42/chorus/internal/satellite"
	"github.com/teagan42/chorus/internal/session"
)

// segment is one piece a stream synthesises on its own.
type segment struct {
	Text string `json:"text"`
	File string `json:"file"`
}

// manifest is what the renderer is asked for: in which voice, and what.
type manifest struct {
	Voice    string    `json:"voice"`
	Segments []segment `json:"segments"`
}

// list is each distinct segment of lines, in the order they are said.
func list(voice string, lines ...string) manifest {
	m := manifest{Voice: voice}
	seen := map[string]bool{}
	for _, line := range lines {
		for _, part := range satellite.Segments(line) {
			if seen[part] {
				continue
			}
			seen[part] = true
			m.Segments = append(m.Segments, segment{Text: part, File: satellite.FallbackName(part)})
		}
	}
	return m
}

func main() {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(list(kokoro.DefaultVoice, session.DefaultCanned.Lines()...)); err != nil {
		fmt.Fprintf(os.Stderr, "cannedvoice: %v\n", err)
		os.Exit(1)
	}
}
