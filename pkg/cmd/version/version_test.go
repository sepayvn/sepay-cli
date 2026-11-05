package version

import (
	"encoding/json"
	"runtime"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/build"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stamp(t *testing.T, version, commit, date string) {
	t.Helper()
	v, c, d := build.Version, build.Commit, build.Date
	t.Cleanup(func() { build.Version, build.Commit, build.Date = v, c, d })
	build.Version, build.Commit, build.Date = version, commit, date
}

func run(t *testing.T, output string) string {
	t.Helper()
	ios, _, out, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: output}}
	cmd := NewCmdVersion(f)
	cmd.SetArgs(nil)
	require.NoError(t, cmd.Execute())
	return out.String()
}

func TestVersion_Text(t *testing.T) {
	cases := []struct {
		name, version, commit, date, want string
	}{
		{
			"goreleaser release", "0.1.0", "abc1234", "2026-11-03T04:05:06Z",
			"sepay version 0.1.0 (abc1234, 2026-11-03)\nhttps://github.com/sepayvn/sepay-cli/releases/tag/v0.1.0\n",
		},
		{
			"go install release", "v0.1.0", "none", "unknown",
			"sepay version 0.1.0\nhttps://github.com/sepayvn/sepay-cli/releases/tag/v0.1.0\n",
		},
		{
			"goreleaser snapshot", "0.1.1-SNAPSHOT-abc1234", "abc1234", "2026-11-03T04:05:06Z",
			"sepay version 0.1.1-SNAPSHOT-abc1234 (abc1234, 2026-11-03)\n",
		},
		{"dev", "dev", "none", "unknown", "sepay version dev\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stamp(t, tc.version, tc.commit, tc.date)
			assert.Equal(t, tc.want, run(t, "table"))
		})
	}
}

func TestVersion_JSONDropsTheVPrefixInBothBuilds(t *testing.T) {
	for _, v := range []string{"0.1.0", "v0.1.0"} {
		t.Run(v, func(t *testing.T) {
			stamp(t, v, "abc1234", "2026-11-03T04:05:06Z")
			var got map[string]string
			require.NoError(t, json.Unmarshal([]byte(run(t, "json")), &got))
			assert.Equal(t, map[string]string{
				"version":    "0.1.0",
				"commit":     "abc1234",
				"date":       "2026-11-03T04:05:06Z",
				"go_version": runtime.Version(),
				"os":         runtime.GOOS,
				"arch":       runtime.GOARCH,
			}, got)
		})
	}
}

func TestVersion_YAML(t *testing.T) {
	stamp(t, "v0.1.0", "abc1234", "2026-11-03T04:05:06Z")
	out := run(t, "yaml")
	assert.Contains(t, out, "version: 0.1.0\n")
	assert.Contains(t, out, "go_version: "+runtime.Version()+"\n")
}
