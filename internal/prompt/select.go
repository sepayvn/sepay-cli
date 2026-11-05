package prompt

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// pageSize is how many options show at once.
const pageSize = 7

var errNoOptions = errors.New("no options to choose from")

// list is the filterable, scrolling option list both pickers share. cursor and offset index into
// visible, which holds indexes into options.
type list struct {
	question
	options []string
	filter  string
	visible []int
	cursor  int
	offset  int
}

func newList(label string, options []string) list {
	l := list{question: question{label: label}, options: options}
	l.refilter()
	return l
}

// refilter keeps the options containing the filter, case-insensitively. A filter that matches
// nothing keeps every option, so the list is never empty.
func (l *list) refilter() {
	l.visible = l.visible[:0]
	needle := strings.ToLower(l.filter)
	for i, o := range l.options {
		if strings.Contains(strings.ToLower(o), needle) {
			l.visible = append(l.visible, i)
		}
	}
	if len(l.visible) == 0 {
		for i := range l.options {
			l.visible = append(l.visible, i)
		}
	}
	l.cursor, l.offset = 0, 0
}

func (l *list) move(delta int) {
	n := len(l.visible)
	l.cursor = (l.cursor + delta + n) % n
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+pageSize {
		l.offset = l.cursor - pageSize + 1
	}
}

// navigate handles the movement and filter keys both pickers share.
func (l *list) navigate(key tea.KeyPressMsg) {
	switch key.String() {
	case "up", "ctrl+p":
		l.move(-1)
	case "down", "ctrl+n":
		l.move(1)
	case "backspace":
		if l.filter == "" {
			return
		}
		r := []rune(l.filter)
		l.filter = string(r[:len(r)-1])
		l.refilter()
	default:
		if key.Text == "" || key.Mod&^tea.ModShift != 0 {
			return
		}
		l.filter += key.Text
		l.refilter()
	}
}

func (l *list) current() int { return l.visible[l.cursor] }

// point puts the cursor on option i, scrolling it into view.
func (l *list) point(i int) {
	if i <= 0 || i >= len(l.options) {
		return
	}
	l.move(i)
}

// render draws the header, the visible page and the key hint; mark decorates each row.
func (l *list) render(hint string, mark func(option int) string) string {
	var b strings.Builder
	head := l.header("")
	if l.filter != "" {
		head += " " + l.filter
	}
	b.WriteString(l.fit(head))
	end := min(l.offset+pageSize, len(l.visible))
	for i := l.offset; i < end; i++ {
		opt := l.visible[i]
		row := "  " + mark(opt) + l.options[opt]
		if i == l.cursor {
			row = accent.Render("> " + mark(opt) + l.options[opt])
		}
		b.WriteString("\n" + l.fit(row))
	}
	b.WriteString("\n" + l.fit(faint.Render(hint)))
	return b.String()
}

type selectModel struct {
	list
	chosen int
}

func (m *selectModel) Init() tea.Cmd    { return nil }
func (m *selectModel) ended() *question { return &m.question }

func (m *selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.handle(msg); ok {
		return m, cmd
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if key.String() == "enter" {
		m.chosen = m.current()
		m.done = true
		return m, tea.Quit
	}
	m.navigate(key)
	return m, nil
}

func (m *selectModel) View() tea.View {
	if v, ok := m.closing(m.options[m.chosen]); ok {
		return v
	}
	return tea.NewView(m.render("↑/↓ move · type to filter · enter select", func(int) string { return "" }))
}

// Select asks for one of options and returns its index; options may repeat a label. The cursor
// starts on option def.
func Select(t Terminal, label string, options []string, def int) (int, error) {
	if len(options) == 0 {
		return 0, errNoOptions
	}
	m := &selectModel{list: newList(label, options)}
	m.point(def)
	m, err := run(t, m)
	if err != nil {
		return 0, err
	}
	return m.chosen, nil
}

type multiSelectModel struct {
	list
	picked map[int]bool
}

func (m *multiSelectModel) Init() tea.Cmd    { return nil }
func (m *multiSelectModel) ended() *question { return &m.question }

func (m *multiSelectModel) mark(opt int) string {
	if m.picked[opt] {
		return "[x] "
	}
	return "[ ] "
}

// chosen returns the picked indexes in option order.
func (m *multiSelectModel) chosen() []int {
	out := []int{}
	for i := range m.options {
		if m.picked[i] {
			out = append(out, i)
		}
	}
	return out
}

func (m *multiSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.handle(msg); ok {
		return m, cmd
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "enter":
		m.done = true
		return m, tea.Quit
	case "space":
		m.picked[m.current()] = !m.picked[m.current()]
	case "right":
		for _, i := range m.visible {
			m.picked[i] = true
		}
	case "left":
		for _, i := range m.visible {
			m.picked[i] = false
		}
	default:
		m.navigate(key)
	}
	return m, nil
}

func (m *multiSelectModel) View() tea.View {
	var names []string
	for _, i := range m.chosen() {
		names = append(names, m.options[i])
	}
	if v, ok := m.closing(strings.Join(names, ", ")); ok {
		return v
	}
	return tea.NewView(m.render("↑/↓ move · space toggle · →/← all/none · type to filter · enter confirm", m.mark))
}

// MultiSelect asks for any number of options and returns their indexes in option order; the
// options in picked start checked.
func MultiSelect(t Terminal, label string, options []string, picked []int) ([]int, error) {
	if len(options) == 0 {
		return nil, errNoOptions
	}
	m := &multiSelectModel{list: newList(label, options), picked: map[int]bool{}}
	for _, i := range picked {
		m.picked[i] = true
	}
	m, err := run(t, m)
	if err != nil {
		return nil, err
	}
	return m.chosen(), nil
}
