// Package build holds version info injected via -ldflags.
package build

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Version, Commit and Date are set via -ldflags, so they must stay vars.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// FromGoInstall reports a binary built by "go install …@version" rather than by a release build.
var FromGoInstall bool

func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		Version, FromGoInstall = fromBuildInfo(Version, info)
	}
}

// fromBuildInfo takes the module version only when -ldflags set none and the module came from the
// module cache, which records a checksum. A build inside a clone records a VCS version (v0.1.0, a
// pseudo-version, "+dirty") and no checksum: it stays "dev", so it never asks to be upgraded.
func fromBuildInfo(version string, info *debug.BuildInfo) (string, bool) {
	if version != "dev" || info.Main.Sum == "" || info.Main.Version == "" {
		return version, false
	}
	return info.Main.Version, true
}

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
