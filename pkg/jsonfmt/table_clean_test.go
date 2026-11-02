package jsonfmt

import (
	"bytes"
	"strings"
	"testing"
)

// Render sanitizes every header and cell itself, so a caller must not wrap an
// AddRow value in Clean, and a bare AddRow is not unsafe.
func TestTableRender_CleansCells(t *testing.T) {
	poison := "ACME\x1b[2J\x1b[H\x1b]0;pwn\x07 Co"

	var raw bytes.Buffer
	tbl := NewTable("NAME", "X")
	tbl.AddRow(poison, "ok")
	if err := tbl.Render(&raw); err != nil {
		t.Fatal(err)
	}
	for _, seq := range []string{"\x1b[2J", "\x1b]0;", "\x07"} {
		if strings.Contains(raw.String(), seq) {
			t.Fatalf("Render leaked %q: %q", seq, raw.String())
		}
	}
	if !strings.Contains(raw.String(), "ACME") || !strings.Contains(raw.String(), "Co") {
		t.Fatalf("Render dropped visible text: %q", raw.String())
	}

	var precleaned bytes.Buffer
	tbl2 := NewTable("NAME", "X")
	tbl2.AddRow(Clean(poison), "ok")
	if err := tbl2.Render(&precleaned); err != nil {
		t.Fatal(err)
	}
	if raw.String() != precleaned.String() {
		t.Fatalf("Clean at the call site changed the table:\n raw:   %q\n clean: %q", raw.String(), precleaned.String())
	}
}
