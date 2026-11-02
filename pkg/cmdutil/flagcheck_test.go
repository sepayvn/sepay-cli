package cmdutil

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dateCmd(t *testing.T, value string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().String("from", "", "")
	require.NoError(t, cmd.Flags().Set("from", value))
	return cmd
}

func TestValidateDates(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string // the value left on the flag; "" means expect an error
	}{
		{"bare date", "2026-07-27", "2026-07-27"},
		{"timestamp", "2026-07-27 10:00:00", "2026-07-27 10:00:00"},
		{"t-separated", "2026-07-27T10:00:00", "2026-07-27T10:00:00"},
		{"utc offset rewritten to +07", "2026-07-27T00:00:00Z", "2026-07-27 07:00:00"},
		{"explicit +07 kept", "2026-07-27T09:30:00+07:00", "2026-07-27 09:30:00"},
		{"non-hour offset", "2026-07-27T00:00:00-05:30", "2026-07-27 12:30:00"},
		{"garbage", "hểtrt", ""},
		{"day-month order", "27-07-2026", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := dateCmd(t, tc.value)
			err := ValidateDates(cmd, "from")
			if tc.want == "" {
				require.Error(t, err)
				assert.Equal(t, ExitUsage, ExitCodeForError(err))
				return
			}
			require.NoError(t, err)
			got, err := cmd.Flags().GetString("from")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestValidateDays(t *testing.T) {
	for _, ok := range []string{"2026-07-27", "2026-07-27 143000"} {
		require.NoError(t, ValidateDays(dateCmd(t, ok), "from"), ok)
	}
	for _, bad := range []string{"2026-07-27 14:30:00", "2026-07-27T00:00:00Z", "nope"} {
		require.Error(t, ValidateDays(dateCmd(t, bad), "from"), bad)
	}
}

func TestValidateDates_EmptyIsUnset(t *testing.T) {
	require.NoError(t, ValidateDates(dateCmd(t, ""), "from"))
	require.NoError(t, ValidateDays(dateCmd(t, ""), "from"))
}

func TestValidatePaging(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"unset", nil, false},
		{"per-page at cap", []string{"--per-page", "100"}, false},
		{"per-page min", []string{"--per-page", "1"}, false},
		{"per-page over cap", []string{"--per-page", "101"}, true},
		{"per-page far over cap", []string{"--per-page", "500"}, true},
		{"per-page zero explicitly", []string{"--per-page", "0"}, true},
		{"page min", []string{"--page", "1"}, false},
		{"page zero", []string{"--page", "0"}, true},
		{"page negative", []string{"--page", "-2"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error { return nil }}
			var page, perPage int
			cmd.Flags().IntVar(&page, "page", 0, "")
			cmd.Flags().IntVar(&perPage, "per-page", 0, "")
			require.NoError(t, cmd.Flags().Parse(tt.args))

			err := ValidatePaging(cmd)
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, ExitUsage, ExitCodeForError(err))
				return
			}
			require.NoError(t, err)
		})
	}
}
