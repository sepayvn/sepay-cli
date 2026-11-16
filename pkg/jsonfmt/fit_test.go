package jsonfmt

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncate(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"fits", "abc", 3, "abc"},
		{"no limit", "abcdef", 0, "abcdef"},
		{"cut", "abcdef", 4, "abc…"},
		{"counts runes, not bytes", "Nguyễn Văn A", 7, "Nguyễn…"},
		{"colored cell resets after the cut", green("abcdef"), 4, "\x1b[32mabc…\x1b[0m"},
		{"width one", "abc", 1, "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Truncate(tc.in, tc.width))
		})
	}
}

func lines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

func wideTable() *Table {
	tbl := NewTable("ID", "SCOPES", "STATUS").Truncatable("SCOPES")
	tbl.AddRow("sess_1", "read,webhook:write,bank-account:write,va:write,refund:write,order:write", "active")
	tbl.AddRow("sess_2", "read", "revoked")
	return tbl
}

func TestTable_FitsMaxWidthByCuttingATruncatableColumn(t *testing.T) {
	tbl := wideTable()
	tbl.MaxWidth = 40
	var b bytes.Buffer
	_ = tbl.Render(&b)

	for _, l := range lines(b.String()) {
		assert.LessOrEqual(t, displayWidth(l), 40, "line %q overflows", l)
	}
	assert.Contains(t, b.String(), "sess_1", "a short column keeps its full value")
	assert.Contains(t, b.String(), "revoked")
	assert.Contains(t, b.String(), "…", "the long column is cut")
}

func TestTable_NeverCutsAnUndeclaredColumn(t *testing.T) {
	tbl := NewTable("ID", "NOTE").Truncatable("NOTE")
	tbl.AddRow("9f1c2e7a-4b1d-4a7e-9c3f-1a2b3c4d5e6f", "a fairly long free-text note")
	tbl.MaxWidth = 30
	var b bytes.Buffer
	_ = tbl.Render(&b)

	assert.Contains(t, b.String(), "9f1c2e7a-4b1d-4a7e-9c3f-1a2b3c4d5e6f")
	assert.Contains(t, b.String(), "a fai…", "the declared column stops at its floor")
}

func TestTable_ShrinksTheGapBeforeCutting(t *testing.T) {
	tbl := NewTable("A", "B")
	tbl.AddRow("aaaaaaaaaa", "bbbbbbbbbb")
	tbl.MaxWidth = 22
	var b bytes.Buffer
	_ = tbl.Render(&b)

	assert.Equal(t, "A           B\naaaaaaaaaa  bbbbbbbbbb\n", b.String())
}

func TestTable_NoMaxWidthKeepsEveryCharacter(t *testing.T) {
	var b bytes.Buffer
	_ = wideTable().Render(&b)

	assert.Contains(t, b.String(), "order:write")
	assert.NotContains(t, b.String(), "…")
	assert.Contains(t, lines(b.String())[0], "ID"+strings.Repeat(" ", 4+columnGap)+"SCOPES")
}

func droppableTable(width int) string {
	tbl := NewTable("REF", "TYPE", "NOTE", "BUYER", "TOTAL").Truncatable("NOTE", "BUYER").Droppable("TYPE", "BUYER")
	tbl.AddRow("084e179d-d95a-11f0-aef4-52c7e9b4f41b", "Increase adjustment", "a long free-text note", "Công ty TNHH A", "110.000")
	tbl.MaxWidth = width
	var b bytes.Buffer
	_ = tbl.Render(&b)
	return b.String()
}

func TestTable_DropsColumnsInTheirOrderOnlyWhenCuttingIsNotEnough(t *testing.T) {
	wide := droppableTable(0)
	assert.Contains(t, wide, "TYPE")
	assert.Contains(t, wide, "BUYER")

	cut := droppableTable(95)
	assert.Contains(t, cut, "TYPE", "cutting the free text is enough at 95")
	assert.Contains(t, cut, "BUYER")

	one := droppableTable(80)
	assert.NotContains(t, one, "TYPE")
	assert.NotContains(t, one, "Increase adjustment", "the cells go with the header")
	assert.Contains(t, one, "BUYER")

	two := droppableTable(60)
	assert.NotContains(t, two, "TYPE")
	assert.NotContains(t, two, "BUYER")
	assert.Contains(t, two, "084e179d-d95a-11f0-aef4-52c7e9b4f41b")
	assert.Contains(t, two, "110.000")
	for _, out := range []string{one, two} {
		for _, l := range lines(out) {
			assert.LessOrEqual(t, displayWidth(l), map[bool]int{true: 80, false: 60}[out == one], l)
		}
	}
}

