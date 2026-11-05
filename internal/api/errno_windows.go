//go:build windows

package api

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// Winsock reports its own WSAE* numbers, which syscall's Windows E* constants never match.

func isConnRefused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED) || errors.Is(err, syscall.ECONNREFUSED)
}

func isNoRoute(err error) bool {
	return errors.Is(err, windows.WSAENETUNREACH) || errors.Is(err, windows.WSAEHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH)
}

func isConnReset(err error) bool {
	return errors.Is(err, windows.WSAECONNRESET) || errors.Is(err, syscall.ECONNRESET)
}
