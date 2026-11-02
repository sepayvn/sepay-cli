package cmdutil

import (
	"fmt"
	"io"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"
)

// RenderOutput prints data as JSON/YAML for machine output, or calls renderTable.
func (f *Factory) RenderOutput(data any, renderTable func() error) error {
	switch f.Global.Output {
	case "json":
		return jsonfmt.RenderJSON(f.IOStreams.Out, data)
	case "yaml":
		return jsonfmt.RenderYAML(f.IOStreams.Out, data)
	default:
		return renderTable()
	}
}

// RenderTable styles the header, fits the table to the terminal and writes it to stdout.
func (f *Factory) RenderTable(t *jsonfmt.Table) error {
	t.HeaderColor = f.IOStreams.Cyan
	t.MaxWidth = f.IOStreams.TerminalWidth(f.IOStreams.Out)
	return t.Render(f.IOStreams.Out)
}

// Fprintf writes a human line to w, stripping terminal control sequences from string arguments.
// An escape in a server value could otherwise overwrite what the user reads.
func Fprintf(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, format, cleanArgs(a)...)
}

// Printf is Fprintf to stdout.
func (f *Factory) Printf(format string, a ...any) {
	Fprintf(f.IOStreams.Out, format, a...)
}

// ErrPrintf is Fprintf to stderr.
func (f *Factory) ErrPrintf(format string, a ...any) {
	Fprintf(f.IOStreams.ErrOut, format, a...)
}

func cleanArgs(a []any) []any {
	out := make([]any, len(a))
	for i, v := range a {
		if s, ok := v.(string); ok {
			out[i] = jsonfmt.Clean(s)
			continue
		}
		out[i] = v
	}
	return out
}

// YesNo renders a boolean as colored "yes"/"no".
func (f *Factory) YesNo(on bool) string {
	if on {
		return f.IOStreams.Green("yes")
	}
	return f.IOStreams.Dim("no")
}

// MaskSecret reduces a secret to an idempotent ****last4 hint, in every output format.
func MaskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return "****"
	}
	return "****" + s[len(s)-4:]
}

// SuccessIcon returns a check mark, green when color is on.
func (f *Factory) SuccessIcon() string { return f.IOStreams.Green("✓") }

// PrintPageInfo writes a pagination summary to stderr. Machine output stays quiet unless rows
// were left behind: a bare array would otherwise read as the whole set.
func (f *Factory) PrintPageInfo(p *api.Pagination, shown int) {
	if p == nil || p.Total == 0 {
		return
	}
	truncated := p.HasMore || p.CurrentPage < p.LastPage
	if !truncated && (f.Global.Quiet || f.Global.Output != "table") {
		return
	}
	line := fmt.Sprintf("page %d/%d · %d of %d", p.CurrentPage, p.LastPage, shown, p.Total)
	if truncated {
		line += " — more rows remain; page through with --page/--per-page"
	}
	fmt.Fprintln(f.IOStreams.ErrOut, line)
}
