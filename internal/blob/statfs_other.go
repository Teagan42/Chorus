//go:build !linux && !darwin

package blob

import "errors"

// DirFree cannot read free space here, so the guard keeps writing.
func DirFree(string) Disk { return noDisk{} }

type noDisk struct{}

func (noDisk) Free() (uint64, error) {
	return 0, errors.New("blob: free space is not readable on this platform")
}
