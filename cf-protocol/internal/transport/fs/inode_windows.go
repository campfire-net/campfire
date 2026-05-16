//go:build windows

package fs

import "os"

// inodeOf on Windows returns 0 (inodes are not supported).
func inodeOf(info os.FileInfo) uint64 {
	return 0
}
