package iostreams

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swapStdout points os.Stdout at f for the test, so System sees what a redirected run sees.
func swapStdout(t *testing.T, f *os.File) {
	t.Helper()
	orig := os.Stdout
	os.Stdout = f
	t.Cleanup(func() { os.Stdout = orig })
}

func TestSystem_PipeIsNotATerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	swapStdout(t, w)

	ios := System()
	assert.False(t, ios.IsStdoutTTY())
	assert.False(t, ios.ColorEnabled(), "piped output stays plain even without NO_COLOR")
	assert.Zero(t, ios.TerminalWidth(w))
	assert.Equal(t, "ok", ios.Green("ok"))
}

func TestColorize(t *testing.T) {
	ios, _, _, _ := Test()
	assert.Equal(t, "ok", ios.Red("ok"), "Test streams start colorless")
	ios.SetColorEnabled(true)
	assert.Equal(t, "\033[31mok\033[0m", ios.Red("ok"))
	assert.Equal(t, "\033[2mok\033[0m", ios.Dim("ok"))
}

func TestTerminalWidth_FixedWidthWins(t *testing.T) {
	ios, _, out, _ := Test()
	assert.Zero(t, ios.TerminalWidth(out))
	ios.SetTerminalWidth(120)
	assert.Equal(t, 120, ios.TerminalWidth(out))
}
