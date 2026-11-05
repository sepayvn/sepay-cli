// Package prompt draws the interactive questions the CLI asks on a terminal. Each question is a
// small Bubble Tea model rendered inline (no alt screen) to the writer it is given, leaving one
// "? label › answer" line behind once answered.
package prompt

import (
	"errors"
	"io"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// ErrInterrupted reports that the prompt ended without an answer: Ctrl+C, Esc, or a signal that
// stopped the program.
var ErrInterrupted = errors.New("prompt interrupted")

// Terminal is where a prompt reads keys and draws. The caller picks Out: stderr, or stdout when
// stderr is redirected.
type Terminal struct {
	In    io.Reader
	Out   io.Writer
	Color bool
}

var (
	accent = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	faint  = lipgloss.NewStyle().Faint(true)
	bold   = lipgloss.NewStyle().Bold(true)
)

// question is the state every prompt model shares: its label, the terminal width and how it ended.
type question struct {
	label       string
	width       int
	done        bool
	interrupted bool
}

// handle consumes the messages every prompt treats alike; ok is false when the model should
// handle msg itself.
func (q *question) handle(msg tea.Msg) (cmd tea.Cmd, ok bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		q.width = msg.Width
		return nil, true
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			q.interrupted = true
			return tea.Quit, true
		}
	}
	return nil, false
}

// header renders "? label", followed by the answer once there is one.
func (q *question) header(answer string) string {
	line := bold.Render("? " + q.label)
	if answer != "" {
		line += " " + faint.Render("›") + " " + accent.Render(answer)
	}
	return line
}

// fit cuts a line to the terminal width so a long option cannot wrap and break the redraw.
func (q *question) fit(line string) string {
	if q.width <= 0 {
		return line
	}
	return ansi.Truncate(line, q.width, "…")
}

// closing renders the line a finished prompt leaves behind; an interrupted prompt keeps only its label.
func (q *question) closing(answer string) (tea.View, bool) {
	if !q.done && !q.interrupted {
		return tea.View{}, false
	}
	if q.interrupted {
		answer = ""
	}
	return tea.NewView(q.fit(q.header(answer)) + "\n"), true
}

type model interface {
	tea.Model
	ended() *question
}

func (t Terminal) options() []tea.ProgramOption {
	opts := []tea.ProgramOption{tea.WithInput(t.In), tea.WithOutput(t.Out)}
	if !t.Color {
		opts = append(opts, tea.WithColorProfile(colorprofile.Ascii))
	}
	return opts
}

func run[M model](t Terminal, m M) (M, error) {
	final, err := tea.NewProgram(m, t.options()...).Run()
	if errors.Is(err, tea.ErrProgramPanic) {
		return m, err
	}
	if errors.Is(err, tea.ErrInterrupted) {
		return m, ErrInterrupted
	}
	// tea wraps a failed read of the input in ErrProgramKilled: report it, never as an interrupt.
	if err != nil {
		return m, err
	}
	// SIGTERM quits the program with no answer given.
	out, ok := final.(M)
	if !ok || out.ended().interrupted || !out.ended().done {
		return m, ErrInterrupted
	}
	return out, nil
}
