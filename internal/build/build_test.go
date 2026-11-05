package build

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFromBuildInfo(t *testing.T) {
	module := func(version, sum string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/sepayvn/sepay-cli", Version: version, Sum: sum}}
	}
	const sum = "h1:+gZE2jOdiscYByu0606Uw8Ldir2Cecd39Vq/3IEasRA="
	cases := []struct {
		name        string
		ldflags     string
		info        *debug.BuildInfo
		want        string
		goInstalled bool
	}{
		{"go install @v0.2.0", "dev", module("v0.2.0", sum), "v0.2.0", true},
		{"go install @main", "dev", module("v0.2.1-0.20261105010203-abcdef123456", sum), "v0.2.1-0.20261105010203-abcdef123456", true},
		{"release build keeps its ldflags", "0.2.0", module("v0.2.0", sum), "0.2.0", false},
		{"go build at a tag in a clone", "dev", module("v0.1.0", ""), "dev", false},
		{"go build of a dirty clone", "dev", module("v0.0.0-20261103073314-c4ba1caa26f1+dirty", ""), "dev", false},
		{"go build outside VCS", "dev", module("(devel)", ""), "dev", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, goInstalled := fromBuildInfo(tc.ldflags, tc.info)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.goInstalled, goInstalled)
		})
	}
}

func stamp(t *testing.T, version, commit, date string) {
	t.Helper()
	v, c, d := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = v, c, d })
	Version, Commit, Date = version, commit, date
}

func TestString(t *testing.T) {
	cases := []struct {
		name, version, commit, date, want string
	}{
		{"goreleaser", "1.2.3", "abc1234", "2026-11-03T04:05:06Z", "sepay version 1.2.3 (abc1234, 2026-11-03)"},
		{"go install drops the v", "v1.2.3", "none", "unknown", "sepay version 1.2.3"},
		{"commit without a date", "1.2.3", "abc1234", "unknown", "sepay version 1.2.3 (abc1234)"},
		{"dev", "dev", "none", "unknown", "sepay version dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stamp(t, tc.version, tc.commit, tc.date)
			assert.Equal(t, tc.want, String())
		})
	}
}

func TestReleaseURL(t *testing.T) {
	cases := []struct {
		version, want string
	}{
		{"1.2.3", "https://github.com/sepayvn/sepay-cli/releases/tag/v1.2.3"},
		{"v1.2.3", "https://github.com/sepayvn/sepay-cli/releases/tag/v1.2.3"},
		{"1.3.0-rc.1", "https://github.com/sepayvn/sepay-cli/releases/tag/v1.3.0-rc.1"},
		{"1.2.4-SNAPSHOT-abc1234", ""},
		{"v1.2.4-0.20261105010203-abcdef123456", ""},
		{"dev", ""},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			stamp(t, tc.version, "none", "unknown")
			assert.Equal(t, tc.want, ReleaseURL())
		})
	}
}
