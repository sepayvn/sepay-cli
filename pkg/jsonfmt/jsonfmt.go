// Package jsonfmt renders output as an aligned table or JSON/YAML.
package jsonfmt

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	columnGap = 8
	narrowGap = 2
)

// minColumn is the narrowest a truncated column gets.
const minColumn = 6

// ansiSeq matches only the SGR codes the CLI emits; reverse/conceal from API data must not pass.
var ansiSeq = regexp.MustCompile("\x1b\\[(?:0|2|31|32|36)(?:;(?:0|2|31|32|36))*m")

func displayWidth(s string) int {
	return utf8.RuneCountInString(ansiSeq.ReplaceAllString(s, ""))
}

// Truncate shortens s to width visible characters with "…", resetting any cut color. width <= 0 means no limit.
func Truncate(s string, width int) string {
	if width <= 0 || displayWidth(s) <= width {
		return s
	}
	var b strings.Builder
	colored := false
	for i, n := 0, 0; i < len(s) && n < width-1; {
		if loc := ansiSeq.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
			b.WriteString(s[i : i+loc[1]])
			colored = true
			i += loc[1]
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		b.WriteString(s[i : i+size])
		i += size
		n++
	}
	b.WriteString("…")
	if colored {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// Clean strips terminal control and invisible characters from an untrusted string, keeping CLI color.
func Clean(s string) string { return stripUnsafe(s) }

func stripUnsafe(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b { // ESC
			if loc := ansiSeq.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
				b.WriteString(s[i : i+loc[1]]) // keep an SGR color sequence
				i += loc[1]
				continue
			}
			i++ // drop any other escape (cursor/clear/OSC); its bytes are unsafe
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		// A raw byte such as a bare 0x9b C1 CSI never decodes to the C1 range below.
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || isInvisibleFormat(r) {
			i += size
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// isInvisibleFormat reports bidi, invisible, separator and tag characters. ZWJ/ZWNJ stay.
func isInvisibleFormat(r rune) bool {
	switch r {
	case 0x00ad, 0x061c, 0x180e, 0x200b, 0x200e, 0x200f, 0x2028, 0x2029, 0xfeff:
		return true
	}
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2060 && r <= 0x206f) || (r >= 0xe0000 && r <= 0xe007f)
}

// Table renders aligned header + rows.
type Table struct {
	headers []string
	rows    [][]string

	// HeaderColor styles header cells.
	HeaderColor func(string) string

	// MaxWidth is the width to fit; 0 means no limit.
	MaxWidth int

	cuttable map[string]bool // headers of the columns fit may cut
}

// NewTable creates a table with the given column headers.
func NewTable(headers ...string) *Table {
	return &Table{headers: headers}
}

// Truncatable names the free-text columns that may be cut to fit; ids, dates and amounts never are.
func (t *Table) Truncatable(headers ...string) *Table {
	if t.cuttable == nil {
		t.cuttable = make(map[string]bool, len(headers))
	}
	for _, h := range headers {
		t.cuttable[h] = true
	}
	return t
}

// AddRow appends one row of cells.
func (t *Table) AddRow(cells ...string) {
	t.rows = append(t.rows, cells)
}

// Render writes the table to w, aligned by visible width.
func (t *Table) Render(w io.Writer) error {
	widths := make([]int, len(t.headers))
	measure := func(cells []string) {
		for i := 0; i < len(widths) && i < len(cells); i++ {
			if d := displayWidth(cells[i]); d > widths[i] {
				widths[i] = d
			}
		}
	}
	// Machine formats bypass Render, so raw values survive there.
	clean := func(cells []string) []string {
		out := make([]string, len(cells))
		for i, c := range cells {
			out[i] = stripUnsafe(c)
		}
		return out
	}
	headers := clean(t.headers)
	rows := make([][]string, len(t.rows))
	for i, r := range t.rows {
		rows[i] = clean(r)
	}

	measure(headers)
	for _, row := range rows {
		measure(row)
	}
	gap := t.fit(widths)

	var b strings.Builder
	writeRow := func(cells []string, color func(string) string) {
		for i := range widths {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			cell = Truncate(cell, widths[i])
			pad := widths[i] - displayWidth(cell)
			if color != nil {
				cell = color(cell)
			}
			b.WriteString(cell)
			if i < len(widths)-1 {
				b.WriteString(strings.Repeat(" ", pad+gap))
			}
		}
		b.WriteByte('\n')
	}
	writeRow(headers, t.HeaderColor)
	for _, row := range rows {
		writeRow(row, nil)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// fit shrinks the gap, then the widest Truncatable column, and returns the gap.
// A table that still cannot fit wraps.
func (t *Table) fit(widths []int) int {
	total := func(gap int) int {
		sum := gap * (len(widths) - 1)
		for _, w := range widths {
			sum += w
		}
		return sum
	}
	if t.MaxWidth <= 0 || total(columnGap) <= t.MaxWidth {
		return columnGap
	}
	floors := make([]int, len(widths))
	for i, w := range widths {
		floors[i] = w
		if t.cuttable[t.headers[i]] {
			floors[i] = min(w, max(minColumn, displayWidth(t.headers[i])))
		}
	}
	for over := total(narrowGap) - t.MaxWidth; over > 0; over-- {
		widest := -1
		for i, w := range widths {
			if w > floors[i] && (widest < 0 || w > widths[widest]) {
				widest = i
			}
		}
		if widest < 0 {
			break
		}
		widths[widest]--
	}
	return narrowGap
}

// FormatMoney formats an amount like FormatVND, keeping two decimals when the fraction is non-zero.
func FormatMoney(f float64) string {
	whole := math.Trunc(f)
	cents := int64(math.Round(math.Abs(f-whole) * 100))
	if cents == 0 {
		return FormatVND(int64(whole))
	}
	return fmt.Sprintf("%s,%02d", FormatVND(int64(whole)), cents)
}

// FormatVND formats n with "." thousands separators, e.g. 5000000 -> "5.000.000".
func FormatVND(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	digits := strconv.FormatInt(n, 10)
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// OrDash returns s, or "-" when s is empty.
func OrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// RenderJSON writes v as indented JSON.
func RenderJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// URLs carry & and < >; escaping them to \u0026 serves HTML embedding, not a terminal or jq.
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// RenderYAML writes v as YAML; structs carry yaml tags mirroring json.
func RenderYAML(w io.Writer, v any) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	defer func() { _ = enc.Close() }()
	return enc.Encode(v)
}
