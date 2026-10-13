package main

import (
	"os"

	"github.com/teagan42/chorus/internal/identity"
)

// readTake returns the PCM of one take, refused unless it is already in the
// satellite's own format (identity.ParseWAV).
func readTake(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return identity.ParseWAV(b)
}
