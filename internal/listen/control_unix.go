//go:build !windows

package listen

import (
	"os/signal"
	"syscall"
)

// ignoreBackgroundReadStop ignores SIGTTIN so a backgrounded `listen &` gets EIO on read
// instead of being stopped, and keeps streaming.
func ignoreBackgroundReadStop() { signal.Ignore(syscall.SIGTTIN) }
