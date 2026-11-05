package cmdutil

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRenamedSet() (*pflag.FlagSet, *string) {
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	v := new(string)
	fs.StringVar(v, "transaction-date-from", "", "old")
	RenameFlag(fs, "from", "transaction-date-from", "new")
	return fs, v
}

func TestRenameFlag_BothSpellingsWriteTheSameValue(t *testing.T) {
	for _, flag := range []string{"--from", "--transaction-date-from"} {
		t.Run(flag, func(t *testing.T) {
			fs, v := newRenamedSet()
			require.NoError(t, fs.Parse([]string{flag, "2026-01-02"}))
			assert.Equal(t, "2026-01-02", *v)
		})
	}
}

func TestRenameFlag_OldSpellingIsHidden(t *testing.T) {
	fs, _ := newRenamedSet()
	assert.True(t, fs.Lookup("transaction-date-from").Hidden)
	assert.Equal(t, []string{"from"}, fs.Lookup("transaction-date-from").Annotations[AnnAliasOf])
	assert.False(t, fs.Lookup("from").Hidden)
	assert.Equal(t, "string", fs.Lookup("from").Value.Type())
}

func TestRenameFlag_BothSpellingsIsAnError(t *testing.T) {
	fs, _ := newRenamedSet()
	err := fs.Parse([]string{"--from", "2026-01-02", "--transaction-date-from", "2026-01-03"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--from and --transaction-date-from are the same flag; pass one")
}

func TestRenameFlag_RepeatingOneSpellingIsAllowed(t *testing.T) {
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fs.StringSlice("sub-account", nil, "old")
	RenameFlag(fs, "va", "sub-account", "new")
	require.NoError(t, fs.Parse([]string{"--va", "a", "--va", "b"}))
	got, err := fs.GetStringSlice("sub-account")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, got)
}
