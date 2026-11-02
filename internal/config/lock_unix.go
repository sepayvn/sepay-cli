//go:build !windows

package config

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryAcquireLock takes a non-blocking exclusive flock; false with nil error means another process holds it.
func tryAcquireLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return false, err
}

// tryAcquireSharedLock is tryAcquireLock for a shared lock.
func tryAcquireSharedLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return false, err
}

func releaseLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }
