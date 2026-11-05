package prompt

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func key(name string) tea.KeyPressMsg {
	switch name {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(name)
	return tea.KeyPressMsg{Code: r[0], Text: name}
}

// press feeds keys to m; a key named "type:abc" types each rune of abc.
func press[M tea.Model](t *testing.T, m M, keys ...string) M {
	t.Helper()
	for _, k := range keys {
		if text, ok := strings.CutPrefix(k, "type:"); ok {
			for _, r := range text {
				m = press(t, m, string(r))
			}
			continue
		}
		next, _ := m.Update(key(k))
		m = next.(M)
	}
	return m
}

func view(m tea.Model) string { return ansi.Strip(m.View().Content) }

var banks = []string{"BIDV 970418", "ACB 970416", "VPBank 970432"}

func newSelect(options ...string) *selectModel {
	return &selectModel{list: newList("Bank", options)}
}

func TestSelect_Keys(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want int
	}{
		{"enter takes the first", []string{"enter"}, 0},
		{"down moves", []string{"down", "down", "enter"}, 2},
		{"up wraps to the last", []string{"up", "enter"}, 2},
		{"filter is case-insensitive", []string{"type:vpBANK", "enter"}, 2},
		{"filter narrows then moves", []string{"type:97041", "down", "enter"}, 1},
		{"no match keeps every option", []string{"type:zzz", "down", "enter"}, 1},
		{"backspace widens the filter", []string{"type:acbx", "backspace", "enter"}, 1},
		{"backspace on an empty filter is a no-op", []string{"backspace", "down", "enter"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := press(t, newSelect(banks...), tt.keys...)
			require.True(t, m.done)
			assert.Equal(t, tt.want, m.chosen)
		})
	}
}

func TestSelect_DuplicateLabelsReturnTheIndex(t *testing.T) {
	m := press(t, newSelect("Main", "Main"), "down", "enter")
	assert.Equal(t, 1, m.chosen)
}

func TestSelect_LongListScrollsAPage(t *testing.T) {
	var opts []string
	for i := range 20 {
		opts = append(opts, "option "+string(rune('a'+i)))
	}
	m := newSelect(opts...)
	assert.Contains(t, view(m), "option g")
	assert.NotContains(t, view(m), "option h")

	m = press(t, m, "down", "down", "down", "down", "down", "down", "down")
	assert.Contains(t, view(m), "> option h")
	assert.NotContains(t, view(m), "option a")

	m = press(t, m, "up", "up", "up", "up", "up", "up", "up", "up")
	assert.Contains(t, view(m), "> option t", "up from the top wraps to the last")
}

func TestSelect_LeavesOneSummaryLine(t *testing.T) {
	m := press(t, newSelect(banks...), "down", "enter")
	assert.Equal(t, "? Bank › ACB 970416\n", view(m))
}

func TestSelect_TruncatesToTheTerminalWidth(t *testing.T) {
	m := newSelect(strings.Repeat("x", 100))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	for line := range strings.SplitSeq(view(next), "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(line), 40)
	}
}

func TestPrompts_InterruptKeys(t *testing.T) {
	for _, k := range []string{"ctrl+c", "esc"} {
		models := map[string]tea.Model{
			"select":       newSelect(banks...),
			"multi-select": &multiSelectModel{list: newList("Banks", banks), picked: map[int]bool{}},
			"input":        newInput("Name", "", textinput.EchoNormal),
			"confirm":      &confirmModel{question: question{label: "Delete?"}},
		}
		for name, m := range models {
			t.Run(name+" "+k, func(t *testing.T) {
				next, cmd := m.Update(key(k))
				q := next.(model).ended()
				assert.True(t, q.interrupted)
				assert.False(t, q.done)
				require.NotNil(t, cmd)
				assert.IsType(t, tea.QuitMsg{}, cmd())
			})
		}
	}
}

func TestMultiSelect_Keys(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want []int
	}{
		{"nothing picked", []string{"enter"}, []int{}},
		{"space toggles", []string{"space", "down", "down", "space", "enter"}, []int{0, 2}},
		{"space twice untoggles", []string{"space", "space", "enter"}, []int{}},
		{"right picks all", []string{"right", "enter"}, []int{0, 1, 2}},
		{"left clears all", []string{"right", "left", "enter"}, []int{}},
		{"right picks only the filtered", []string{"type:97041", "right", "enter"}, []int{0, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := press(t, &multiSelectModel{list: newList("Banks", banks), picked: map[int]bool{}}, tt.keys...)
			require.True(t, m.done)
			assert.Equal(t, tt.want, m.chosen())
		})
	}
}

func TestMultiSelect_SummaryListsPicks(t *testing.T) {
	m := press(t, &multiSelectModel{list: newList("Banks", banks), picked: map[int]bool{}}, "right", "enter")
	assert.Equal(t, "? Banks › BIDV 970418, ACB 970416, VPBank 970432\n", view(m))
}

