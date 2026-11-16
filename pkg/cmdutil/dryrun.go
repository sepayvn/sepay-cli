package cmdutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// AddDryRunFlag adds --dry-run to a production write. Only the api.Client from NewAPIClient
// refuses writes during a dry run; a command that writes through another client needs its own stop.
func AddDryRunFlag(cmd *cobra.Command, f *Factory) {
	cmd.Flags().BoolVar(&f.Global.DryRun, "dry-run", false, "Show what would change without sending the write (the reads it makes still run)")
}

// DryRunRequest is the write a dry run withholds; Path is relative to the API base and Query is
// the raw query string the request would carry.
type DryRunRequest struct {
	Method  string   `json:"method" yaml:"method"`
	Path    string   `json:"path" yaml:"path"`
	Query   string   `json:"query,omitempty" yaml:"query,omitempty"`
	Headers []string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Body    any      `json:"body" yaml:"body"`
}

// DryRun is what a dry run prints instead of the write's result. Before is the resource as get
// shows it (a replay's delivery preview), null for a create; After is Before with the fields the
// write sends, as sent, in get's field names and values, null for a delete. NotChecked holds a
// command's own lines; RenderDryRun puts DryRunNotChecked before them and fills WouldRefuse with
// the CLI's confirm-gate refusal, if any.
type DryRun struct {
	DryRun      bool          `json:"dry_run" yaml:"dry_run"`
	Request     DryRunRequest `json:"request" yaml:"request"`
	Before      any           `json:"before" yaml:"before"`
	After       any           `json:"after" yaml:"after"`
	NotChecked  []string      `json:"not_checked" yaml:"not_checked"`
	WouldRefuse []string      `json:"would_refuse" yaml:"would_refuse"`
}

// DryRunNotice opens a dry run's table output. --quiet keeps it, so a preview is never mistaken
// for the real run.
const DryRunNotice = "Dry run: nothing was sent."

// DryRunNotChecked is the first not_checked entry of every dry run: After is never a guess at what the
// server does with the values.
const DryRunNotChecked = "what the server normalises, derives or refuses; after the real run, get shows what it stored"

// RenderDryRun prints d as one object under -o json/yaml. In table mode it prints the notice, then
// table, then a "! not checked" line for each not_checked entry and a "! the real run would refuse"
// line for each refusal.
func (f *Factory) RenderDryRun(d DryRun, table func() error) error {
	d.DryRun = true
	d.NotChecked = append([]string{DryRunNotChecked}, d.NotChecked...)
	d.WouldRefuse = f.wouldRefuse()
	if f.Global.Output == "json" || f.Global.Output == "yaml" {
		return f.RenderOutput(d, nil)
	}
	fmt.Fprintln(f.IOStreams.ErrOut, DryRunNotice)
	if err := table(); err != nil {
		return err
	}
	for _, line := range d.NotChecked {
		Fprintf(f.IOStreams.ErrOut, "! not checked: %s\n", line)
	}
	f.printWouldRefuse(d.WouldRefuse)
	return nil
}

// RenderDryRunRequest prints the request of a raw `sepay api` dry run, as JSON in table mode too,
// like the response it stands in for; table mode writes each terminal control or invisible
// character as a \u escape, -o json keeps every byte.
func (f *Factory) RenderDryRunRequest(req DryRunRequest) error {
	out := struct {
		DryRun      bool          `json:"dry_run" yaml:"dry_run"`
		Request     DryRunRequest `json:"request" yaml:"request"`
		WouldRefuse []string      `json:"would_refuse" yaml:"would_refuse"`
	}{true, req, f.wouldRefuse()}
	if f.Global.Output == "json" || f.Global.Output == "yaml" {
		return f.RenderOutput(out, nil)
	}
	fmt.Fprintln(f.IOStreams.ErrOut, DryRunNotice)
	var buf bytes.Buffer
	if err := jsonfmt.RenderJSON(&buf, out); err != nil {
		return err
	}
	if _, err := io.WriteString(f.IOStreams.Out, jsonfmt.EscapeUnsafe(buf.String())); err != nil {
		return err
	}
	f.printWouldRefuse(out.WouldRefuse)
	return nil
}

// wouldRefuse is the confirm gates' refusal, if any, never nil.
func (f *Factory) wouldRefuse() []string {
	if f.realRunRefusal == "" {
		return []string{}
	}
	return []string{f.realRunRefusal}
}

func (f *Factory) printWouldRefuse(lines []string) {
	for _, line := range lines {
		Fprintf(f.IOStreams.ErrOut, "! the real run would refuse: %s\n", line)
	}
}

// RenderChanges prints a FIELD BEFORE AFTER table of the fields that differ, nested ones as dotted
// paths, so a key holding a dot would read as a nested one. Each path in replaced is a masked value
// the write sets: it is listed whatever both sides read. Values are never cut to the terminal width:
// a preview must show what the write sets in full.
func (f *Factory) RenderChanges(before, after any, replaced ...string) error {
	was, err := flatten(before)
	if err != nil {
		return err
	}
	now, err := flatten(after)
	if err != nil {
		return err
	}
	t := jsonfmt.NewTable("FIELD", "BEFORE", "AFTER")
	rows := 0
	paths := unionPaths(was, now)
	for _, p := range replaced {
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	for _, path := range paths {
		b, a := leafAt(was, path), leafAt(now, path)
		switch {
		case slices.Contains(replaced, path):
			a.text = "*** (new)"
		case a.raw == b.raw:
			continue
		}
		t.AddRow(path, b.text, a.text)
		rows++
	}
	if rows == 0 {
		fmt.Fprintln(f.IOStreams.ErrOut, "No field would change.")
		return nil
	}
	return f.RenderTable(t)
}

// RenderFields prints v as a FIELD VALUE table, nested fields as dotted paths, never cut to width.
func (f *Factory) RenderFields(v any) error {
	fields, err := flatten(v)
	if err != nil {
		return err
	}
	t := jsonfmt.NewTable("FIELD", "VALUE")
	for _, fv := range fields {
		t.AddRow(fv.path, fv.text)
	}
	return f.RenderTable(t)
}

// leaf is one flattened field: text is what the table shows, raw the JSON it came from, so two
// values that print alike ("" and null, 1 and "1") still compare as different.
type leaf struct{ path, text, raw string }

// flatten lists v's JSON leaves in key order: objects and arrays of objects become dotted and
// indexed paths, an array of scalars one comma-joined value, null and empty "-".
func flatten(v any) ([]leaf, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out []leaf
	if err := flattenJSON(raw, "", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func flattenJSON(raw json.RawMessage, path string, out *[]leaf) error {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) > 0 && raw[0] == '{':
		dec := json.NewDecoder(bytes.NewReader(raw))
		if _, err := dec.Token(); err != nil {
			return err
		}
		empty := true
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			var elem json.RawMessage
			if err := dec.Decode(&elem); err != nil {
				return err
			}
			if err := flattenJSON(elem, joinPath(path, fmt.Sprint(key)), out); err != nil {
				return err
			}
			empty = false
		}
		if empty && path != "" {
			*out = append(*out, leaf{path, "-", "{}"})
		}
		return nil
	case len(raw) > 0 && raw[0] == '[':
		var elems []json.RawMessage
		if err := json.Unmarshal(raw, &elems); err != nil {
			return err
		}
		if slices.ContainsFunc(elems, isComposite) {
			for i, elem := range elems {
				if err := flattenJSON(elem, fmt.Sprintf("%s[%d]", path, i), out); err != nil {
					return err
				}
			}
			return nil
		}
		texts := make([]string, len(elems))
		for i, elem := range elems {
			texts[i] = scalarText(elem)
		}
		text := strings.Join(texts, ", ")
		if text == "" {
			text = "-"
		}
		*out = append(*out, leaf{path, text, string(raw)})
		return nil
	default:
		*out = append(*out, leaf{path, scalarText(raw), string(raw)})
		return nil
	}
}

func isComposite(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && (raw[0] == '{' || raw[0] == '[')
}

func scalarText(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return string(raw)
	}
	switch s := v.(type) {
	case nil:
		return "-"
	case string:
		if s == "" {
			return `""`
		}
		return s
	default:
		return fmt.Sprint(s)
	}
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func unionPaths(lists ...[]leaf) []string {
	seen := map[string]bool{}
	var paths []string
	for _, l := range lists {
		for _, fv := range l {
			if !seen[fv.path] {
				seen[fv.path] = true
				paths = append(paths, fv.path)
			}
		}
	}
	return paths
}

// leafAt returns path's leaf, or "-" with no raw JSON when that side lacks it.
func leafAt(fields []leaf, path string) leaf {
	for _, fv := range fields {
		if fv.path == path {
			return fv
		}
	}
	return leaf{path: path, text: "-"}
}
