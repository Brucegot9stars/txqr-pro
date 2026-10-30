//go:build windows

package scanner

import "syscall"

// MoveFile publishes atomically without replacing an existing destination.
func publishExclusive(from, to string) error {
	src, err := syscall.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	dst, err := syscall.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return syscall.MoveFile(src, dst)
}
