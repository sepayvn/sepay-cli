package root

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
)

func TestLogoutAll_UnconfirmedRevokeExitsNonZeroWithEnvelope(t *testing.T) {
	keyring.MockInit()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/tokens/revoke-all", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error_code": "unauthorized", "message": "invalid token"})
	})
	mux.HandleFunc("/oauth/revoke", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	args := []string{"logout", "--all", "--yes", "--json"}
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}, RawArgs: args}
	f.Credentials = func() (*auth.Manager, error) {
		hosts, err := config.LoadHostsFrom(dir)
		if err != nil {
			return nil, err
		}
		return auth.NewManager(hosts), nil
	}
	f.NewOAuthClient = func(string) *auth.Client { return auth.NewClient(srv.URL, cmdutil.ClientID, nil) }
	mgr, err := f.Credentials()
	require.NoError(t, err)
	_, err = mgr.Set("work", "my.sepay.vn", &auth.Credential{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour)}, []string{"read"}, false, "a@x.vn")
	require.NoError(t, err)

	cmd := NewCmdRoot(f)
	cmd.SetArgs(args)
	runErr := cmd.ExecuteContext(context.Background())
	errOut.Reset() // keep only the envelope
	code := cmdutil.HandleError(f, runErr)

	assert.Equal(t, cmdutil.ExitError, code)
	assert.NotContains(t, out.String(), "Logged out")
	var env struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
		Message   string `json:"message"`
		ExitCode  int    `json:"exit_code"`
	}
	require.NoError(t, json.Unmarshal(errOut.Bytes(), &env), errOut.String())
	assert.Equal(t, "error", env.Status)
	assert.Equal(t, "error", env.ErrorCode)
	assert.Equal(t, 1, env.ExitCode)
	assert.Contains(t, env.Message, "sepay logout --all")
}
