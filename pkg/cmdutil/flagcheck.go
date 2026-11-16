package cmdutil

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

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

// ValidateDateOnly is ValidateDates for endpoints that take a bare date and nothing finer.
func ValidateDateOnly(cmd *cobra.Command, flags ...string) error {
	for _, flag := range flags {
		v, ok := changedString(cmd, flag)
		if !ok {
			continue
		}
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return &FlagError{Err: fmt.Errorf("--%s must be a date (YYYY-MM-DD)", flag)}
		}
	}
	return nil
}

// ValidateDateOrder rejects a date range whose start is after its end. A bare end date covers its
// whole day, so "--from 2026-11-05 --to 2026-11-05" is one day, not an empty range. Run it after
// ValidateDates or ValidateDays, which leave the values in a layout it reads.
func ValidateDateOrder(cmd *cobra.Command, fromFlag, toFlag string) error {
	from, fromOK := changedOrShared(cmd, fromFlag)
	to, toOK := changedOrShared(cmd, toFlag)
	if !fromOK || !toOK {
		return nil
	}
	layouts := append(slices.Clone(dateLayouts), dayLayouts...)
	start, _, err := parseDate(from, layouts)
	if err != nil {
		return nil
	}
	end, layout, err := parseDate(to, layouts)
	if err != nil {
		return nil
	}
	if layout == "2006-01-02" {
		end = end.Add(24*time.Hour - time.Nanosecond)
	}
	if start.After(end) {
		return &FlagError{Err: fmt.Errorf("--%s must not be after --%s", SpelledAs(cmd, fromFlag), SpelledAs(cmd, toFlag))}
	}
	return nil
}

// IncludeEndDay sends a bare end date as its last second. The API compares a datetime column with
// "<=", which reads "2026-10-20" as midnight and drops the rest of that day.
func IncludeEndDay(cmd *cobra.Command, flags ...string) error {
	for _, flag := range flags {
		v, ok := changedString(cmd, flag)
		if !ok {
			continue
		}
		if _, err := time.Parse("2006-01-02", v); err != nil {
			continue
		}
		if err := cmd.Flags().Set(flag, v+" 23:59:59"); err != nil {
			return err
		}
	}
	return nil
}

// ValidateAmountOrder rejects a min/max money filter whose min is above its max.
func ValidateAmountOrder(cmd *cobra.Command, minFlag, maxFlag string) error {
	lo, loOK := changedString(cmd, minFlag)
	hi, hiOK := changedString(cmd, maxFlag)
	if !loOK || !hiOK {
		return nil
	}
	l, lerr := strconv.ParseInt(lo, 10, 64)
	h, herr := strconv.ParseInt(hi, 10, 64)
	if lerr == nil && herr == nil && l > h {
		return &FlagError{Err: fmt.Errorf("--%s must not be greater than --%s", SpelledAs(cmd, minFlag), SpelledAs(cmd, maxFlag))}
	}
	return nil
}

// redirectScheme is RFC 3986's scheme syntax. A browser drops leading control characters before
// reading the scheme, so anything else in front of the colon could hide javascript: from the list.
var redirectScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*$`)

// ValidateRedirectURL accepts a URL a browser is sent back to: a scheme, no whitespace or control
// character, at most 2048 characters, and none of the schemes that run code in the page.
func ValidateRedirectURL(flag, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 2048 || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return &FlagError{Err: fmt.Errorf("--%s must be at most 2048 characters and contain no whitespace or control characters", flag)}
	}
	// host:port (localhost:3000/done) parses as scheme "localhost"; a digit right after the colon is
	// read as a port, which no browser or app redirect scheme starts with.
	scheme, rest, found := strings.Cut(value, ":")
	if !found || !redirectScheme.MatchString(scheme) || (rest != "" && rest[0] >= '0' && rest[0] <= '9') {
		return &FlagError{Err: fmt.Errorf("--%s must include a scheme, e.g. https://example.com/done, got %q", flag, value)}
	}
	blocked := []string{"javascript", "data", "vbscript", "file", "blob", "about"}
	if slices.Contains(blocked, strings.ToLower(scheme)) {
		return &FlagError{Err: fmt.Errorf("--%s scheme %q is not allowed", flag, scheme)}
	}
	return nil
}

// SpelledAs names a flag the way the user typed it: a hidden alias sharing its value
// (RenameFlag, AliasFlag) records which spelling was set.
func SpelledAs(cmd *cobra.Command, flag string) string {
	if fl := cmd.Flags().Lookup(flag); fl != nil {
		if v, ok := fl.Value.(*aliasedValue); ok && *v.setBy != "" {
			return *v.setBy
		}
	}
	return flag
}

// changedOrShared reads a flag that may also be set through a hidden alias sharing its value.
func changedOrShared(cmd *cobra.Command, flag string) (string, bool) {
	v, err := cmd.Flags().GetString(flag)
	if err != nil || v == "" {
		return "", false
	}
	return v, true
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
