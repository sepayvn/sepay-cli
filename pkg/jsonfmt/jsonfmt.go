// Package jsonfmt renders output as an aligned table or JSON/YAML.
package jsonfmt

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	columnGap = 8
	narrowGap = 2
)

// minColumn is the narrowest a truncated column gets.
const minColumn = 6

// ansiSeq matches only the SGR codes the CLI emits; reverse/conceal from API data must not pass.
var ansiSeq = regexp.MustCompile("\x1b\\[(?:0|2|31|32|33|36)(?:;(?:0|2|31|32|33|36))*m")

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
		if loc := ansiSeq.FindStringIndex(s[i:]); len(loc) == 2 && loc[0] == 0 {
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
			if loc := ansiSeq.FindStringIndex(s[i:]); len(loc) == 2 && loc[0] == 0 {
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

// EscapeUnsafe rewrites each character Clean would strip from JSON text as a \u escape, so a
// terminal shows that it is there without acting on it. Outside a string JSON text is ASCII and
// its only control is the newline Indent writes, which stays.
func EscapeUnsafe(jsonText string) string {
	var b strings.Builder
	b.Grow(len(jsonText))
	for _, r := range jsonText {
		if r == '\n' || (r >= 0x20 && r != 0x7f && (r < 0x80 || r > 0x9f) && !isInvisibleFormat(r)) {
			b.WriteRune(r)
			continue
		}
		for _, u := range utf16.Encode([]rune{r}) {
			fmt.Fprintf(&b, "\\u%04x", u)
		}
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

	cuttable  map[string]bool // headers of the columns fit may cut
	tailed    map[string]bool // headers of the columns whose cells may join a text and a tail
	droppable []string        // headers of the columns a too-wide table leaves out, first to go first
}

// TailSep joins the text of a cell in a KeepsTail column to a tail kept whole when the cell is
// cut, such as an item's code after its name: Render cuts the text before the tail. Clean the
// parts first, since cleaning the joined cell would strip the separator. In any other column a
// TailSep is an ordinary control character and is stripped, so API data cannot forge a tail.
const TailSep = "\x1f"

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

// KeepsTail names the Truncatable columns whose cells may join a text and a tail with TailSep.
func (t *Table) KeepsTail(headers ...string) *Table {
	if t.tailed == nil {
		t.tailed = make(map[string]bool, len(headers))
	}
	for _, h := range headers {
		t.tailed[h] = true
	}
	return t
}

// Droppable names the columns a table still wider than MaxWidth after cutting leaves out, in the
// order they go. Only a summary column belongs here: the full record stays in get and --json.
func (t *Table) Droppable(headers ...string) *Table {
	t.droppable = append(t.droppable, headers...)
	return t
}

// AddRow appends one row of cells.
func (t *Table) AddRow(cells ...string) {
	t.rows = append(t.rows, cells)
}

// Render writes the table to w, aligned by visible width.
func (t *Table) Render(w io.Writer) error {
	// Machine formats bypass Render, so raw values survive there.
	headers := make([]string, len(t.headers))
	for i, h := range t.headers {
		headers[i] = stripUnsafe(h)
	}
	rows := make([][]string, len(t.rows))
	tails := make([][]string, len(t.rows))
	for r, row := range t.rows {
		rows[r] = make([]string, len(row))
		tails[r] = make([]string, len(row))
		for i, c := range row {
			if i < len(t.headers) && t.tailed[t.headers[i]] {
				if cut := strings.LastIndex(c, TailSep); cut >= 0 {
					tails[r][i] = stripUnsafe(c[cut+len(TailSep):])
					c = c[:cut] + tails[r][i]
				}
			}
			rows[r][i] = stripUnsafe(c)
		}
	}

	for {
		widths, floors := t.measure(headers, rows, tails)
		drop := t.nextDrop(headers, widths, floors)
		if drop < 0 {
			return t.write(w, headers, rows, tails, widths, t.fit(widths, floors))
		}
		headers = removeColumn(headers, drop)
		for r := range rows {
			rows[r], tails[r] = removeColumn(rows[r], drop), removeColumn(tails[r], drop)
		}
	}
}

// measure returns each column's widest cell, and the width fit may cut it to: a Truncatable
// column stops at its header, minColumn, or what keeps its widest tail whole; any other at its width.
func (t *Table) measure(headers []string, rows, tails [][]string) (widths, floors []int) {
	widths = make([]int, len(headers))
	tailWidths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = displayWidth(h)
	}
	for r, row := range rows {
		for i := 0; i < len(widths) && i < len(row); i++ {
			widths[i] = max(widths[i], displayWidth(row[i]))
			if tails[r][i] != "" {
				tailWidths[i] = max(tailWidths[i], displayWidth(tails[r][i])+2)
			}
		}
	}
	floors = make([]int, len(widths))
	for i, w := range widths {
		floors[i] = w
		if t.cuttable[headers[i]] {
			floors[i] = min(w, max(minColumn, displayWidth(headers[i]), tailWidths[i]))
		}
	}
	return widths, floors
}

// nextDrop returns the column to leave out when even the cut table is wider than MaxWidth, or -1.
func (t *Table) nextDrop(headers []string, widths, floors []int) int {
	if t.MaxWidth <= 0 || len(t.droppable) == 0 {
		return -1
	}
	narrowest := narrowGap * (len(widths) - 1)
	for _, f := range floors {
		narrowest += f
	}
	if narrowest <= t.MaxWidth {
		return -1
	}
	for _, d := range t.droppable {
		if i := slices.Index(headers, d); i >= 0 {
			return i
		}
	}
	return -1
}

func removeColumn(cells []string, i int) []string {
	if i >= len(cells) {
		return cells
	}
	return append(cells[:i:i], cells[i+1:]...)
}

func (t *Table) write(w io.Writer, headers []string, rows, tails [][]string, widths []int, gap int) error {
	var b strings.Builder
	writeRow := func(cells, tails []string, color func(string) string) {
		var line strings.Builder
		for i := range widths {
			cell, tail := "", ""
			if i < len(cells) {
				cell = cells[i]
			}
			if i < len(tails) {
				tail = tails[i]
			}
			cell = cutKeepingTail(cell, tail, widths[i])
			pad := widths[i] - displayWidth(cell)
			// A colored empty cell would end the line in codes that hide its padding from the trim.
			if color != nil && cell != "" {
				cell = color(cell)
			}
			line.WriteString(cell)
			if i < len(widths)-1 {
				line.WriteString(strings.Repeat(" ", pad+gap))
			}
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	writeRow(headers, nil, t.HeaderColor)
	for r, row := range rows {
		writeRow(row, tails[r], nil)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// cutKeepingTail cuts cell to width, cutting the text before its tail when the tail fits.
func cutKeepingTail(cell, tail string, width int) string {
	if tail == "" || displayWidth(cell) <= width || width < displayWidth(tail)+2 {
		return Truncate(cell, width)
	}
	return Truncate(strings.TrimSuffix(cell, tail), width-displayWidth(tail)) + tail
}

// fit shrinks the gap, then the widest Truncatable column, and returns the gap.
// A table that still cannot fit overflows the line.
func (t *Table) fit(widths, floors []int) int {
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
