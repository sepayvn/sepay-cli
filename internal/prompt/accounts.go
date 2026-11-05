package prompt

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Account is one bank account in an Accounts form: what it notifies is edited in place.
type Account struct {
	Title string // e.g. "MSB · 0000007201 · NGUYEN VAN A"
	Note  string // shown beside the title, e.g. "virtual accounts only"
	// PrimaryFixed leaves the account-number choice at Primary, shown but not editable.
	PrimaryFixed bool
	Primary      bool
	Modes        []Mode // offered VA modes; one mode is shown but not editable
	Mode         int    // index into Modes
	VAs          []Option
	Picked       []int // indexes into VAs, checked when Modes[Mode].Pick
}

// Mode is one VA mode: Label in the form, Short in the summary line. Pick marks the mode that
// notifies only the checked VAs; None the one that notifies no VA.
type Mode struct {
	Label string
	Short string
	Pick  bool
	None  bool
}

// Option is one checkable VA: Label in the form, Short in the summary line.
type Option struct {
	Label string
	Short string
}

// AccountAnswer is what one account was set to.
type AccountAnswer struct {
	Primary bool
	Mode    int
	Picked  []int
}

type rowKind int

const (
	rowPrimary rowKind = iota
	rowMode
	rowVA
)

// row is one focusable line: the account it belongs to and, for rowVA, the VA.
type row struct {
	kind    rowKind
	account int
	va      int
}

type accountsModel struct {
	question
	accounts []Account
	picked   []map[int]bool
	cursor   int
	height   int
	problem  string
}

func newAccounts(label string, accounts []Account) *accountsModel {
	m := &accountsModel{question: question{label: label}, accounts: accounts}
	for _, a := range accounts {
		set := map[int]bool{}
		for _, i := range a.Picked {
			set[i] = true
		}
		m.picked = append(m.picked, set)
	}
	return m
}

// rows lists the focusable lines in display order; VA rows exist only under a Pick mode.
func (m *accountsModel) rows() []row {
	var out []row
	for i, a := range m.accounts {
		if !a.PrimaryFixed {
			out = append(out, row{kind: rowPrimary, account: i})
		}
		if len(a.Modes) > 1 {
			out = append(out, row{kind: rowMode, account: i})
		}
		if a.Modes[a.Mode].Pick {
			for v := range a.VAs {
				out = append(out, row{kind: rowVA, account: i, va: v})
			}
		}
	}
	return out
}

func (m *accountsModel) Init() tea.Cmd    { return nil }
func (m *accountsModel) ended() *question { return &m.question }

