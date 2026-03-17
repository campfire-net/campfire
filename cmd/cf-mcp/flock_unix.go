//go:build !windows

package main

import "syscall"

// tryFlock attempts an exclusive non-blocking file lock.
func tryFlock(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
}
