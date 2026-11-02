package cmdutil

import (
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssertCredentialHost(t *testing.T) {
	cases := []struct {
		name      string
		env       string
		url       string
		canonical string
		wantErr   bool
	}{
		{name: "sandbox api host", env: config.EnvSandbox, url: "https://cli-api-sandbox.sepay.vn/v2", canonical: baseURLForEnv(config.EnvSandbox)},
		{name: "sandbox pg host", env: config.EnvSandbox, url: "https://pgapi-sandbox.sepay.vn", canonical: pgBaseURLForEnv(config.EnvSandbox)},
		{name: "sandbox bankhub host", env: config.EnvSandbox, url: "https://bankhub-api-sandbox.sepay.vn", canonical: bankHubBaseURLForEnv(config.EnvSandbox)},
		{name: "sandbox einvoice host", env: config.EnvSandbox, url: "https://einvoice-api-sandbox.sepay.vn", canonical: einvoiceBaseURLForEnv(config.EnvSandbox)},
		{name: "production api host", env: config.EnvProduction, url: "https://cli-api.sepay.vn/v2", canonical: baseURLForEnv(config.EnvProduction)},
		{name: "production pg host", env: config.EnvProduction, url: "https://pgapi.sepay.vn", canonical: pgBaseURLForEnv(config.EnvProduction)},
		{name: "oauth host refused in prod", env: config.EnvProduction, url: "https://my.sepay.vn", canonical: baseURLForEnv(config.EnvProduction), wantErr: true},
		{name: "oauth host refused in sandbox", env: config.EnvSandbox, url: "https://my.sepay.vn", canonical: baseURLForEnv(config.EnvSandbox), wantErr: true},
		{name: "loopback always allowed", env: config.EnvSandbox, url: "http://127.0.0.1:8079/api/v2", canonical: baseURLForEnv(config.EnvSandbox)},
		{name: "localhost always allowed", env: config.EnvProduction, url: "http://localhost:8080/v2", canonical: baseURLForEnv(config.EnvProduction)},

		{name: "pg host refused for the api client", env: config.EnvSandbox, url: "https://pgapi-sandbox.sepay.vn/v2", canonical: baseURLForEnv(config.EnvSandbox), wantErr: true},
		{name: "bankhub host refused for the pg client", env: config.EnvSandbox, url: "https://bankhub-api-sandbox.sepay.vn", canonical: pgBaseURLForEnv(config.EnvSandbox), wantErr: true},
		{name: "einvoice host refused for the pg client", env: config.EnvProduction, url: "https://einvoice-api.sepay.vn", canonical: pgBaseURLForEnv(config.EnvProduction), wantErr: true},
		{name: "api host refused for the einvoice client", env: config.EnvProduction, url: "https://cli-api.sepay.vn", canonical: einvoiceBaseURLForEnv(config.EnvProduction), wantErr: true},

		{name: "prod api host refused in sandbox", env: config.EnvSandbox, url: "https://cli-api.sepay.vn/v2", canonical: baseURLForEnv(config.EnvSandbox), wantErr: true},
		{name: "sandbox api host refused in prod", env: config.EnvProduction, url: "https://cli-api-sandbox.sepay.vn/v2", canonical: baseURLForEnv(config.EnvProduction), wantErr: true},

		{name: "arbitrary host refused", env: config.EnvProduction, url: "https://evil.example/v2", canonical: baseURLForEnv(config.EnvProduction), wantErr: true},
		{name: "lookalike suffix refused", env: config.EnvProduction, url: "https://cli-api.sepay.vn.evil.com/v2", canonical: baseURLForEnv(config.EnvProduction), wantErr: true},
		{name: "public ip refused", env: config.EnvSandbox, url: "https://203.0.113.9:8080/api/v2", canonical: baseURLForEnv(config.EnvSandbox), wantErr: true},
		{name: "private ip refused", env: config.EnvSandbox, url: "http://192.168.1.10:8079/api/v2", canonical: baseURLForEnv(config.EnvSandbox), wantErr: true},

		{name: "uppercase prod host refused in sandbox", env: config.EnvSandbox, url: "https://CLI-API.SEPAY.VN/v2", canonical: baseURLForEnv(config.EnvSandbox), wantErr: true},
		{name: "trailing dot normalized", env: config.EnvProduction, url: "https://cli-api.sepay.vn./v2", canonical: baseURLForEnv(config.EnvProduction)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AssertCredentialHost(tc.env, tc.url, "test", tc.canonical)
			if tc.wantErr {
				require.Error(t, err)
				var flagErr *FlagError
				assert.ErrorAs(t, err, &flagErr, "must be a FlagError for exit-code mapping")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestOAuthBaseURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "https://my.sepay.vn"},
		{"my.sepay.vn", "https://my.sepay.vn"},
		{"my.sepay.vn/", "https://my.sepay.vn"},
		{"127.0.0.1:8080", "https://127.0.0.1:8080"},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"https://my.sepay.vn", "https://my.sepay.vn"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, OAuthBaseURL(tc.in))
		})
	}
}

