// Package build holds version info injected via -ldflags.
package build

import (
	"fmt"
	"regexp"
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

// Number returns Version without the "v" that a go install build carries, so both builds print 0.1.0.
func Number() string {
	return strings.TrimPrefix(Version, "v")
}

// String renders the version banner, e.g. "sepay version 1.2.3 (abc1234, 2026-11-03)".
func String() string {
	var meta []string
	if Commit != "" && Commit != "none" {
		meta = append(meta, Commit)
	}
	if d := dateOnly(Date); d != "" {
		meta = append(meta, d)
	}
	if len(meta) == 0 {
		return "sepay version " + Number()
	}
	return fmt.Sprintf("sepay version %s (%s)", Number(), strings.Join(meta, ", "))
}

// ReleaseURL returns the GitHub release page of a tagged build, or "" when there is none.
func ReleaseURL() string {
	// The tags release.yml publishes: vX.Y.Z and -alpha.N, -beta.N, -rc.N. A snapshot
	// (0.1.1-SNAPSHOT-abc1234), a go install @main pseudo-version and "dev" have no release page.
	// Compiled here, not at init, so no other command pays for it at startup.
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(-(alpha|beta|rc)\.\d+)?$`).MatchString(Number()) {
		return ""
	}
	return "https://github.com/sepayvn/sepay-cli/releases/tag/v" + Number()
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
