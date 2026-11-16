package jsonfmt

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func green(s string) string { return "\x1b[32m" + s + "\x1b[0m" }

func TestDisplayWidth_IgnoresANSI(t *testing.T) {
	assert.Equal(t, 3, displayWidth(green("yes")))
	assert.Equal(t, 3, displayWidth("yes"))
}

func TestTable_ColoredMiddleCellStaysAligned(t *testing.T) {
	plain := NewTable("A", "B", "C")
	plain.AddRow("x", "yes", "z")
	plain.AddRow("longer", "no", "zz")

	colored := NewTable("A", "B", "C")
	colored.AddRow("x", green("yes"), "z")
	colored.AddRow("longer", green("no"), "zz")

	var pb, cb bytes.Buffer
	_ = plain.Render(&pb)
	_ = colored.Render(&cb)

	assert.Equal(t, pb.String(), ansiSeq.ReplaceAllString(cb.String(), ""))
}

func TestTable_HeaderColorStylesHeaderOnly(t *testing.T) {
	tbl := NewTable("ID", "NAME")
	tbl.HeaderColor = func(s string) string { return "\x1b[36m" + s + "\x1b[0m" }
	tbl.AddRow("1", "alice")

	var b bytes.Buffer
	_ = tbl.Render(&b)
	out := b.String()
	assert.Contains(t, out, "\x1b[36mID\x1b[0m", "header cell is colored")
	assert.NotContains(t, out, "\x1b[36m1", "row cells are not colored")
}

func TestTableRender_StripsControlSequencesKeepsColorAndUTF8(t *testing.T) {
	tbl := NewTable("NAME", "NOTE")
	tbl.AddRow("Nguyễn\x1b[2J\x1b[1;1H\x07 A", "\x1b[32mok\x1b[0m")

	var b bytes.Buffer
	if err := tbl.Render(&b); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := b.String()

	assert.NotContains(t, out, "\x1b[2J", "screen-clear escape must be stripped")
	assert.NotContains(t, out, "\x1b[1;1H", "cursor-move escape must be stripped")
	assert.NotContains(t, out, "\x07", "bare control byte must be stripped")
	assert.Contains(t, out, "Nguyễn", "Vietnamese text preserved")
	assert.Contains(t, out, "\x1b[32mok\x1b[0m", "CLI SGR color preserved")
}

func TestStripUnsafe_DropsRawC1AndInvalidBytes(t *testing.T) {
	tbl := NewTable("V")
	tbl.AddRow("A\x9bB\x80C") // raw 0x9b (C1 CSI introducer) + lone 0x80
	var b bytes.Buffer
	require.NoError(t, tbl.Render(&b))
	out := b.String()
	assert.NotContains(t, out, "\x9b", "raw C1 introducer must be dropped")
	assert.NotContains(t, out, "\x80", "lone invalid byte must be dropped")
	assert.Contains(t, out, "ABC", "printable text preserved")
}

func TestRenderYAML_ShapeAndEmptyCollections(t *testing.T) {
	type row struct {
		ID     string `json:"id" yaml:"id"`
		Amount int64  `json:"amount" yaml:"amount"`
	}

	var buf bytes.Buffer
	require.NoError(t, RenderYAML(&buf, []row{{ID: "tx_1", Amount: 5000000}}))
	assert.Contains(t, buf.String(), "id: tx_1")
	assert.Contains(t, buf.String(), "amount: 5000000")

	buf.Reset()
	require.NoError(t, RenderYAML(&buf, []row{}))
	assert.Equal(t, "[]\n", buf.String(), "an empty list must still emit a document")

	buf.Reset()
	require.NoError(t, RenderYAML(&buf, []row{{ID: "\x1b[31mred\x1b[0m"}}))
	assert.NotContains(t, buf.String(), "\x1b[31m")
}

func TestStripUnsafe_KeepsOnlyTheSGRCodesTheCLIEmits(t *testing.T) {
	cases := []struct {
		name, cell, want string
	}{
		{"conceal", "hook-one\x1b[8m", "hook-one[8m"},
		{"reverse video", "hook-one\x1b[7m", "hook-one[7m"},
		{"blink", "hook-one\x1b[5m", "hook-one[5m"},
		{"bold red", "hook\x1b[1;31mX", "hook[1;31mX"},
		{"256-color", "hook\x1b[38;5;196mX", "hook[38;5;196mX"},
		{"CLI red", "\x1b[31mfailed\x1b[0m", "\x1b[31mfailed\x1b[0m"},
		{"CLI green", "\x1b[32mok\x1b[0m", "\x1b[32mok\x1b[0m"},
		{"CLI yellow", "\x1b[33mdraft\x1b[0m", "\x1b[33mdraft\x1b[0m"},
		{"CLI yellow after a reset", "\x1b[0;33mdraft\x1b[0m", "\x1b[0;33mdraft\x1b[0m"},
		{"CLI dim", "\x1b[2mmeta\x1b[0m", "\x1b[2mmeta\x1b[0m"},
		{"CLI cyan", "\x1b[36mID\x1b[0m", "\x1b[36mID\x1b[0m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Clean(tc.cell))
		})
	}
}

func TestDisplayWidth_CountsAStrippedSGRAsVisibleText(t *testing.T) {
	assert.Equal(t, len("hook-one[8m"), displayWidth(Clean("hook-one\x1b[8m")))
	assert.Equal(t, len("ok"), displayWidth("\x1b[32mok\x1b[0m"))
}

func TestClean_DropsBidiAndInvisibleFormatKeepsJoinersAndVietnamese(t *testing.T) {
	for _, r := range []rune{
		0x061c, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069,
		0x200b, 0x2060, 0x2061, 0x2064, 0x206a, 0x206f, 0xfeff, 0x00ad, 0x180e, 0x2028, 0x2029, 0xe0000, 0xe0041, 0xe007f,
	} {
		assert.Equal(t, "ab", Clean("a"+string(r)+"b"), "U+%04X", r)
	}
	assert.Equal(t, "a\u200db\u200cc", Clean("a\u200db\u200cc"), "ZWJ and ZWNJ survive")
	assert.Equal(t, "Nguyễn Văn Ạ chuyển khoản", Clean("Nguyễn Văn Ạ chuyển khoản"))
}

func TestRenderJSON_KeepsURLCharacters(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RenderJSON(&buf, map[string]string{"url": "https://x.example/?a=1&b=<2>"}))
	assert.Equal(t, "{\n  \"url\": \"https://x.example/?a=1&b=<2>\"\n}\n", buf.String()) //nolint:testifylint // the exact bytes are the assertion: indent and no HTML escaping
}