func TestAssertOAuthHost(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "default oauth host", url: "https://my.sepay.vn"},
		{name: "loopback", url: "http://127.0.0.1:9443"},
		{name: "api host is not an oauth host", url: "https://cli-api.sepay.vn", wantErr: true},
		{name: "arbitrary host refused", url: "https://evil.example", wantErr: true},
		{name: "public ip refused", url: "https://203.0.113.9:8080", wantErr: true},
		{name: "private ip refused", url: "https://192.168.1.10:8080", wantErr: true},
		{name: "uppercase default host", url: "https://MY.SEPAY.VN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AssertOAuthHost(tc.url, "test")
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// Accepting the OAuth host would let SEPAY_SANDBOX_PG_API_URL=https://my.sepay.vn reach production.
func TestAssertCredentialHost_OAuthHostIsNotASandboxTarget(t *testing.T) {
	for _, env := range []string{config.EnvSandbox, config.EnvProduction} {
		err := AssertCredentialHost(env, "https://"+defaultOAuthHost+"/api/v2", "test", baseURLForEnv(env))
		require.Error(t, err, "env=%s: the OAuth host is not a credential target", env)
		assert.Contains(t, err.Error(), "refusing to send credentials")
	}
	require.NoError(t, AssertOAuthHost("https://"+defaultOAuthHost, "--hostname"))
	require.NoError(t, AssertCredentialHost(config.EnvSandbox, "https://pgapi-sandbox.sepay.vn", "test", pgBaseURLForEnv(config.EnvSandbox)))
	require.NoError(t, AssertCredentialHost(config.EnvProduction, "https://pgapi.sepay.vn", "test", pgBaseURLForEnv(config.EnvProduction)))
}

func TestAssertCheckoutHost(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "production checkout host", url: "https://pay.sepay.vn/v1/checkout?merchant=m1"},
		{name: "sandbox checkout host", url: "https://pay-sandbox.sepay.vn/v1/checkout?merchant=m1"},
		{name: "uppercase host", url: "https://PAY.SEPAY.VN/v1/checkout"},
		{name: "trailing dot host", url: "https://pay.sepay.vn./v1/checkout"},
		{name: "zone apex refused", url: "https://sepay.vn/v1/checkout", wantErr: true},
		{name: "sibling sepay host refused", url: "https://cli-api.sepay.vn/v1/checkout", wantErr: true},
		{name: "loopback", url: "http://127.0.0.1:8079/v1/checkout"},
		{name: "suffix lookalike refused", url: "https://pay.sepay.vn.attacker.io/v1/checkout", wantErr: true},
		{name: "arbitrary host refused", url: "https://evil.example/pay", wantErr: true},
		{name: "public ip refused", url: "http://203.0.113.9:8079/v1/checkout", wantErr: true},
		{name: "private ip refused", url: "http://192.168.1.10:8079/v1/checkout", wantErr: true},
		{name: "no host", url: "/v1/checkout", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AssertCheckoutHost(tc.url, "Location")
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestAssertCredentialHost_RejectsTheCheckoutHost(t *testing.T) {
	for env, url := range map[string]string{
		config.EnvProduction: "https://pay.sepay.vn/v1/checkout",
		config.EnvSandbox:    "https://pay-sandbox.sepay.vn/v1/checkout",
	} {
		require.Error(t, AssertCredentialHost(env, url, "Location", pgBaseURLForEnv(env)), "env=%s", env)
		require.NoError(t, AssertCheckoutHost(url, "Location"), "env=%s", env)
	}
}

func TestAssertHubHost(t *testing.T) {
	const prod, sb = config.EnvProduction, config.EnvSandbox
	tests := []struct {
		env     string
		url     string
		wantErr bool
	}{
		{prod, "https://ssehub.sepay.vn", false},
		{prod, "https://ssehub.sepay.vn:8443/", false},
		{prod, "https://SSEHUB.SEPAY.VN", false},
		{prod, "https://ssehub.sepay.vn.", false},
		{prod, "http://127.0.0.1:8080", false},
		{prod, "http://localhost:8080", false},
		{prod, "http://[::1]:8080", false},
		{sb, "https://ssehub-sandbox.sepay.vn", false},
		{sb, "https://ssehub-sandbox.sepay.vn:8443", false},
		{sb, "https://SSEHUB-SANDBOX.sepay.vn.", false},
		{sb, "http://127.0.0.1:8080", false},

		{prod, "https://ssehub-sandbox.sepay.vn", true},
		{sb, "https://ssehub.sepay.vn", true},
		{prod, "https://sepay.vn", true},
		{sb, "https://sepay.vn", true},
		{prod, "https://sse.sepay.vn", true},
		{prod, "https://my.sepay.vn", true},
		{sb, "https://x.sepay.vn", true},
		{prod, "https://ssehub.sepay.vn.evil.com", true},
		{prod, "https://x.sepay.vn.evil.com", true},
		{prod, "https://evilssehub.sepay.vn", true},
		{prod, "https://[fe80::1%25eth0.sepay.vn]", true},
		{prod, "https://[fe80::1%25ssehub.sepay.vn]", true},
		{prod, "https://ssehub.sepay.vn@evil.com", true},
		{prod, "", true},
		{"", "https://ssehub.sepay.vn", true}, // an unset env gets sandbox's hub
	}
	for _, tt := range tests {
		t.Run(tt.env+" "+tt.url, func(t *testing.T) {
			err := AssertHubHost(tt.env, tt.url, "hub_url")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestAssertHubHost_ErrorNamesExpectedHost(t *testing.T) {
	err := AssertHubHost(config.EnvProduction, "https://ssehub-sandbox.sepay.vn", "hub_url in the mint response")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"ssehub-sandbox.sepay.vn"`)
	assert.Contains(t, err.Error(), "expected ssehub.sepay.vn (production) or loopback")
}
