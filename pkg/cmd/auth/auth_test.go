package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
)

// fakeOAuth is an in-memory OAuth host that issues a token, echoes the requested scope and records revocations.
type fakeOAuth struct {
	*httptest.Server

	mu           sync.Mutex
	pending      int      // device-grant polls to answer with authorization_pending first
	deviceScope  string   // scope carried on /oauth/device, returned on the device grant
	email        string   // when set, returned as the token owner's email
	revoked      []string // tokens passed to /oauth/revoke
	revokeStatus int      // when non-zero, /oauth/revoke fails with this status

	// Kept apart from deviceScope: whether the refresh omitted scope is the assertion.
	refreshScopeSent bool
	refreshScope     string

	// Grant at login; a refresh outside it gets invalid_scope. Empty accepts any.
	grantedScopes string

	sessions        []auth.TokenSession // returned by GET /oauth/tokens
	revokedIDs      []string            // ids passed to POST /oauth/tokens/revoke
	revokeAllCount  int                 // POST /oauth/tokens/revoke-all calls
	tokensStatus    int                 // when non-zero, /oauth/tokens* fail with this status
	verificationURI string              // overrides the device flow's verification_uri
}

func newFakeOAuth(t *testing.T) *fakeOAuth {
	t.Helper()
	f := &fakeOAuth{}
	mux := http.NewServeMux()

	mux.HandleFunc("/oauth/device", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.deviceScope = r.FormValue("scope")
		f.mu.Unlock()
		uri := f.verificationURI
		if uri == "" {
			uri = "https://my.sepay.vn/activate"
		}
		writeJSON(w, map[string]any{
			"device_code":               "dev_123",
			"user_code":                 "WDJB-MJHT",
			"verification_uri":          uri,
			"verification_uri_complete": "https://my.sepay.vn/activate?user_code=WDJB-MJHT",
			"expires_in":                900,
			"interval":                  0,
		})
	})

	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		scope := r.FormValue("scope")
		if r.FormValue("grant_type") == "urn:ietf:params:oauth:grant-type:device_code" {
			f.mu.Lock()
			if f.pending > 0 {
				f.pending--
				f.mu.Unlock()
				writeJSON(w, map[string]any{"error": "authorization_pending"})
				return
			}
			scope = f.deviceScope
			f.mu.Unlock()
		}
		if r.FormValue("grant_type") == "refresh_token" {
			f.mu.Lock()
			f.refreshScopeSent = r.Form.Has("scope")
			f.refreshScope = scope
			granted := f.grantedScopes
			f.mu.Unlock()
			if granted != "" {
				if outside := scopesOutside(scope, granted); outside != "" {
					w.WriteHeader(http.StatusBadRequest)
					writeJSON(w, map[string]any{
						"error":             "invalid_scope",
						"error_description": "Check the `" + outside + "` scope",
					})
					return
				}
				if scope == "" {
					scope = granted
				}
			}
		}
		if scope == "" {
			scope = "sandbox"
		}
		body := map[string]any{
			"access_token":  "at_" + strings.ReplaceAll(scope, " ", "_"),
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "rt_" + strings.ReplaceAll(scope, " ", "_"),
			"scope":         scope,
		}
		if f.email != "" {
			body["email"] = f.email
		}
		writeJSON(w, body)
	})

	mux.HandleFunc("/oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.revoked = append(f.revoked, r.FormValue("token"))
		status := f.revokeStatus
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"server_error"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/oauth/tokens", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		status, sessions := f.tokensStatus, f.sessions
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			writeJSON(w, map[string]any{"status": "error", "error_code": "unauthorized", "message": "invalid token"})
			return
		}
		writeJSON(w, map[string]any{"status": "success", "data": sessions})
	})

	mux.HandleFunc("/oauth/tokens/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		status := f.tokensStatus
		if status == 0 {
			f.revokedIDs = append(f.revokedIDs, r.FormValue("id"))
		}
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			writeJSON(w, map[string]any{"status": "error", "error_code": "not_found", "message": "no such token"})
			return
		}
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"revoked": 1}})
	})

	mux.HandleFunc("/oauth/tokens/revoke-all", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.revokeAllCount++
		n := len(f.sessions)
		f.mu.Unlock()
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"revoked": n}})
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeOAuth) revokeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.revoked)
}

