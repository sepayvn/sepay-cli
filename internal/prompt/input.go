package prompt

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type inputModel struct {
	question
	field textinput.Model
	def   string
	value string
}

func newInput(label, def string, echo textinput.EchoMode) *inputModel {
	field := textinput.New()
	field.Prompt = ""
	field.EchoMode = echo
	field.Focus()
	return &inputModel{question: question{label: label}, field: field, def: def}
}

func (m *inputModel) Init() tea.Cmd    { return nil }
func (m *inputModel) ended() *question { return &m.question }

func (m *inputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.handle(msg); ok {
		return m, cmd
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "enter" {
		m.value = m.field.Value()
		m.done = true
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.field, cmd = m.field.Update(msg)
	return m, cmd
}

func (m *inputModel) View() tea.View {
	answer := m.value
	if answer == "" {
		answer = m.def
	}
	if m.field.EchoMode != textinput.EchoNormal {
		answer = ""
	}
	if v, ok := m.closing(answer); ok {
		return v
	}
	head := m.header("")
	if m.def != "" {
		head += " " + faint.Render("("+m.def+")")
	}
	return tea.NewView(head + " " + m.field.View())
}

// Input asks for free text and returns what was typed. def is only shown: an empty answer stays
// empty, so the caller keeps its own unsanitised default.
func Input(t Terminal, label, def string) (string, error) {
	m, err := run(t, newInput(label, def, textinput.EchoNormal))
	if err != nil {
		return "", err
	}
	return m.value, nil
}

// Password asks for a secret. Keystrokes show as a mask while typing; the line left behind carries
// neither the value nor its length.
func Password(t Terminal, label string) (string, error) {
	m, err := run(t, newInput(label, "", textinput.EchoPassword))
	if err != nil {
		return "", err
	}
	return m.value, nil
}

type confirmModel struct {
	question
	def    bool
	answer bool
}

func (m *confirmModel) Init() tea.Cmd    { return nil }
func (m *confirmModel) ended() *question { return &m.question }

func (m *confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.handle(msg); ok {
		return m, cmd
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch strings.ToLower(key.String()) {
	case "enter":
		m.answer = m.def
	case "y":
		m.answer = true
	case "n":
		m.answer = false
	default:
		return m, nil
	}
	m.done = true
	return m, tea.Quit
}

func (m *confirmModel) View() tea.View {
	answer := "No"
	if m.answer {
		answer = "Yes"
	}
	if v, ok := m.closing(answer); ok {
		return v
	}
	hint := "[y/N]"
	if m.def {
		hint = "[Y/n]"
	}
	return tea.NewView(m.header("") + " " + faint.Render(hint) + " ")
}

// Confirm asks a yes/no question, returning def on Enter.
func Confirm(t Terminal, label string, def bool) (bool, error) {
	m, err := run(t, &confirmModel{question: question{label: label}, def: def})
	if err != nil {
		return false, err
	}
	return m.answer, nil
}