func (m *accountsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.height = size.Height
	}
	if cmd, ok := m.handle(msg); ok {
		return m, cmd
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	rows := m.rows()
	if len(rows) == 0 {
		if key.String() == "enter" {
			if m.problem = m.check(); m.problem == "" {
				m.done = true
				return m, tea.Quit
			}
		}
		return m, nil
	}
	m.cursor = min(m.cursor, len(rows)-1)
	r := rows[m.cursor]
	a := &m.accounts[r.account]
	m.problem = ""
	switch key.String() {
	case "up", "k", "shift+tab":
		m.cursor = (m.cursor - 1 + len(rows)) % len(rows)
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % len(rows)
	case "space", "x":
		switch r.kind {
		case rowPrimary:
			a.Primary = !a.Primary
		case rowVA:
			m.picked[r.account][r.va] = !m.picked[r.account][r.va]
		case rowMode:
			a.Mode = (a.Mode + 1) % len(a.Modes)
		}
	case "left", "h":
		if r.kind == rowMode {
			a.Mode = (a.Mode - 1 + len(a.Modes)) % len(a.Modes)
		}
	case "right", "l":
		if r.kind == rowMode {
			a.Mode = (a.Mode + 1) % len(a.Modes)
		}
	case "enter":
		if m.problem = m.check(); m.problem == "" {
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// check names the first account that would be notified of nothing, "" when every one notifies
// something: the account number, all VAs, or at least one checked VA.
func (m *accountsModel) check() string {
	for i, a := range m.accounts {
		mode := a.Modes[a.Mode]
		switch {
		case mode.Pick && len(m.checked(i)) == 0:
			return a.Title + ": check at least one virtual account"
		case mode.None && !a.Primary:
			return a.Title + ": notifies nothing; turn on the account number or pick virtual accounts"
		}
	}
	return ""
}

func (m *accountsModel) checked(account int) []int {
	var out []int
	for v := range m.accounts[account].VAs {
		if m.picked[account][v] {
			out = append(out, v)
		}
	}
	return out
}

func checkbox(on bool) string {
	if on {
		return "[x] "
	}
	return "[ ] "
}

// summary is the line an answered account leaves behind.
func (m *accountsModel) summary(i int) string {
	a := m.accounts[i]
	var parts []string
	if a.Primary {
		parts = append(parts, "account number")
	}
	mode := a.Modes[a.Mode]
	switch {
	case mode.Pick:
		var names []string
		for _, v := range m.checked(i) {
			names = append(names, a.VAs[v].Short)
		}
		parts = append(parts, "virtual accounts "+strings.Join(names, ", "))
	case !mode.None:
		parts = append(parts, mode.Short)
	}
	return strings.Join(parts, " + ")
}

func (m *accountsModel) View() tea.View {
	if m.done || m.interrupted {
		if m.interrupted {
			v, _ := m.closing("")
			return v
		}
		var b strings.Builder
		for i, a := range m.accounts {
			q := question{label: a.Title, width: m.width}
			b.WriteString(q.fit(q.header(m.summary(i))) + "\n")
		}
		return tea.NewView(b.String())
	}

	rows := m.rows()
	focus := func(r row) bool {
		return m.cursor < len(rows) && rows[m.cursor] == r
	}
	line := func(r row, text string) string {
		if focus(r) {
			return accent.Render("> " + text)
		}
		return "  " + text
	}

	var lines []string
	cursorLine := 0
	for i, a := range m.accounts {
		if i > 0 {
			lines = append(lines, "")
		}
		title := bold.Render(a.Title)
		if a.Note != "" {
			title += "  " + faint.Render(a.Note)
		}
		lines = append(lines, "  "+title)

		primary := row{kind: rowPrimary, account: i}
		if a.PrimaryFixed {
			if a.Primary {
				lines = append(lines, "    "+faint.Render("[x] Account number (the only option)"))
			}
		} else {
			if focus(primary) {
				cursorLine = len(lines)
			}
			lines = append(lines, "  "+line(primary, checkbox(a.Primary)+"Account number"))
		}

		mode := a.Modes[a.Mode]
		modeRow := row{kind: rowMode, account: i}
		switch {
		case len(a.Modes) > 1:
			if focus(modeRow) {
				cursorLine = len(lines)
			}
			lines = append(lines, "  "+line(modeRow, "Virtual accounts  ‹ "+mode.Label+" ›"))
		case !mode.None:
			lines = append(lines, "    "+faint.Render("Virtual accounts: "+mode.Label))
		}
		if mode.Pick {
			for v, va := range a.VAs {
				r := row{kind: rowVA, account: i, va: v}
				if focus(r) {
					cursorLine = len(lines)
				}
				lines = append(lines, "    "+line(r, checkbox(m.picked[i][v])+va.Label))
			}
		}
	}

	lines = m.window(lines, cursorLine)
	var b strings.Builder
	b.WriteString(m.fit(m.header("")))
	b.WriteString("\n" + m.fit(faint.Render("↑/↓ move · space toggle · ←/→ virtual account mode · enter done")))
	for _, l := range lines {
		b.WriteString("\n" + m.fit(l))
	}
	if m.problem != "" {
		b.WriteString("\n" + m.fit(accent.Render("! "+m.problem)))
	}
	return tea.NewView(b.String())
}

// window keeps the lines around the cursor when the form is taller than the terminal.
func (m *accountsModel) window(lines []string, cursor int) []string {
	room := m.height - 4
	if m.height <= 0 || len(lines) <= room || room < 3 {
		return lines
	}
	start := max(0, min(cursor-room/2, len(lines)-room))
	out := lines[start : start+room]
	if start > 0 {
		out[0] = faint.Render(fmt.Sprintf("  ↑ %d more", start+1))
	}
	if start+room < len(lines) {
		out[len(out)-1] = faint.Render(fmt.Sprintf("  ↓ %d more", len(lines)-start-room+1))
	}
	return out
}

// Accounts shows one form for every account and returns what each was set to, in order. Enter is
// refused while any account would be notified of nothing.
func Accounts(t Terminal, label string, accounts []Account) ([]AccountAnswer, error) {
	for i, a := range accounts {
		if len(a.Modes) == 0 || a.Mode < 0 || a.Mode >= len(a.Modes) {
			return nil, fmt.Errorf("account %d (%s) offers no virtual account mode", i, a.Title)
		}
	}
	m, err := run(t, newAccounts(label, accounts))
	if err != nil {
		return nil, err
	}
	out := make([]AccountAnswer, len(m.accounts))
	for i, a := range m.accounts {
		out[i] = AccountAnswer{Primary: a.Primary, Mode: a.Mode, Picked: m.checked(i)}
	}
	return out, nil
}
