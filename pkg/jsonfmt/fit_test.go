package jsonfmt

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
