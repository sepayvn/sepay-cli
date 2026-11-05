package cmdutil

import (
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/require"
)

func TestConfirmMutation(t *testing.T) {
	cases := []struct {
		name        string
		env         string
		destructive bool
		yes         bool
		wantErr     bool
	}{
		{"prod create needs confirm (non-TTY, no --yes)", config.EnvProduction, false, false, true},
		{"prod create with --yes", config.EnvProduction, false, true, false},
		{"prod delete needs confirm", config.EnvProduction, true, false, true},
		{"prod delete with --yes", config.EnvProduction, true, true, false},
		{"sandbox create is silent (non-destructive)", config.EnvSandbox, false, false, false},
		{"sandbox delete needs confirm", config.EnvSandbox, true, false, true},
		{"sandbox delete with --yes", config.EnvSandbox, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test() // non-TTY
			f := &Factory{IOStreams: ios, Global: &GlobalOptions{Env: tc.env, Yes: tc.yes}}
			err := f.ConfirmMutation("do thing", tc.destructive)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// A command labelled sandbox but pointed at a production host via --api-base is
// still treated as production and requires confirmation.
func TestConfirmMutation_ProdHostUnderSandboxLabel(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	ios, _, _, _ := iostreams.Test()
	f := &Factory{
		IOStreams: ios,
		Global:    &GlobalOptions{Env: config.EnvSandbox, APIBase: "https://cli-api.sepay.vn/v2"},
	}
	f.Config = config.Load

	require.Error(t, f.ConfirmMutation("create webhook", false), "sandbox label + prod host must still confirm")

	f.Global.Yes = true
	require.NoError(t, f.ConfirmMutation("create webhook", false))
}
