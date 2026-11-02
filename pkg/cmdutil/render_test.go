package cmdutil

import (
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
)

// A --json list is a bare array with no envelope, so a truncated answer has to
// announce itself on stderr or a caller reads page 1 of N as the whole set.
func TestPrintPageInfo_SpeaksWhenTruncated(t *testing.T) {
	cases := []struct {
		name   string
		output string
		page   api.Pagination
		want   string
	}{
		{"table, complete", "table", api.Pagination{CurrentPage: 1, LastPage: 1, Total: 8}, "page 1/1 · 2 of 8"},
		{"table, truncated", "table", api.Pagination{CurrentPage: 1, LastPage: 4, Total: 8, HasMore: true}, "more rows remain"},
		{"json, complete stays silent", "json", api.Pagination{CurrentPage: 1, LastPage: 1, Total: 8}, ""},
		{"json, truncated speaks", "json", api.Pagination{CurrentPage: 1, LastPage: 4, Total: 8, HasMore: true}, "more rows remain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ios, _, _, errOut := iostreams.Test()
			f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: tc.output}}
			f.PrintPageInfo(&tc.page, 2)
			if tc.want == "" {
				assert.Empty(t, errOut.String())
				return
			}
			assert.Contains(t, errOut.String(), tc.want)
		})
	}
}

// --quiet drops the routine page line but must not hide an incomplete result.
func TestPrintPageInfo_QuietKeepsOnlyTheTruncationNotice(t *testing.T) {
	cases := []struct {
		name string
		page api.Pagination
		want string
	}{
		{"complete is dropped", api.Pagination{CurrentPage: 1, LastPage: 1, Total: 8}, ""},
		{"truncated still speaks", api.Pagination{CurrentPage: 1, LastPage: 4, Total: 8, HasMore: true}, "more rows remain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ios, _, _, errOut := iostreams.Test()
			f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: "table", Quiet: true}}
			f.PrintPageInfo(&tc.page, 2)
			if tc.want == "" {
				assert.Empty(t, errOut.String())
				return
			}
			assert.Contains(t, errOut.String(), tc.want)
		})
	}
}
