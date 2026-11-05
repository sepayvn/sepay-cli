package prompt

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	modeAll  = Mode{Label: "All", Short: "all virtual accounts"}
	modeList = Mode{Label: "Only selected", Pick: true}
	modeNone = Mode{Label: "None", None: true}
)

func formAccounts() []Account {
	return []Account{
		{ // VA-only: account number fixed off
			Title: "MSB · 201", Note: "virtual accounts only", PrimaryFixed: true,
			Modes: []Mode{modeAll, modeList}, VAs: []Option{{Label: "VA A · SHOP A", Short: "VAA"}, {Label: "VA B", Short: "VAB"}},
		},
		{ // both: everything editable
			Title: "MSB · 202", Primary: true,
			Modes: []Mode{modeAll, modeList, modeNone}, VAs: []Option{{Label: "VA S", Short: "VAS"}},
		},
		{ // no VA: nothing to edit
			Title: "TPBank · 203", PrimaryFixed: true, Primary: true, Modes: []Mode{modeNone},
		},
	}
}

func TestAccounts_OnlyEditableLinesTakeTheCursor(t *testing.T) {
	m := newAccounts("Notify", formAccounts())
	assert.Equal(t, []row{
		{kind: rowMode, account: 0},
		{kind: rowPrimary, account: 1},
		{kind: rowMode, account: 1},
	}, m.rows(), "fixed account numbers and single modes are shown, not focused")

	out := view(m)
	assert.Contains(t, out, "> Virtual accounts  ‹ All ›")
	assert.Contains(t, out, "[x] Account number (the only option)")
	assert.NotContains(t, out, "VA A", "VAs show only under Only selected")
}

func TestAccounts_KeysEditInPlace(t *testing.T) {
	m := press(t, newAccounts("Notify", formAccounts()),
		"right",         // MSB 201 → Only selected; its VAs appear
		"down", "space", // check VA A
		"down", "down", "space", // MSB 202 account number off
		"down", "left", // MSB 202 mode All → None (wraps backwards)
		"left",          // → Only selected
		"down", "space", // check VA S
		"enter")
	require.True(t, m.done, m.problem)
	assert.Equal(t, "? MSB · 201 › virtual accounts VAA\n? MSB · 202 › virtual accounts VAS\n? TPBank · 203 › account number\n", view(m))

	ans := []AccountAnswer{}
	for i, a := range m.accounts {
		ans = append(ans, AccountAnswer{Primary: a.Primary, Mode: a.Mode, Picked: m.checked(i)})
	}
	assert.Equal(t, []AccountAnswer{
		{Primary: false, Mode: 1, Picked: []int{0}},
		{Primary: false, Mode: 1, Picked: []int{0}},
		{Primary: true, Mode: 0},
	}, ans)
}

func TestAccounts_EnterRefusesAnAccountThatNotifiesNothing(t *testing.T) {
	m := press(t, newAccounts("Notify", formAccounts()), "right", "enter")
	assert.False(t, m.done)
	assert.Equal(t, "MSB · 201: check at least one virtual account", m.problem)
	assert.Contains(t, view(m), "! MSB · 201: check at least one virtual account")

	m = press(t, newAccounts("Notify", formAccounts()), "down", "space", "down", "left", "enter")
	assert.False(t, m.done)
	assert.Equal(t, "MSB · 202: notifies nothing; turn on the account number or pick virtual accounts", m.problem)

	m = press(t, m, "up", "space", "enter")
	assert.True(t, m.done, "turning the account number back on fixes it")
}

func TestAccounts_DefaultsArePreChecked(t *testing.T) {
	accts := formAccounts()
	accts[0].Mode, accts[0].Picked = 1, []int{1}
	m := press(t, newAccounts("Notify", accts), "enter")
	require.True(t, m.done)
	assert.Contains(t, view(m), "? MSB · 201 › virtual accounts VAB")
}

func TestAccounts_ScrollsALongFormAroundTheCursor(t *testing.T) {
	var accts []Account
	for i := range 12 {
		accts = append(accts, Account{Title: "Bank · " + string(rune('a'+i)), Primary: true, Modes: []Mode{modeAll, modeNone}})
	}
	m := newAccounts("Notify", accts)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = next.(*accountsModel)
	assert.LessOrEqual(t, strings.Count(view(m), "\n")+1, 12)
	assert.Contains(t, view(m), "↓")

	for range 20 {
		m = press(t, m, "down")
	}
	assert.Contains(t, view(m), "> ", "the cursor line stays on screen")
	assert.Contains(t, view(m), "↑")
}

func TestAccounts_InterruptKeys(t *testing.T) {
	for _, k := range []string{"ctrl+c", "esc"} {
		m := press(t, newAccounts("Notify", formAccounts()), k)
		assert.True(t, m.interrupted)
	}
}

func TestAccounts_SummaryUsesTheShortModeName(t *testing.T) {
	m := press(t, newAccounts("Notify", formAccounts()), "enter")
	require.True(t, m.done)
	assert.Equal(t, "? MSB · 201 › all virtual accounts\n? MSB · 202 › account number + all virtual accounts\n? TPBank · 203 › account number\n", view(m))
}

func TestAccounts_EnterIsCheckedEvenWithNothingToEdit(t *testing.T) {
	nothing := []Account{{Title: "MSB · 9", PrimaryFixed: true, Modes: []Mode{modeNone}}}
	m := press(t, newAccounts("Notify", nothing), "enter")
	assert.False(t, m.done, "an account fixed to notify nothing is still refused")
	assert.Contains(t, m.problem, "notifies nothing")

	fixed := []Account{{Title: "TPBank · 3", PrimaryFixed: true, Primary: true, Modes: []Mode{modeNone}}}
	m = press(t, newAccounts("Notify", fixed), "enter")
	assert.True(t, m.done)
}