func (f *fakeOAuth) revokeAllCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revokeAllCount
}

func (f *fakeOAuth) revokedSessionIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revokedIDs...)
}

func (f *fakeOAuth) deviceScopeValue() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deviceScope
}

func (f *fakeOAuth) refreshScopeValue() (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshScopeSent, f.refreshScope
}

// scopesOutside returns the first requested scope not in granted, or "".
func scopesOutside(requested, granted string) string {
	have := make(map[string]bool)
	for _, s := range strings.Fields(granted) {
		have[s] = true
	}
	for _, s := range strings.Fields(requested) {
		if !have[s] {
			return s
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// testFactory wires a Factory to a temp config dir, a mocked keychain and oauthURL.
func testFactory(t *testing.T, oauthURL string) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	keyring.MockInit()
	dir := t.TempDir()
	ios, in, out, errOut := iostreams.Test()

	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	f.Credentials = func() (*auth.Manager, error) {
		hosts, err := config.LoadHostsFrom(dir)
		if err != nil {
			return nil, err
		}
		return auth.NewManager(hosts), nil
	}
	f.NewOAuthClient = func(string) *auth.Client {
		return auth.NewClient(oauthURL, cmdutil.ClientID, nil)
	}
	return f, in, out, errOut
}

func run(cmd *cobra.Command, args ...string) error {
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func seedProfile(t *testing.T, f *cmdutil.Factory, name string, scopes []string) {
	t.Helper()
	mgr, err := f.Credentials()
	require.NoError(t, err)
	_, err = mgr.Set(name, "my.sepay.vn", &auth.Credential{AccessToken: "at_" + name, RefreshToken: "rt_" + name}, scopes, false, "")
	require.NoError(t, err)
}

func TestParseScopes(t *testing.T) {
	assert.Equal(t, []string{"sandbox", "read"}, parseScopes(" sandbox , read , , read"))
	assert.Nil(t, parseScopes(""))
}

func TestMergeScopes(t *testing.T) {
	got := mergeScopes([]string{"sandbox"}, []string{"write"}, []string{"read"})
	assert.Equal(t, []string{"sandbox", "write"}, got)

	assert.Equal(t, []string{"read"}, mergeScopes(nil, []string{"read"}, nil))
}

// Exit 1 is retryable, so a missing credential must be exit 4 or a script loops forever.
func TestAuth_NoCredentialIsUnauthenticated(t *testing.T) {
	verbs := map[string]func(*cmdutil.Factory) *cobra.Command{
		"sessions": NewCmdSessions,
		"token":    NewCmdToken,
		"refresh":  NewCmdRefresh,
		"status":   NewCmdStatus,
	}
	for name, newCmd := range verbs {
		t.Run(name, func(t *testing.T) {
			srv := newFakeOAuth(t)
			f, _, out, errOut := testFactory(t, srv.URL)
			f.Global.Output = "json"

			err := run(newCmd(f))
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(err))
			assert.Equal(t, cmdutil.ExitAuth, cmdutil.HandleError(f, err))
			assert.Empty(t, out.String(), "stdout stays data-only")

			var got struct {
				Status    string `json:"status"`
				ErrorCode string `json:"error_code"`
				Message   string `json:"message"`
				ExitCode  int    `json:"exit_code"`
			}
			require.NoError(t, json.Unmarshal(errOut.Bytes(), &got), errOut.String())
			assert.Equal(t, "error", got.Status)
			assert.Equal(t, "unauthenticated", got.ErrorCode)
			assert.Equal(t, 4, got.ExitCode)
			assert.Contains(t, got.Message, "sepay login")
		})
	}
}
