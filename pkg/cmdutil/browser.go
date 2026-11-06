package cmdutil

import (
	"os/exec"
	"runtime"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/envvar"
)

// CanOpenBrowser reports whether a launched browser would appear in front of the user. Over SSH or
// on a display-less server xdg-open may still start, open nothing, and report success.
func CanOpenBrowser() bool {
	return !envvar.RemoteSession() && !envvar.NoDisplay()
}

// OpenBrowser opens an http/https rawURL via a fixed launcher (never a shell) and reports whether it started.
// A false result makes the caller print the URL instead.
func OpenBrowser(rawURL string) bool {
	if !CanOpenBrowser() || (!strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://")) {
		return false
	}
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		name = "xdg-open"
	}
	if _, err := exec.LookPath(name); err != nil {
		return false
	}
	return exec.Command(name, append(args, rawURL)...).Start() == nil //nolint:gosec,noctx // G204: fixed launcher, the http(s) URL is one argv entry; it must outlive the command
}
