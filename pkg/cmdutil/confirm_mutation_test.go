package cmdutil_test

import (
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const webhookXID = "11111111-2222-4333-8444-555555555555"

// mutationFactory is non-interactive; defaultEnv, when set, is the stored default_env.
func mutationFactory(t *testing.T, global cmdutil.GlobalOptions, defaultEnv string) *cmdutil.Factory {
	t.Helper()
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &global}
	f.Config = func() (*config.Config, error) {
		cfg, err := config.Load()
		if err == nil && defaultEnv != "" {
			cfg.Set("default_env", defaultEnv)
		}
		return cfg, err
	}
	return f
}

func TestConfirmMutation_Unattended(t *testing.T) {
	const ownTerminal = "ask the user to run this command in their own terminal"
	cases := []struct {
		name        string
		global      cmdutil.GlobalOptions
		defaultEnv  string
		target      string
		destructive bool
		wantErr     string // "" = proceeds
	}{
		{"production write naming its resource, with --yes", cmdutil.GlobalOptions{Env: config.EnvProduction, Yes: true}, "", webhookXID, true, ""},
		{"production write without --yes", cmdutil.GlobalOptions{Env: config.EnvProduction}, "", webhookXID, true, ownTerminal},
		{"production create with --yes names no resource", cmdutil.GlobalOptions{Env: config.EnvProduction, Yes: true}, "", "", false, "names no single resource"},
		{"production write naming only whitespace, with --yes", cmdutil.GlobalOptions{Env: config.EnvProduction, Yes: true}, "", " \t", false, "names no single resource"},
		{"production create without --yes", cmdutil.GlobalOptions{Env: config.EnvProduction}, "", "", false, ownTerminal},
		{"default_env production does not count as --env", cmdutil.GlobalOptions{Yes: true}, config.EnvProduction, webhookXID, false, "default_env does not count"},
		{"production host behind a sandbox label", cmdutil.GlobalOptions{Env: config.EnvSandbox, Yes: true, APIBase: "https://cli-api.sepay.vn/v2"}, "", webhookXID, false, `through the API host "cli-api.sepay.vn", which is not the sandbox host; ` + ownTerminal},
		{"sandbox create is silent", cmdutil.GlobalOptions{Env: config.EnvSandbox}, "", "", false, ""},
		{"sandbox delete needs confirm", cmdutil.GlobalOptions{Env: config.EnvSandbox}, "", webhookXID, true, "pass --yes"},
		{"sandbox delete with --yes", cmdutil.GlobalOptions{Env: config.EnvSandbox, Yes: true}, "", webhookXID, true, ""},
		{"sandbox create with --yes and no resource", cmdutil.GlobalOptions{Env: config.EnvSandbox, Yes: true}, "", "", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := mutationFactory(t, tc.global, tc.defaultEnv)
			err := f.ConfirmMutation("delete webhook", tc.target, tc.destructive)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), tc.wantErr)
			if tc.global.Env == config.EnvProduction || tc.defaultEnv == config.EnvProduction || tc.global.APIBase != "" {
				assert.NotContains(t, err.Error(), "pass --yes", "a production refusal must not teach an agent to add --yes")
				assert.NotContains(t, err.Error(), "add --env", "nor to add --env production")
			}
		})
	}
}

func TestConfirmMutation_ProductionOnATerminal(t *testing.T) {
	cases := []struct {
		name   string
		target string
		answer bool
		wantOK bool
	}{
		{"y runs a write naming its resource", webhookXID, true, true},
		{"n cancels", webhookXID, false, false},
		{"y runs a create, which names no resource", "", true, true},
		{"n cancels a create", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := mutationFactory(t, cmdutil.GlobalOptions{Env: config.EnvProduction}, "")
			p := prompttest.Use(t, f, prompttest.Step{Label: "create webhook on production?", Answer: tc.answer})
			err := f.ConfirmMutation("create webhook", tc.target, false)
			assert.Equal(t, false, p.Defaults["create webhook on production?"], "Enter must mean no")
			if tc.wantOK {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cancelled")
		})
	}
}

// --yes is the confirmation, so no prompt opens behind it even on a terminal.
func TestConfirmMutation_ProductionYesNeverPrompts(t *testing.T) {
	f := mutationFactory(t, cmdutil.GlobalOptions{Env: config.EnvProduction, Yes: true}, "")
	prompttest.Use(t, f)
	require.NoError(t, f.ConfirmMutation("delete webhook", webhookXID, true))
}

// A person at a terminal still names production on the command line; no question is asked.
func TestConfirmMutation_TerminalStillNeedsEnvOnTheCommandLine(t *testing.T) {
	f := mutationFactory(t, cmdutil.GlobalOptions{}, config.EnvProduction)
	prompttest.Use(t, f)
	err := f.ConfirmMutation("delete webhook", webhookXID, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "add --env production")
}

// The host, not default_env, is what sent this write to production, so the message names the host.
// Under --env sandbox the only fix is the host: adding --env production would contradict the user.
func TestConfirmMutation_TerminalNamesTheProductionHost(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		want    string
		wantNot string
	}{
		{"--env sandbox", config.EnvSandbox, `through the API host "cli-api.sepay.vn", but --env sandbox was given: point it at the sandbox host`, "add --env production"},
		{"no --env", "", `through the API host "cli-api.sepay.vn", which is not the sandbox host: point it at the sandbox host, or add --env production`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := mutationFactory(t, cmdutil.GlobalOptions{Env: tc.env, APIBase: "https://cli-api.sepay.vn/v2"}, "")
			prompttest.Use(t, f)
			err := f.ConfirmMutation("delete webhook", webhookXID, true)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), "default_env")
			if tc.wantNot != "" {
				assert.NotContains(t, err.Error(), tc.wantNot)
			}
		})
	}
}

func TestCheckProdWrite_SandboxIsNoOp(t *testing.T) {
	f := mutationFactory(t, cmdutil.GlobalOptions{Env: config.EnvSandbox, Yes: true}, "")
	require.NoError(t, f.CheckProdWrite("replay a webhook delivery", ""))
}

// A person at a terminal whose flags keep the prompt away is told which flags, not to ask the user.
func TestConfirmMutation_TerminalWithPromptOffNamesTheFlags(t *testing.T) {
	for _, global := range []cmdutil.GlobalOptions{
		{Env: config.EnvProduction, Output: "json"},
		{Env: config.EnvProduction, Quiet: true},
		{Env: config.EnvProduction, NoInput: true},
		{Env: config.EnvProduction, Yes: true},
	} {
		f := mutationFactory(t, global, "")
		f.IOStreams.SetStdinTTY(true)
		f.IOStreams.SetStdoutTTY(true)
		err := f.ConfirmMutation("create webhook", "", false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "and answer the prompt")
		assert.NotContains(t, err.Error(), "ask the user")
	}
}
