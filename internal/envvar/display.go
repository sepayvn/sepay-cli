package envvar

import (
	"os"
	"runtime"
)

// NoDisplay reports a Unix session other than macOS with neither an X11 nor a Wayland display,
// such as a server reached over SSH: nothing there can show a browser or unlock a keyring.
func NoDisplay() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return false
	}
	return os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == ""
}

// RemoteSession reports a shell reached over SSH, where a launched browser would open on the
// remote machine instead of in front of the user.
func RemoteSession() bool {
	return os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != ""
}
