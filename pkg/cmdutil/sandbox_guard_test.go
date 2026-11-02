package cmdutil

import (
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssertSandboxTarget(t *testing.T) {
	cases := []struct {
		name        string
		apiBase     string // --api-base
		envURL      string // SEPAY_SANDBOX_API_URL
		wantErr     bool
		errContains string
	}{
		{name: "default sandbox host", wantErr: false},
		{name: "sandbox host via api-base", apiBase: "https://cli-api-sandbox.sepay.vn/v2", wantErr: false},
		{name: "loopback via api-base", apiBase: "http://localhost:8787/v2", wantErr: false},
		{name: "loopback ip via api-base", apiBase: "http://127.0.0.1:8079/api/v2", wantErr: false},
		{name: "loopback is not port-constrained", apiBase: "http://127.0.0.1:8080/api/v2", wantErr: false},
		{name: "production host via api-base", apiBase: "https://cli-api.sepay.vn/v2", wantErr: true, errContains: "non-sandbox host"},
		{name: "production host via env", envURL: "https://cli-api.sepay.vn/v2", wantErr: true, errContains: "non-sandbox host"},
		{name: "uppercase production host blocked", apiBase: "https://CLI-API.SEPAY.VN/v2", wantErr: true, errContains: "non-sandbox host"},
		{name: "trailing-dot production host blocked", apiBase: "https://cli-api.sepay.vn./v2", wantErr: true, errContains: "non-sandbox host"},
		{name: "mixed-case pg production host blocked", apiBase: "https://PgApi.SePay.vn/v2", wantErr: true, errContains: "non-sandbox host"},
		{name: "arbitrary host refused", apiBase: "https://evil.example/v2", wantErr: true, errContains: "non-sandbox host"},
		{name: "arbitrary sepay zone host refused", apiBase: "https://alias.sepay.vn/v2", wantErr: true, errContains: "non-sandbox host"},
		{name: "public ip refused", apiBase: "https://203.0.113.9:8079/api/v2", wantErr: true, errContains: "non-sandbox host"},
		{name: "private ip refused", apiBase: "http://192.168.1.10:8079/api/v2", wantErr: true, errContains: "non-sandbox host"},
		{name: "unparseable host", apiBase: "http://203.0.113.9:8079./api/v2", wantErr: true, errContains: "cannot parse API host"},
		{name: "userinfo cannot smuggle a host past the check", apiBase: "http://127.0.0.1:8079@evil.example/api/v2", wantErr: true, errContains: "non-sandbox host"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SEPAY_SANDBOX_API_URL", tc.envURL)

			ios, _, _, _ := iostreams.Test()
			f := &Factory{
				IOStreams: ios,
				Global:    &GlobalOptions{Env: config.EnvSandbox, APIBase: tc.apiBase},
			}

			err := f.AssertSandboxTarget()
			if tc.wantErr {
				require.Error(t, err)
				if tc.errContains != "" {
					assert.Contains(t, err.Error(), tc.errContains)
				}
				var flagErr *FlagError
				assert.ErrorAs(t, err, &flagErr, "must be a FlagError for exit-code mapping")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestIsProductionTarget(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		apiBase string
		want    bool
	}{
		{name: "production env", env: config.EnvProduction, want: true},
		{name: "canonical sandbox host", env: config.EnvSandbox, want: false},
		{name: "loopback is sandbox on any port", env: config.EnvSandbox, apiBase: "http://127.0.0.1:8080/api/v2", want: false},
		{name: "production host under a sandbox label", env: config.EnvSandbox, apiBase: "https://cli-api.sepay.vn/v2", want: true},
		{name: "unknown host under a sandbox label", env: config.EnvSandbox, apiBase: "https://203.0.113.9:8079/api/v2", want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			f := &Factory{
				IOStreams: ios,
				Global:    &GlobalOptions{Env: tc.env, APIBase: tc.apiBase},
			}

			assert.Equal(t, tc.want, f.isProductionTarget())
		})
	}
}

func TestAssertSandboxBase(t *testing.T) {
	canonical := "https://bankhub-api-sandbox.sepay.vn"

	cases := []struct {
		name        string
		env         string
		baseURL     string
		wantErr     bool
		errContains string
	}{
		{name: "canonical product sandbox host", env: config.EnvSandbox, baseURL: canonical, wantErr: false},
		{name: "production env is not this check's business", env: config.EnvProduction, baseURL: "https://bankhub-api.sepay.vn", wantErr: false},
		{name: "another product's sandbox host is refused", env: config.EnvSandbox, baseURL: "https://pgapi-sandbox.sepay.vn", wantErr: true, errContains: "non-sandbox host"},
		{name: "product production host refused on sandbox", env: config.EnvSandbox, baseURL: "https://bankhub-api.sepay.vn", wantErr: true, errContains: "non-sandbox host"},
		{name: "private ip refused", env: config.EnvSandbox, baseURL: "http://192.168.1.10:9000", wantErr: true, errContains: "non-sandbox host"},
		{name: "loopback allowed on any port", env: config.EnvSandbox, baseURL: "http://127.0.0.1:9999", wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}

			err := f.AssertSandboxBase(tc.env, tc.baseURL, "config", canonical)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errContains != "" {
					assert.Contains(t, err.Error(), tc.errContains)
				}
				var flagErr *FlagError
				assert.ErrorAs(t, err, &flagErr, "must be a FlagError for exit-code mapping")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestAssertSandboxBase_ErrorNamesTheExpectedHost(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}

	err := f.AssertSandboxBase(config.EnvSandbox, "https://bankhub-api-sandbox.sepay.vn",
		"SEPAY_SANDBOX_PG_API_URL", "https://pgapi-sandbox.sepay.vn")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bankhub-api-sandbox.sepay.vn", "names what was given")
	assert.Contains(t, err.Error(), "pgapi-sandbox.sepay.vn", "and what was expected")
	assert.Contains(t, err.Error(), "SEPAY_SANDBOX_PG_API_URL", "and where it came from")
}

func TestAssertSandboxTarget_SilentWhenAccepted(t *testing.T) {
	for _, apiBase := range []string{"", "http://127.0.0.1:8079/api/v2"} {
		ios, _, _, errOut := iostreams.Test()
		f := &Factory{IOStreams: ios, Global: &GlobalOptions{Env: config.EnvSandbox, APIBase: apiBase, Output: "table"}}

		require.NoError(t, f.AssertSandboxTarget())
		assert.Empty(t, errOut.String())
	}
}
