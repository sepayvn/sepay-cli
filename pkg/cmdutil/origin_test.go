package cmdutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCredentialOrigin(t *testing.T) {
	cases := []struct {
		name, url string
		wantErr   bool
	}{
		{name: "https ok", url: "https://cli-api.sepay.vn/v2"},
		{name: "https ip ok", url: "https://10.0.0.5:8443"},
		{name: "http loopback ok", url: "http://localhost:8787/v2"},
		{name: "http 127 ok", url: "http://127.0.0.1:8079/api/v2"},
		{name: "http ipv6 loopback ok", url: "http://[::1]:8443"},
		{name: "http non-loopback rejected", url: "http://cli-api.sepay.vn/v2", wantErr: true},
		{name: "http public ip rejected", url: "http://203.0.113.9:8080", wantErr: true},
		{name: "http private ip rejected", url: "http://192.168.1.10:8079/api/v2", wantErr: true},
		{name: "userinfo rejected", url: "https://user:pass@cli-api.sepay.vn", wantErr: true},
		{name: "no host rejected", url: "https://", wantErr: true},
		{name: "ftp scheme rejected", url: "ftp://cli-api.sepay.vn", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCredentialOrigin(tc.url)
			if tc.wantErr {
				require.Error(t, err)
				var flagErr *FlagError
				assert.ErrorAs(t, err, &flagErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// No rejection branch may echo the value it rejected: this validator runs on URLs
// that can carry a credential, and its error text reaches CI logs and bug reports.
func TestValidateCredentialOrigin_ErrorNeverEchoesTheSecret(t *testing.T) {
	cases := []struct{ name, url, secret string }{
		{"userinfo", "https://svc:p@ssw0rd@cli-api.sepay.vn/v2", "p@ssw0rd"},
		{"userinfo with no host", "https://svc:p@ssw0rd@", "p@ssw0rd"},
		{"unparseable", "https://svc:p@ssw0rd@ho st/api\x7f", "p@ssw0rd"},
		{"bare credential as username", "https://notasecret@cli-api.sepay.vn", "notasecret"},
		{"token in the query of a bad scheme", "ftp://cli-api.sepay.vn/x?access_token=SEKRIT", "SEKRIT"},
		{"plaintext non-loopback", "http://svc:p@ssw0rd@evil.tld", "p@ssw0rd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCredentialOrigin(tc.url)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), tc.secret)
		})
	}
}

func TestValidateDestinationURL(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		{url: "https://example.com/hook"},
		{url: "http://example.com/hook"},
		{url: "http://localhost:3000/hook"}, // the API decides whether the host is reachable
		{url: "test", wantErr: true},
		{url: "example.com/hook", wantErr: true},
		{url: "", wantErr: true},
		{url: "ftp://example.com", wantErr: true},
		{url: "https://", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			err := ValidateDestinationURL(tt.url, "--with-webhook")
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, ExitUsage, ExitCodeForError(err))
			assert.Contains(t, err.Error(), "--with-webhook")
		})
	}
}

func TestDisplayHost(t *testing.T) {
	cases := []struct{ name, host, want string }{
		{"implicit https reads as a bare host", "https://my.sepay.vn", "my.sepay.vn"},
		{"a host stored bare passes through", "my.sepay.vn", "my.sepay.vn"},
		{"http and port are the dev backend's identity", "http://127.0.0.1:8810", "http://127.0.0.1:8810"},
		{"trailing slash dropped", "https://my.sepay.vn/", "my.sepay.vn"},
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, DisplayHost(tc.host))
		})
	}
}
