//go:build !windows

package fs

import (
	"os"
	"syscall"
)

// inodeOf returns the inode number for the given FileInfo.
// On POSIX systems this is available via Sys().(*syscall.Stat_t).Ino.
func inodeOf(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Ino
	}
	return 0
}
