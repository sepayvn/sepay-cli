package cmdutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathSegment(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "%2e%2e", "%2E.", "%2e", "a/b", "a%2Fb", "/", `a\b`, "a%5Cb"} {
		_, err := PathSegment("xid", bad)
		require.Error(t, err, "%q", bad)
		assert.Equal(t, ExitUsage, ExitCodeForError(err), "%q", bad)
	}
	for in, want := range map[string]string{
		"11111111-2222-4333-8444-555555555555": "11111111-2222-4333-8444-555555555555",
		"INV_20261031_609":                     "INV_20261031_609",
		"a b":                                  "a%20b",
		"..x":                                  "..x",
		"%zz":                                  "%25zz",
	} {
		got, err := PathSegment("xid", in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
}
