package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sepayvn/sepay-cli/internal/build"
)

// clearAgentEnv blanks every variable the detector reads, so the developer's own shell (often an
// agent) cannot leak into the expected User-Agent.
func clearAgentEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"AI_AGENT", "CLAUDECODE", "GEMINI_CLI", "CODEX_SANDBOX", "CODEX_CI", "CURSOR_AGENT", "COPILOT_AGENT", "CLINE_ACTIVE", "AGENT"} {
		t.Setenv(k, "")
	}
}

func TestFormatUserAgent(t *testing.T) {
	assert.Equal(t, "sepay-cli/0.1.8 (darwin; arm64)", formatUserAgent("0.1.8", "darwin", "arm64", ""))
	assert.Equal(t, "sepay-cli/0.1.8 (windows; amd64) AI-Agent/claude-code", formatUserAgent("0.1.8", "windows", "amd64", "claude-code"))
}

func TestUserAgent_NoAgent(t *testing.T) {
	clearAgentEnv(t)
	assert.Equal(t, "sepay-cli/"+build.Version+" ("+runtime.GOOS+"; "+runtime.GOARCH+")", UserAgent())
}

func TestUserAgent_WithAgent(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("CLAUDECODE", "1")
	assert.Equal(t, "sepay-cli/"+build.Version+" ("+runtime.GOOS+"; "+runtime.GOARCH+") AI-Agent/claude-code", UserAgent())
}

func TestClient_SendsUserAgentWithAgent(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("AI_AGENT", "Amp")
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "tok").Post(context.Background(), "/webhooks", WriteOptions{Body: map[string]string{}})
	require.NoError(t, err)
	assert.Equal(t, UserAgent(), got)
	assert.Contains(t, got, " AI-Agent/amp")
}
