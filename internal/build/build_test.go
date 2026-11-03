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

func TestString_DropsTheModuleVersionPrefix(t *testing.T) {
	defer func(v, d string) { Version, Date = v, d }(Version, Date)
	Version, Date = "v0.2.0", "unknown"
	assert.Equal(t, "sepay-cli version 0.2.0", String())
}
