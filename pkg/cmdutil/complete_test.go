package cmdutil

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlagEnum(t *testing.T) {
	tests := []struct {
		usage string
		want  []string
	}{
		{"Environment: sandbox|production (default sandbox)", []string{"sandbox", "production"}},
		{"PURCHASE|VERIFY", []string{"PURCHASE", "VERIFY"}},
		{"sms|api; sms only on plans that include it", []string{"sms", "api"}},
		{"individual|business_household|enterprise (required)", []string{"individual", "business_household", "enterprise"}},
		// Another flag's values inside parentheses are not this flag's.
		{"Bank account xid, repeat for multi (required for --bank-mode single|multi)", nil},
		{"Pay-code prefix (2-5 letters), same as the argument; use it when the prefix reads as switch syntax (on|off|yes|no|true|false)", nil},
		// A structured value is not a choice list.
		{"Per-account config xid=on|off:all|list|none (repeatable)", nil},
		{"Webhook name", nil},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, flagEnum(tt.usage), tt.usage)
	}
}

func TestCompleteEnumFlags_KeepsAnExistingCompletion(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	child := &cobra.Command{Use: "child", Run: func(*cobra.Command, []string) {}}
	root.AddCommand(child)
	root.PersistentFlags().String("env", "", "Environment: sandbox|production")
	child.Flags().String("mode", "", "Mode: a|b")
	child.Flags().String("kind", "", "Kind: x|y")
	require.NoError(t, child.RegisterFlagCompletionFunc("kind", cobra.FixedCompletions([]string{"custom"}, cobra.ShellCompDirectiveNoFileComp)))

	CompleteEnumFlags(root)

	got := func(c *cobra.Command, name string) []string {
		fn, ok := c.GetFlagCompletionFunc(name)
		require.True(t, ok, name)
		vals, _ := fn(c, nil, "")
		return vals
	}
	assert.Equal(t, []string{"sandbox", "production"}, got(root, "env"))
	assert.Equal(t, []string{"a", "b"}, got(child, "mode"))
	assert.Equal(t, []string{"custom"}, got(child, "kind"))
}
