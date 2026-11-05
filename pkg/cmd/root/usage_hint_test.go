package root

import (
	"context"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/build"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The hint names the command that failed, the way main resolves it from ExecuteContextC.
func TestUsageHint_NamesTheFailingCommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing arg", []string{"transaction", "get"}, "sepay transaction get"},
		{"extra arg", []string{"tx", "get", "a", "b"}, "sepay transaction get"},
		{"unknown flag", []string{"transaction", "list", "--bogus"}, "sepay transaction list"},
		{"flag error from RunE", []string{"transaction", "get", ""}, "sepay transaction get"},
		{"flag error from the root pre-run", []string{"transaction", "list", "-o", "bogus"}, "sepay transaction list"},
		{"unknown command", []string{"bogus"}, "sepay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
			ios, _, _, errOut := iostreams.Test()
			f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}, RawArgs: tc.args}
			root := NewCmdRoot(f)
			root.SetArgs(tc.args)

			cmd, err := root.ExecuteContextC(context.Background())
			require.Error(t, err)
			f.UsageCommand = cmd.CommandPath()

			assert.Equal(t, cmdutil.ExitUsage, cmdutil.HandleError(f, err))
			assert.Contains(t, errOut.String(), "\nRun '"+tc.want+" --help' for usage.\n")
		})
	}
}

// --map=json reports the version the way `sepay version --json` does: no "v" from a go install build.
func TestMap_JSON_VersionHasNoVPrefix(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	saved := build.Version
	build.Version = "v1.2.3"
	t.Cleanup(func() { build.Version = saved })

	args := []string{"--map=json"}
	ios, _, out, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}, RawArgs: args}
	root := NewCmdRoot(f)
	root.SetArgs(args)
	require.NoError(t, root.Execute())

	assert.Contains(t, out.String(), `"version": "1.2.3"`)
}
