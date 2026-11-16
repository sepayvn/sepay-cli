package root

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"
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
	f, out, errOut := logoutAllFactory(t, dir, srv.URL, args)

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

// Without a terminal --yes confirms ending every session, in either env; without --yes nothing is sent.
func TestRevokeAll_Unattended(t *testing.T) {
	cases := []struct {
		args []string
		sent bool
	}{
		{[]string{"logout", "--all", "--yes"}, true},
		{[]string{"logout", "--all", "--yes", "--json"}, true},
		{[]string{"logout", "--all", "--yes", "--env", "production"}, true},
		{[]string{"logout", "--all"}, false},
		{[]string{"logout", "--all", "--env", "production"}, false},
		{[]string{"auth", "revoke", "--all", "--yes"}, true},
		{[]string{"auth", "revoke", "--all", "--yes", "--env", "production"}, true},
		{[]string{"auth", "revoke", "--all"}, false},
		{[]string{"auth", "revoke", "--all", "--env", "production"}, false},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			keyring.MockInit()
			var requests, revokeAll atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path == "/oauth/tokens/revoke-all" {
					revokeAll.Add(1)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"revoked": 1}})
			}))
			t.Cleanup(srv.Close)
			f, _, _ := logoutAllFactory(t, t.TempDir(), srv.URL, tc.args)

			cmd := NewCmdRoot(f)
			cmd.SetArgs(tc.args)
			err := cmd.ExecuteContext(context.Background())
			if tc.sent {
				require.NoError(t, err)
				assert.Equal(t, int32(1), revokeAll.Load())
				return
			}
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), err.Error())
			assert.Contains(t, err.Error(), "requires confirmation; pass --yes")
			assert.Zero(t, requests.Load())
		})
	}
}

// At a terminal --yes answers it without a prompt, and n keeps every session.
func TestRevokeAll_TerminalNeedsTheAnswer(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		answer any
		sent   bool
	}{
		{"y revokes", []string{"auth", "revoke", "--all"}, true, true},
		{"n keeps", []string{"auth", "revoke", "--all"}, false, false},
		{"--yes skips the prompt", []string{"auth", "revoke", "--all", "--yes"}, nil, true},
		{"logout y revokes", []string{"logout", "--all"}, true, true},
		{"logout --yes skips the prompt", []string{"logout", "--all", "--yes"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keyring.MockInit()
			var revokeAll atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("/oauth/tokens/revoke-all", func(w http.ResponseWriter, _ *http.Request) {
				revokeAll.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"revoked": 1}})
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			f, _, _ := logoutAllFactory(t, t.TempDir(), srv.URL, tc.args)
			label := "revoke EVERY session of the account (all devices/CI)?"
			if tc.args[0] == "logout" {
				label = "log out of EVERY stored profile (and revoke each account's sessions)?"
			}
			if tc.answer != nil {
				prompttest.Use(t, f, prompttest.Step{Label: label, Answer: tc.answer})
			} else {
				prompttest.Use(t, f)
			}

			cmd := NewCmdRoot(f)
			cmd.SetArgs(tc.args)
			err := cmd.ExecuteContext(context.Background())
			if tc.sent {
				require.NoError(t, err)
				assert.Equal(t, int32(1), revokeAll.Load())
				return
			}
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), err.Error())
			assert.Zero(t, revokeAll.Load())
		})
	}
}

func logoutAllFactory(t *testing.T, dir, oauthURL string, args []string) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}, RawArgs: args}
	f.Credentials = func() (*auth.Manager, error) {
		hosts, err := config.LoadHostsFrom(dir)
		if err != nil {
			return nil, err
		}
		return auth.NewManager(hosts), nil
	}
	f.NewOAuthClient = func(string) *auth.Client { return auth.NewClient(oauthURL, cmdutil.ClientID, nil) }
	mgr, err := f.Credentials()
	require.NoError(t, err)
	_, err = mgr.Set("work", "my.sepay.vn", &auth.Credential{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour)}, []string{"read"}, false, "a@x.vn")
	require.NoError(t, err)
	return f, out, errOut
}
