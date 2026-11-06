package prompt

import (
	"context"
	"errors"
	"os"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// DeviceWait is the device-login wait screen. Strings arrive sanitised and translated. An empty
// URL hides the copy-link key; a nil Open hides the open-browser key.
type DeviceWait struct {
	URL  string
	Code string
	Open func() bool
	Text DeviceWaitText
}

// DeviceWaitText is the wording of the wait screen.
type DeviceWaitText struct {
	CopyLink, CopyCode, OpenBrowser, Cancel string
	Waiting                                 string
	LinkCopied, CodeCopied                  string
	Opened, NotOpened                       string
}

type pollDone struct{ err error }

type waitModel struct {
	question
	screen  DeviceWait
	tmux    bool
	spinner spinner.Model
	status  string
	err     error
}

// WaitDevice draws the wait screen while poll runs and returns poll's error. Keys copy the link or
// the code to the clipboard through OSC 52, which reaches the local clipboard over SSH, or open
// the browser; Ctrl+C or Esc cancel poll and return ErrInterrupted.
func WaitDevice(ctx context.Context, t Terminal, w DeviceWait, poll func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := &waitModel{screen: w, tmux: os.Getenv("TMUX") != "", spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot))}
	p := tea.NewProgram(m, append(t.options(), tea.WithContext(ctx))...)
	go func() { p.Send(pollDone{poll(ctx)}) }()

	_, err := p.Run()
	switch {
	case m.done:
		return m.err
	case m.interrupted, errors.Is(err, tea.ErrInterrupted):
		return ErrInterrupted
	case err != nil:
		return err
	}
	// SIGTERM quits the program before poll finished.
	return ErrInterrupted
}

func (m *waitModel) Init() tea.Cmd { return m.spinner.Tick }

func (m *waitModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.handle(msg); ok {
		return m, cmd
	}
	switch msg := msg.(type) {
	case pollDone:
		m.done, m.err = true, msg.err
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		switch msg.String() {
		case "c":
			if m.screen.URL != "" {
				m.status = m.screen.Text.LinkCopied
				cmd := m.copy(m.screen.URL)
				return m, cmd
			}
		case "y":
			m.status = m.screen.Text.CodeCopied
			cmd := m.copy(m.screen.Code)
			return m, cmd
		case "enter":
			if m.screen.Open != nil {
				m.status = m.screen.Text.NotOpened
				if m.screen.Open() {
					m.status = m.screen.Text.Opened
				}
			}
		}
	}
	return m, nil
}

// copy sets the clipboard with OSC 52. tmux takes the plain sequence with set-clipboard on and the
// wrapped one with allow-passthrough on; both carry the same text.
func (m *waitModel) copy(s string) tea.Cmd {
	if !m.tmux {
		return tea.SetClipboard(s)
	}
	return tea.Batch(tea.SetClipboard(s), tea.Raw(ansi.TmuxPassthrough(ansi.SetSystemClipboard(s))))
}

func (m *waitModel) View() tea.View {
	if m.done || m.interrupted {
		return tea.NewView("")
	}
	t := m.screen.Text
	keys := ""
	if m.screen.URL != "" {
		keys += "c " + t.CopyLink + " · "
	}
	keys += "y " + t.CopyCode + " · "
	if m.screen.Open != nil {
		keys += "enter " + t.OpenBrowser + " · "
	}
	keys += "ctrl+c " + t.Cancel
	status := t.Waiting
	if m.status != "" {
		status = m.status
	}
	return tea.NewView(m.fit(faint.Render(keys)) + "\n" + m.fit(m.spinner.View()+" "+status))
}