func TestInput_TypedAndDefault(t *testing.T) {
	m := press(t, newInput("Name", "shop", textinput.EchoNormal), "type:my shop", "enter")
	assert.Equal(t, "my shop", m.value)
	assert.Equal(t, "? Name › my shop\n", view(m))

	m = press(t, newInput("Name", "shop", textinput.EchoNormal), "enter")
	assert.Empty(t, m.value, "the caller substitutes its own default")
	assert.Equal(t, "? Name › shop\n", view(m))
}

func TestPassword_NeverShowsTheValue(t *testing.T) {
	m := newInput("Shared secret", "", textinput.EchoPassword)
	m = press(t, m, "type:hunter2")
	assert.NotContains(t, view(m), "hunter2")

	m = press(t, m, "enter")
	assert.Equal(t, "hunter2", m.value)
	assert.Equal(t, "? Shared secret\n", view(m), "neither the value nor its length")
}

func TestConfirm_Keys(t *testing.T) {
	tests := []struct {
		name string
		def  bool
		key  string
		want bool
	}{
		{"enter takes default no", false, "enter", false},
		{"enter takes default yes", true, "enter", true},
		{"y", false, "y", true},
		{"Y", false, "Y", true},
		{"n", true, "n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := press(t, &confirmModel{question: question{label: "Delete?"}, def: tt.def}, tt.key)
			require.True(t, m.done)
			assert.Equal(t, tt.want, m.answer)
		})
	}
}

func TestConfirm_ShowsTheDefault(t *testing.T) {
	assert.Contains(t, view(&confirmModel{question: question{label: "Go?"}}), "[y/N]")
	assert.Contains(t, view(&confirmModel{question: question{label: "Go?"}, def: true}), "[Y/n]")
	m := press(t, &confirmModel{question: question{label: "Go?"}}, "x")
	assert.False(t, m.done, "other keys are ignored")
}

// --no-color and NO_COLOR draw without color even where the terminal would show it.
func TestTerminal_NoColorDrawsWithoutColor(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("TERM", "xterm-256color")
	for _, color := range []bool{true, false} {
		var out bytes.Buffer
		in, keys := io.Pipe()
		go func() {
			time.Sleep(100 * time.Millisecond)
			_, _ = keys.Write([]byte("\r"))
		}()
		opts := append(Terminal{In: in, Out: &out, Color: color}.options(), tea.WithWindowSize(80, 24))
		_, err := tea.NewProgram(newSelect(banks...), opts...).Run()
		require.NoError(t, err)
		require.Contains(t, out.String(), "BIDV 970418", "a frame was drawn")
		hasColor := regexp.MustCompile(`\x1b\[[0-9;]*3\d[;m]`).MatchString(out.String())
		assert.Equal(t, color, hasColor, "color=%v", color)
	}
}

func TestSelect_RefusesNoOptions(t *testing.T) {
	_, err := Select(Terminal{}, "Bank", nil, 0)
	require.ErrorIs(t, err, errNoOptions)
	_, err = MultiSelect(Terminal{}, "Bank", nil, nil)
	assert.ErrorIs(t, err, errNoOptions)
}

type panicModel struct{ question }

func (m *panicModel) Init() tea.Cmd                       { return nil }
func (m *panicModel) ended() *question                    { return &m.question }
func (m *panicModel) View() tea.View                      { return tea.NewView("") }
func (m *panicModel) Update(tea.Msg) (tea.Model, tea.Cmd) { panic("boom") }

func TestRun_APanicIsNotReportedAsAnInterrupt(t *testing.T) {
	_, err := run(Terminal{In: strings.NewReader("x"), Out: io.Discard}, &panicModel{})
	require.ErrorIs(t, err, tea.ErrProgramPanic)
	assert.NotErrorIs(t, err, ErrInterrupted)
}

func TestSelect_StartsOnTheDefault(t *testing.T) {
	m := newSelect(banks...)
	m.point(2)
	m = press(t, m, "enter")
	assert.Equal(t, 2, m.chosen)

	var opts []string
	for i := range 20 {
		opts = append(opts, "option "+string(rune('a'+i)))
	}
	long := newSelect(opts...)
	long.point(15)
	assert.Contains(t, view(long), "> option p", "a default past the first page is scrolled into view")
}

type quitModel struct{ question }

func (m *quitModel) Init() tea.Cmd                       { return tea.Quit }
func (m *quitModel) ended() *question                    { return &m.question }
func (m *quitModel) View() tea.View                      { return tea.NewView("") }
func (m *quitModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }

func TestRun_QuitWithoutAnAnswerIsAnInterrupt(t *testing.T) {
	// tea quits this way on SIGTERM; without the check the caller would read option 0 as chosen.
	_, err := run(Terminal{In: strings.NewReader(""), Out: io.Discard}, &quitModel{})
	require.ErrorIs(t, err, ErrInterrupted)
}

func TestRun_AFailedReadIsReportedNotTakenForAnInterrupt(t *testing.T) {
	_, err := Select(Terminal{In: iotest.ErrReader(errors.New("stdin closed")), Out: io.Discard}, "Bank", banks, 0)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrInterrupted)
	assert.ErrorContains(t, err, "stdin closed")
}
