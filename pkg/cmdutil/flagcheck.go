package cmdutil

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var dateLayouts = []string{"2006-01-02", "2006-01-02 15:04:05", "2006-01-02T15:04:05", time.RFC3339}

// dayLayouts are what the bankhub/pg builders accept; anything else is silently ignored there.
var dayLayouts = []string{"2006-01-02", "2006-01-02 150405"}

// serverWallClock is +07: the API compares naive date filters in Vietnam local time.
var serverWallClock = time.FixedZone("ICT", 7*60*60)

const sendLayout = "2006-01-02 15:04:05"

// ValidateChoice rejects a changed flag whose value is outside the allowed set.
func ValidateChoice(cmd *cobra.Command, flag string, allowed []string) error {
	v, ok := changedString(cmd, flag)
	if !ok {
		return nil
	}
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return &FlagError{Err: fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))}
}

// ValidateChoices applies ValidateChoice to each flag/allowed-set pair.
func ValidateChoices(cmd *cobra.Command, enums map[string][]string) error {
	for flag, allowed := range enums {
		if err := ValidateChoice(cmd, flag, allowed); err != nil {
			return err
		}
	}
	return nil
}

// ValidateAmounts rejects a money filter that is not a non-negative integer; the API ignores malformed ones.
func ValidateAmounts(cmd *cobra.Command, flags ...string) error {
	for _, flag := range flags {
		v, ok := changedString(cmd, flag)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return &FlagError{Err: fmt.Errorf("--%s must be a whole number of VND (0 or more)", flag)}
		}
	}
	return nil
}

// ValidateDates rejects a date filter matching none of dateLayouts; the API ignores malformed ones.
func ValidateDates(cmd *cobra.Command, flags ...string) error {
	for _, flag := range flags {
		v, ok := changedString(cmd, flag)
		if !ok {
			continue
		}
		t, layout, err := parseDate(v, dateLayouts)
		if err != nil {
			return &FlagError{Err: fmt.Errorf("--%s must be a date (YYYY-MM-DD), a timestamp (YYYY-MM-DD HH:MM:SS) or RFC3339", flag)}
		}
		// The server drops an RFC3339 offset, so convert the instant to server wall time.
		if layout == time.RFC3339 {
			if err := cmd.Flags().Set(flag, t.In(serverWallClock).Format(sendLayout)); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateDays is ValidateDates for endpoints that accept only a bare date or colon-less timestamp.
func ValidateDays(cmd *cobra.Command, flags ...string) error {
	for _, flag := range flags {
		v, ok := changedString(cmd, flag)
		if !ok {
			continue
		}
		if _, _, err := parseDate(v, dayLayouts); err != nil {
			return &FlagError{Err: fmt.Errorf("--%s must be a date (YYYY-MM-DD) or a date with a colon-less time (YYYY-MM-DD HHMMSS)", flag)}
		}
	}
	return nil
}

// MaxPerPage is the largest page size; above it the API silently falls back to 20.
const MaxPerPage = 100

// ValidatePaging rejects an out-of-range --page or --per-page.
func ValidatePaging(cmd *cobra.Command) error {
	if n, ok := changedInt(cmd, "per-page"); ok && (n < 1 || n > MaxPerPage) {
		return &FlagError{Err: fmt.Errorf("--per-page must be between 1 and %d", MaxPerPage)}
	}
	if n, ok := changedInt(cmd, "page"); ok && n < 1 {
		return &FlagError{Err: errors.New("--page must be 1 or more")}
	}
	return nil
}

func changedInt(cmd *cobra.Command, flag string) (int, bool) {
	fs := cmd.Flags()
	if !fs.Changed(flag) {
		return 0, false
	}
	v, err := fs.GetInt(flag)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseDate(value string, layouts []string) (time.Time, string, error) {
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, layout, nil
		}
	}
	return time.Time{}, "", fmt.Errorf("unrecognized date %q", value)
}

// changedString treats an empty value as unset, matching BindListFlags.
func changedString(cmd *cobra.Command, flag string) (string, bool) {
	fs := cmd.Flags()
	if !fs.Changed(flag) {
		return "", false
	}
	v, err := fs.GetString(flag)
	if err != nil || v == "" {
		return "", false
	}
	return v, true
}
