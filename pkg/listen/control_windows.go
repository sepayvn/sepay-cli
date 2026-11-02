//go:build windows

package listen

// ignoreBackgroundReadStop is a no-op: Windows has no SIGTTIN.
func ignoreBackgroundReadStop() {}
