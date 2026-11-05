// Package iostreams bundles a command's streams and TTY/color state.
package iostreams

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/sepayvn/sepay-cli/internal/envvar"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

// IOStreams bundles a command's in/out/err streams plus TTY and color state.
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer

	stdinTTY     bool
	stdoutTTY    bool
	stderrTTY    bool
	colorEnabled bool
	termWidth    int // fixed by SetTerminalWidth; 0 asks the terminal

	writeMu sync.Mutex
}

// Block runs fn holding the screen so its lines aren't interleaved. fn must not call Block again.
func (s *IOStreams) Block(fn func()) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	fn()
}

// System wires IOStreams to the real terminal; NO_COLOR is read by value, so NO_COLOR=0 keeps color.
func System() *IOStreams {
	stdoutTTY := isTerminal(os.Stdout)
	return &IOStreams{
		In:           os.Stdin,
		Out:          os.Stdout,
		ErrOut:       os.Stderr,
		stdinTTY:     isTerminal(os.Stdin),
		stdoutTTY:    stdoutTTY,
		stderrTTY:    isTerminal(os.Stderr),
		colorEnabled: stdoutTTY && !envvar.Enabled("NO_COLOR"),
	}
}

// Test returns IOStreams backed by buffers, for tests in other packages.
func Test() (*IOStreams, *bytes.Buffer, *bytes.Buffer, *bytes.Buffer) {
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	return &IOStreams{In: in, Out: out, ErrOut: errOut}, in, out, errOut
}

// Plain returns colorless IOStreams that discard writes, for building strings stored elsewhere.
func Plain() *IOStreams {
	return &IOStreams{In: strings.NewReader(""), Out: io.Discard, ErrOut: io.Discard}
}

// IsStdinTTY reports whether stdin is a terminal.
func (s *IOStreams) IsStdinTTY() bool { return s.stdinTTY }

// SetStdinTTY overrides the detected stdin TTY state.
func (s *IOStreams) SetStdinTTY(tty bool) { s.stdinTTY = tty }

// IsStdoutTTY reports whether stdout is a terminal.
func (s *IOStreams) IsStdoutTTY() bool { return s.stdoutTTY }

// SetStdoutTTY overrides the detected stdout TTY state.
func (s *IOStreams) SetStdoutTTY(tty bool) { s.stdoutTTY = tty }

// IsStderrTTY reports whether stderr is a terminal.
func (s *IOStreams) IsStderrTTY() bool { return s.stderrTTY }

// SetStderrTTY overrides the detected stderr TTY state.
func (s *IOStreams) SetStderrTTY(tty bool) { s.stderrTTY = tty }

// CanPrompt reports whether stdin and stdout are both terminals.
func (s *IOStreams) CanPrompt() bool { return s.stdinTTY && s.stdoutTTY }

// ColorEnabled reports whether colored output is enabled.
func (s *IOStreams) ColorEnabled() bool { return s.colorEnabled }

// SetColorEnabled turns colored output on or off.
func (s *IOStreams) SetColorEnabled(enabled bool) { s.colorEnabled = enabled }

// TerminalWidth returns w's terminal width, or 0 when w is not a terminal.
func (s *IOStreams) TerminalWidth(w io.Writer) int {
	if s.termWidth > 0 {
		return s.termWidth
	}
	f, ok := w.(*os.File)
	if !ok || !isTerminal(f) {
		return 0
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return width
}

// SetTerminalWidth fixes the width TerminalWidth reports.
func (s *IOStreams) SetTerminalWidth(width int) { s.termWidth = width }

// Green wraps t in ANSI green when color is enabled.
func (s *IOStreams) Green(t string) string { return s.colorize("32", t) }

// Red wraps t in ANSI red (see Green).
func (s *IOStreams) Red(t string) string { return s.colorize("31", t) }

// Yellow wraps t in ANSI yellow (see Green).
func (s *IOStreams) Yellow(t string) string { return s.colorize("33", t) }

// Dim wraps t in ANSI faint (see Green).
func (s *IOStreams) Dim(t string) string { return s.colorize("2", t) }

// Cyan wraps t in ANSI cyan (see Green).
func (s *IOStreams) Cyan(t string) string { return s.colorize("36", t) }

func (s *IOStreams) colorize(code, t string) string {
	if !s.colorEnabled {
		return t
	}
	return "\033[" + code + "m" + t + "\033[0m"
}

func isTerminal(f *os.File) bool {
	fd := f.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}
