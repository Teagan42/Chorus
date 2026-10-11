//go:build linux || darwin

package blob

import (
	"fmt"
	"syscall"
)

// DirFree reads the space left to an unprivileged writer on root's
// filesystem. The daemon's only disk probe; tests inject a Disk instead.
func DirFree(root string) Disk { return dirDisk(root) }

type dirDisk string

func (d dirDisk) Free() (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(string(d), &st); err != nil {
		return 0, fmt.Errorf("blob: statfs %q: %w", string(d), err)
	}
	return st.Bavail * uint64(st.Bsize), nil
}
