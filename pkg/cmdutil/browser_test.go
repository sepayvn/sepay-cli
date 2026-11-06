package cmdutil

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpenBrowser_RejectsNonHTTP(t *testing.T) {
	assert.False(t, OpenBrowser("file:///etc/passwd"))
	assert.False(t, OpenBrowser("javascript:alert(1)"))
	assert.False(t, OpenBrowser("-a calc"))
}

func TestCanOpenBrowser_NotOverSSH(t *testing.T) {
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("SSH_CONNECTION", "203.0.113.5 50000 198.51.100.7 22")
	assert.False(t, CanOpenBrowser())
	assert.False(t, OpenBrowser("https://my.sepay.vn/device"), "nothing is launched where it cannot be seen")

	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "/dev/pts/3")
	assert.False(t, CanOpenBrowser())
}

func TestCanOpenBrowser_NeedsADisplayOutsideMacAndWindows(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	headless := runtime.GOOS != "darwin" && runtime.GOOS != "windows"
	assert.Equal(t, !headless, CanOpenBrowser())

	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	assert.True(t, CanOpenBrowser())
}