func TestTable_StopsDroppingWhenNoDroppableColumnIsLeft(t *testing.T) {
	out := droppableTable(30)
	assert.Contains(t, out, "084e179d-d95a-11f0-aef4-52c7e9b4f41b", "an id is never cut, so the line overflows")
	assert.Contains(t, out, "NOTE")
}

func TestTable_CutsTheTextBeforeATail(t *testing.T) {
	render := func(width int, cell string) string {
		tbl := NewTable("#", "ITEM", "AMOUNT").Truncatable("ITEM").KeepsTail("ITEM")
		tbl.AddRow("1", cell, "200.000")
		tbl.MaxWidth = width
		var b bytes.Buffer
		_ = tbl.Render(&b)
		return b.String()
	}
	long := "Áo thun cotton cổ tròn tay ngắn" + TailSep + " (SP01) · promotion"

	assert.Contains(t, render(0, long), "Áo thun cotton cổ tròn tay ngắn (SP01) · promotion", "no cut: the tail joins the text")
	assert.Contains(t, render(45, long), "Áo thun cotto… (SP01) · promotion", "the text is cut, the tail stays whole")
	assert.NotContains(t, render(45, long), TailSep)
	assert.Contains(t, render(45, "Phí giao hàng nhanh trong ngày tại nội thành"), "…", "a cell without a tail is cut at its end")
}

func TestCutKeepingTail(t *testing.T) {
	assert.Equal(t, "abcd… (X)", cutKeepingTail("abcdefgh (X)", " (X)", 9))
	assert.Equal(t, "abcdefgh (X)", cutKeepingTail("abcdefgh (X)", " (X)", 12), "fits")
	assert.Equal(t, "abcd…", cutKeepingTail("abcdefgh (X)", " (X)", 5), "too narrow for the tail: cut at the end")
}

func TestTable_LeavesNoTrailingSpaces(t *testing.T) {
	tbl := NewTable("#", "ITEM", "QTY", "AMOUNT")
	tbl.AddRow("6", "Giao trong 3 ngày · note", "", "")
	var b bytes.Buffer
	_ = tbl.Render(&b)
	for _, l := range lines(b.String()) {
		assert.Equal(t, strings.TrimRight(l, " "), l)
	}
}

func TestTable_ATailColumnIsNeverCutNarrowerThanItsTail(t *testing.T) {
	tbl := NewTable("#", "ITEM", "AMOUNT").Truncatable("ITEM").KeepsTail("ITEM")
	tbl.AddRow("1", "Áo thun cotton cổ tròn tay ngắn"+TailSep+" (SP01) · promotion", "200.000")
	tbl.MaxWidth = 20
	var b bytes.Buffer
	_ = tbl.Render(&b)

	assert.Contains(t, b.String(), "… (SP01) · promotion", "the column stops where the tail still fits")
}

func TestTable_DroppingAColumnKeepsTheTailsOfTheOthers(t *testing.T) {
	tbl := NewTable("#", "TYPE", "ITEM").Truncatable("ITEM").KeepsTail("ITEM").Droppable("TYPE")
	tbl.AddRow("1", "Increase adjustment", "Áo thun cotton cổ tròn tay ngắn"+TailSep+" (SP01)")
	tbl.MaxWidth = 30
	var b bytes.Buffer
	_ = tbl.Render(&b)

	assert.NotContains(t, b.String(), "TYPE")
	assert.Contains(t, b.String(), "… (SP01)")
}

func TestTable_DropsAColumnARowLeftShort(t *testing.T) {
	tbl := NewTable("REF", "NOTE", "EXTRA").Droppable("EXTRA")
	tbl.AddRow("084e179d-d95a-11f0-aef4-52c7e9b4f41b", "note")
	tbl.AddRow("084e179d-d95a-11f0-aef4-52c7e9b4f41c", "note", "a very long extra column value")
	tbl.MaxWidth = 50
	var b bytes.Buffer

	require.NotPanics(t, func() { _ = tbl.Render(&b) })
	assert.NotContains(t, b.String(), "EXTRA")
}

