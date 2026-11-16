package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sepayvn/sepay-cli/internal/api"
)

// The server attributes login, refresh and revoke rows to an agent from the User-Agent, so every
// OAuth and token-management request must carry it rather than Go's default.
func TestOAuthRequestsSendUserAgent(t *testing.T) {
	for _, k := range []string{"AI_AGENT", "CLAUDECODE", "GEMINI_CLI", "CODEX_SANDBOX", "CODEX_CI", "CURSOR_AGENT", "COPILOT_AGENT", "CLINE_ACTIVE", "AGENT"} {
		t.Setenv(k, "")
	}
	t.Setenv("CURSOR_AGENT", "1")

	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+" "+r.Header.Get("User-Agent"))
		switch r.URL.Path {
		case "/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"at","token_type":"Bearer","refresh_token":"rt","expires_in":3600}`))
		case "/oauth/tokens":
			_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
		case "/oauth/tokens/revoke-all":
			_, _ = w.Write([]byte(`{"status":"success","data":{"revoked":0}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sepay-cli", srv.Client())
	ctx := context.Background()
	_, err := c.Refresh(ctx, "rt", nil)
	require.NoError(t, err)
	require.NoError(t, c.Revoke(ctx, "rt", "refresh_token"))
	_, err = c.ListTokens(ctx, "at")
	require.NoError(t, err)
	_, err = c.RevokeAllTokens(ctx, "at")
	require.NoError(t, err)

	want := api.UserAgent()
	assert.Contains(t, want, " AI-Agent/cursor")
	require.Len(t, got, 4)
	for _, line := range got {
		assert.Contains(t, line, " "+want)
	}
}
