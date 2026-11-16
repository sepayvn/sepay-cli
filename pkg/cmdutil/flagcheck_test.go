package cmdutil

import (
	"fmt"
	"strings"
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

func TestValidateDateOnly(t *testing.T) {
	require.NoError(t, ValidateDateOnly(dateCmd(t, "2026-07-27"), "from"))
	for _, bad := range []string{"2026-07-27 143000", "2026-07-27 14:30:00", "2026-13-01", "2026-02-30", "27/07/2026", "nope"} {
		err := ValidateDateOnly(dateCmd(t, bad), "from")
		require.Error(t, err, bad)
		assert.Equal(t, "--from must be a date (YYYY-MM-DD)", err.Error(), bad)
	}
	require.NoError(t, ValidateDateOnly(dateCmd(t, ""), "from"), "empty is unset")
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

func dateRangeCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
	cmd.Flags().String("from", "", "")
	cmd.Flags().String("to", "", "")
	cmd.Flags().String("min", "", "")
	cmd.Flags().String("max", "", "")
	require.NoError(t, cmd.ParseFlags(args))
	return cmd
}

func TestValidateDateOrder(t *testing.T) {
	tests := []struct {
		args    []string
		wantErr bool
	}{
		{[]string{"--from", "2026-11-05", "--to", "2026-11-05"}, false},
		{[]string{"--from", "2026-11-05 18:00:00", "--to", "2026-11-05"}, false},
		{[]string{"--from", "2026-11-06", "--to", "2026-11-05"}, true},
		{[]string{"--from", "2026-11-05 10:00:00", "--to", "2026-11-05 09:59:59"}, true},
		{[]string{"--from", "2026-11-06 000000", "--to", "2026-11-05 235959"}, true},
		{[]string{"--from", "2026-11-06"}, false},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			err := ValidateDateOrder(dateRangeCmd(t, tt.args...), "from", "to")
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, "--from must not be after --to")
			assert.Equal(t, ExitUsage, ExitCodeForError(err))
		})
	}
}

func TestValidateAmountOrder(t *testing.T) {
	require.NoError(t, ValidateAmountOrder(dateRangeCmd(t, "--min", "100", "--max", "100"), "min", "max"))
	require.NoError(t, ValidateAmountOrder(dateRangeCmd(t, "--min", "100"), "min", "max"))
	err := ValidateAmountOrder(dateRangeCmd(t, "--min", "200", "--max", "100"), "min", "max")
	require.EqualError(t, err, "--min must not be greater than --max")
}

func TestValidateRedirectURL(t *testing.T) {
	require.NoError(t, ValidateRedirectURL("redirect-uri", "https://shop.test/done"))
	require.NoError(t, ValidateRedirectURL("redirect-uri", "myapp://linked"))
	require.EqualError(t, ValidateRedirectURL("redirect-uri", "javascript:alert(1)"), `--redirect-uri scheme "javascript" is not allowed`)
	require.EqualError(t, ValidateRedirectURL("redirect-uri", "shop.test/done"), `--redirect-uri must include a scheme, e.g. https://example.com/done, got "shop.test/done"`)
	require.EqualError(t, ValidateRedirectURL("redirect-uri", "localhost:3000/done"), `--redirect-uri must include a scheme, e.g. https://example.com/done, got "localhost:3000/done"`)
	require.NoError(t, ValidateRedirectURL("redirect-uri", "http://localhost:3000/done"))
}

func TestValidateRedirectURL_RefusesWhatABrowserWouldRunAnyway(t *testing.T) {
	for _, v := range []string{
		"\x01javascript:alert(1)",
		"java\tscript:alert(1)",
		"\u00a0javascript:alert(1)",
		"javascript\x00:alert(1)",
		" javascript:alert(1)",
		"1http://x.test",
		"ja va:x",
	} {
		t.Run(fmt.Sprintf("%q", v), func(t *testing.T) {
			err := ValidateRedirectURL("redirect-uri", v)
			require.Error(t, err)
			assert.Equal(t, ExitUsage, ExitCodeForError(err))
		})
	}
	require.NoError(t, ValidateRedirectURL("redirect-uri", "com.shop.app+v2://done"))
}

func TestValidateRedirectURL_RefusesAControlCharacterAfterTheScheme(t *testing.T) {
	for _, v := range []string{"https://shop.test/\x1b[2Jdone", "https://shop.test/\x07", "https://shop.test/\x7f"} {
		t.Run(fmt.Sprintf("%q", v), func(t *testing.T) {
			err := ValidateRedirectURL("redirect-uri", v)
			require.Error(t, err)
			assert.Equal(t, ExitUsage, ExitCodeForError(err))
		})
	}
}