func TestTable_ATailSepOutsideAKeepsTailColumnIsStripped(t *testing.T) {
	render := func(name string) string {
		tbl := NewTable("ID", "NAME", "URL").Truncatable("NAME", "URL")
		tbl.AddRow("12", name, "https://example.com/hook")
		tbl.MaxWidth = 60
		var b bytes.Buffer
		_ = tbl.Render(&b)
		return b.String()
	}
	long := "My webhook for orders of the shop number one hundred twenty three"

	forged := render("x" + TailSep + long)
	for _, l := range lines(forged) {
		assert.LessOrEqual(t, displayWidth(l), 60, "API data cannot widen a column with a tail: %q", l)
	}
	assert.NotContains(t, forged, TailSep)
	assert.Contains(t, forged, "xMy webhook for orders of the…", "the cell is cut at its end, as without the separator")
	assert.Contains(t, forged, "https://example.com/hook")
}

func TestTable_CleansTheTail(t *testing.T) {
	tbl := NewTable("#", "ITEM").Truncatable("ITEM").KeepsTail("ITEM")
	tbl.AddRow("1", "long webhook name here"+TailSep+"\x1b]0;pwn\x07ok")
	tbl.MaxWidth = 20
	var b bytes.Buffer
	_ = tbl.Render(&b)

	assert.Contains(t, b.String(), "…]0;pwnok", "the cut cell keeps its tail, cleaned")
	assert.NotContains(t, b.String(), "\x1b]")
	assert.NotContains(t, b.String(), "\x07")
}

func TestTable_ACellThatIsOnlyATailIsNotCut(t *testing.T) {
	tbl := NewTable("#", "ITEM", "AMOUNT").Truncatable("ITEM").KeepsTail("ITEM")
	tbl.AddRow("1", TailSep+" (SP01) · promotion", "200.000")
	tbl.MaxWidth = 14
	var b bytes.Buffer
	_ = tbl.Render(&b)

	assert.Contains(t, b.String(), "(SP01) · promotion")
}

func TestTable_TheLastTailSepStartsTheTail(t *testing.T) {
	tbl := NewTable("#", "ITEM").Truncatable("ITEM").KeepsTail("ITEM")
	tbl.AddRow("1", "Áo thun"+TailSep+"cotton cổ tròn tay ngắn size L"+TailSep+" (SP01)")
	tbl.MaxWidth = 25
	var b bytes.Buffer
	_ = tbl.Render(&b)

	assert.Contains(t, b.String(), "… (SP01)", "a separator left in the text is not the tail")
	assert.NotContains(t, b.String(), TailSep)
}

func TestTable_KeepsEveryColumnWhenTheCutTableFitsExactly(t *testing.T) {
	tbl := NewTable("A", "B", "C").Droppable("B")
	tbl.AddRow("aaaa", "bbbb", "cccc")
	tbl.MaxWidth = 16
	var b bytes.Buffer
	_ = tbl.Render(&b)

	assert.Equal(t, "A     B     C\naaaa  bbbb  cccc\n", b.String())
}

func TestTable_LeavesNoTrailingSpacesUnderAColoredEmptyHeader(t *testing.T) {
	tbl := NewTable("#", "EVENT", "").Truncatable("EVENT", "")
	tbl.HeaderColor = func(s string) string { return "\x1b[36m" + s + "\x1b[0m" }
	tbl.AddRow("#1", "transaction.created", "")
	var b bytes.Buffer
	_ = tbl.Render(&b)

	assert.Equal(t, "\x1b[36m#\x1b[0m         \x1b[36mEVENT\x1b[0m\n#1        transaction.created\n", b.String())
}

func TestTable_RendersARowWithFewerOrMoreCellsThanHeaders(t *testing.T) {
	tbl := NewTable("ID", "NAME").KeepsTail("NAME")
	tbl.AddRow("1")
	tbl.AddRow("2", "b", "extra")
	var b bytes.Buffer

	require.NoError(t, tbl.Render(&b))

	assert.Equal(t, "ID        NAME\n1\n2         b\n", b.String())
}
