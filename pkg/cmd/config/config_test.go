package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
)

func testFactory(t *testing.T) (*cmdutil.Factory, *cobra.Command) {
	t.Helper()
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	f.Config = config.Load
	return f, NewCmdConfig(f)
}

func run(cmd *cobra.Command, args ...string) error {
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestConfigSet_PersistsAndReads(t *testing.T) {
	f, cmd := testFactory(t)
	f.Global.Yes = true // arming production is confirmed; see the test below
	require.NoError(t, run(cmd, "set", "default_env", "production"))
	require.NoError(t, run(cmd, "set", "sandbox.api_url", "http://localhost:8079/api/v2"))

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "production", cfg.Get("default_env"))
	assert.Equal(t, "http://localhost:8079/api/v2", cfg.Get("sandbox.api_url"))
}

func TestConfigSet_RejectsUnknownKeyAndBadEnv(t *testing.T) {
	_, cmd := testFactory(t)
	err := run(cmd, "set", "bogus_key", "x")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))

	err = run(cmd, "set", "default_env", "staging")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestConfigSet_ProductionDefaultNeedsConfirmation(t *testing.T) {
	_, cmd := testFactory(t)
	err := run(cmd, "set", "default_env", "production")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires confirmation")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.NotEqual(t, "production", cfg.Get("default_env"))
}

func TestConfigSet_SandboxDefaultIsNotConfirmed(t *testing.T) {
	_, cmd := testFactory(t)
	require.NoError(t, run(cmd, "set", "default_env", "sandbox"))
}

func TestConfigGet_SingleKey(t *testing.T) {
	f, cmd := testFactory(t)
	require.NoError(t, run(cmd, "set", "default_env", "sandbox"))

	ios, _, out, _ := iostreams.Test()
	f.IOStreams = ios
	require.NoError(t, run(NewCmdConfig(f), "get", "default_env"))
	assert.Equal(t, "sandbox\n", out.String())
}

func TestConfigGet_ListAliases(t *testing.T) {
	for _, name := range []string{"get", "list", "ls"} {
		t.Run(name, func(t *testing.T) {
			f, cmd := testFactory(t)
			require.NoError(t, run(cmd, "set", "default_env", "sandbox"))

			ios, _, out, _ := iostreams.Test()
			f.IOStreams = ios
			require.NoError(t, run(NewCmdConfig(f), name))
			assert.Contains(t, out.String(), "default_env")
			assert.Contains(t, out.String(), "sandbox")
		})
	}
}

func TestConfigGet_RejectsSecretKeysAndDoesNotLeakEnv(t *testing.T) {
	t.Setenv("SEPAY_TOKEN", "supersecrettoken")
	t.Setenv("SEPAY_REFRESH_TOKEN", "supersecretrefresh")

	for _, key := range []string{"token", "refresh_token", "bogus"} {
		f, _ := testFactory(t)
		ios, _, out, errOut := iostreams.Test()
		f.IOStreams = ios

		err := run(NewCmdConfig(f), "get", key)
		require.Error(t, err, "get %q must error", key)
		assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		assert.NotContains(t, out.String(), "supersecret")
		assert.NotContains(t, errOut.String(), "supersecret")
	}
}

func TestConfigGet_SingleKeyHonoursJSON(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	ios, _, out, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	f.Config = config.Load

	require.NoError(t, run(NewCmdConfig(f), "set", "default_env", "sandbox"))
	out.Reset()
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdConfig(f), "get", "default_env"))

	var got map[string]string
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), "output must be valid JSON: %q", out.String())
	assert.Equal(t, "sandbox", got["default_env"])
}

func TestConfigSet_MissingArgsIsUsageErrorWhenNotInteractive(t *testing.T) {
	_, cmd := testFactory(t)

	err := run(cmd, "set")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "sandbox.api_url")

	err = run(cmd, "set", "sandbox.api_url")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "a value for sandbox.api_url is required")

	err = run(cmd, "set", "bogus.key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown key "bogus.key"`)
}

func TestConfigSet_EmptyValueClearsSetting(t *testing.T) {
	_, cmd := testFactory(t)
	require.NoError(t, run(cmd, "set", "sandbox.api_url", "http://localhost:8079/api/v2"))
	require.NoError(t, run(cmd, "set", "sandbox.api_url", ""))

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.Get("sandbox.api_url"))
}
