package iostreams

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// openPTY returns the terminal end of a new pseudo-terminal, or skips where /dev/ptmx is absent.
func openPTY(t *testing.T) *os.File {
	t.Helper()
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close() })
	require.NoError(t, unix.IoctlSetPointerInt(int(ptmx.Fd()), unix.TIOCSPTLCK, 0))
	n, err := unix.IoctlGetInt(int(ptmx.Fd()), unix.TIOCGPTN)
	require.NoError(t, err)
	tty, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tty.Close() })
	return tty
}

func TestSystem_TerminalColorFollowsNO_COLOR(t *testing.T) {
	cases := []struct {
		noColor string
		want    bool
	}{
		{"", true},
		{"1", false},
		{"0", true},
	}
	for _, tc := range cases {
		t.Run("NO_COLOR="+tc.noColor, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			swapStdout(t, openPTY(t))

			ios := System()
			assert.True(t, ios.IsStdoutTTY())
			assert.Equal(t, tc.want, ios.ColorEnabled())
		})
	}
}
