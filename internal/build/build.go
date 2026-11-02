// Package build holds version info injected via -ldflags.
package build

import (
	"fmt"
	"strings"
)

// Version, Commit and Date are set via -ldflags, so they must stay vars.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String renders the version banner, e.g. "sepay-cli version 0.1.0 (2026-07-09)".
func String() string {
	v := strings.TrimPrefix(Version, "v")
	var date string
	if d := dateOnly(Date); d != "" {
		date = fmt.Sprintf(" (%s)", d)
	}
	return fmt.Sprintf("sepay-cli version %s%s", v, date)
}

func dateOnly(s string) string {
	if s == "" || s == "unknown" {
		return ""
	}
	if i := strings.IndexByte(s, 'T'); i > 0 {
		return s[:i]
	}
	return s
}
