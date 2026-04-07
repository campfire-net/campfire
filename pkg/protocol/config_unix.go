//go:build !windows

package protocol

import (
	"os"
	"syscall"
)

// defaultOwnerTrusted checks that the directory at dirPath is owned by the
// current UID.
func defaultOwnerTrusted(dirPath string) bool {
	info, err := os.Stat(dirPath)
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	return stat.Uid == uint32(os.Getuid())
}
