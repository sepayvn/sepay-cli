package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
)

// grantServer is a stateful OAuth host: it rotates refresh tokens and revoke-all kills every token.
type grantServer struct {
	*httptest.Server
	mu     sync.Mutex
	live   map[string]bool   // token -> live
	pairOf map[string]string // refresh -> access
	minted int
	calls  map[string]int
}

func newGrantServer(t *testing.T, pairs map[string]string) *grantServer {
	t.Helper()
	g := &grantServer{live: map[string]bool{}, pairOf: map[string]string{}, calls: map[string]int{}}
	for rt, at := range pairs {
		g.live[rt], g.pairOf[rt] = true, at
		if at != "" {
			g.live[at] = true
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		g.mu.Lock()
		defer g.mu.Unlock()
		g.calls["refresh"]++
		rt := r.FormValue("refresh_token")
		if !g.live[rt] {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "invalid_grant"})
			return
		}
		g.live[rt], g.live[g.pairOf[rt]] = false, false
		g.minted++
		at, nrt := fmt.Sprintf("at_new%d", g.minted), fmt.Sprintf("rt_new%d", g.minted)
		g.live[at], g.live[nrt], g.pairOf[nrt] = true, true, at
		writeJSON(w, map[string]any{"access_token": at, "refresh_token": nrt, "token_type": "Bearer", "expires_in": 3600, "scope": "read"})
	})
	mux.HandleFunc("/oauth/tokens/revoke-all", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.calls["revoke-all"]++
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !g.live[bearer] {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, map[string]any{"status": "error", "error_code": "unauthorized", "message": "Token không hợp lệ hoặc đã hết hạn"})
			return
		}
		n := 0
		for tok, ok := range g.live {
			if ok {
				g.live[tok] = false
				n++
			}
		}
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"revoked": n}})
	})
	mux.HandleFunc("/oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		g.mu.Lock()
		defer g.mu.Unlock()
		g.calls["revoke"]++
		g.live[r.FormValue("token")] = false
		w.WriteHeader(http.StatusOK)
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

func (g *grantServer) alive(tok string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.live[tok]
}

func (g *grantServer) count(name string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[name]
}

func seedAccount(t *testing.T, f *cmdutil.Factory, name, email string, exp time.Time) {
	t.Helper()
	mgr, err := f.Credentials()
	require.NoError(t, err)
	_, err = mgr.Set(name, "my.sepay.vn", &auth.Credential{AccessToken: "at_" + name, RefreshToken: "rt_" + name, ExpiresAt: exp}, []string{"read"}, false, email)
	require.NoError(t, err)
}

func TestLogoutAll_ExpiredAccessRefreshesThenRevokesEverySession(t *testing.T) {
	srv := newGrantServer(t, map[string]string{"rt_work": "", "rt_m2": "at_m2"})
	f, _, out, errOut := testFactory(t, srv.URL)
	f.Global.Yes = true
	seedAccount(t, f, "work", "a@x.vn", time.Now().Add(-time.Minute))

	require.NoError(t, run(NewCmdLogout(f), "--all"))
	assert.Equal(t, 1, srv.count("refresh"))
	assert.Equal(t, 1, srv.count("revoke-all"))
	assert.False(t, srv.alive("rt_work"))
	assert.False(t, srv.alive("at_m2"))
	assert.False(t, srv.alive("rt_m2"))
	assert.Contains(t, out.String(), "Logged out")
	assert.Empty(t, errOut.String())
}

func TestLogoutAll_RevokeFailureRevokesOwnGrantAndExitsNonZero(t *testing.T) {
	srv := newGrantServer(t, map[string]string{"rt_work": "", "rt_m2": "at_m2"})
	f, _, out, errOut := testFactory(t, srv.URL)
	f.Global.Yes = true
	seedAccount(t, f, "work", "a@x.vn", time.Now().Add(30*time.Minute))

	err := run(NewCmdLogout(f), "--all")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "work")
	assert.Contains(t, err.Error(), "sepay logout --all")
	assert.NotContains(t, err.Error(), "rt_work")
	assert.False(t, srv.alive("rt_work"), "this machine's refresh token must not outlive the logout")
	assert.NotContains(t, out.String(), "Logged out")
	assert.Contains(t, errOut.String(), "other sessions may still be live")

	mgr, _ := f.Credentials()
	_, gerr := mgr.Get("work")
	assert.ErrorIs(t, gerr, auth.ErrNotFound)
}

func TestLogoutAll_OfflineKeepsTheCredential(t *testing.T) {
	srv := newGrantServer(t, nil)
	f, _, out, _ := testFactory(t, srv.URL)
	f.Global.Yes = true
	seedAccount(t, f, "work", "a@x.vn", time.Now().Add(30*time.Minute))
	srv.Close()

	err := run(NewCmdLogout(f), "--all")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "retry when the server is reachable: sepay logout --all, or remove it from this machine only: sepay logout --all --local")
	assert.NotContains(t, out.String(), "Logged out")
	mgr, _ := f.Credentials()
	assert.Contains(t, mgr.Hosts().Profiles, "work")
}

func TestLogoutAll_SameAccountRevokesOnce(t *testing.T) {
	srv := newGrantServer(t, map[string]string{"rt_work": "at_work", "rt_ci": "at_ci"})
	f, _, out, errOut := testFactory(t, srv.URL)
	f.Global.Yes = true
	future := time.Now().Add(30 * time.Minute)
	seedAccount(t, f, "work", "a@x.vn", future)
	seedAccount(t, f, "ci", "a@x.vn", future)

	require.NoError(t, run(NewCmdLogout(f), "--all"))
	assert.Equal(t, 1, srv.count("revoke-all"))
	assert.Equal(t, 2, strings.Count(out.String(), "Logged out"))
	assert.Empty(t, errOut.String())
	mgr, _ := f.Credentials()
	assert.Empty(t, mgr.Hosts().Profiles)
}

func TestLogout_PlainOfflineKeepsTheCredential(t *testing.T) {
	srv := newGrantServer(t, nil)
	f, _, out, errOut := testFactory(t, srv.URL)
	seedAccount(t, f, "work", "a@x.vn", time.Now().Add(30*time.Minute))
	f.Global.Profile = "work"
	srv.Close()

	err := run(NewCmdLogout(f))
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "could not revoke the session of work")
	assert.Contains(t, err.Error(), "sepay logout --local")
	assert.NotContains(t, out.String(), "Logged out")
	assert.Contains(t, errOut.String(), "could not revoke token server-side")
	mgr, _ := f.Credentials()
	cred, gerr := mgr.Get("work")
	require.NoError(t, gerr, "the credential that can still revoke the session is kept")
	assert.Equal(t, "rt_work", cred.RefreshToken)
}

func TestLogout_LocalRemovesWithoutRevoking(t *testing.T) {
	srv := newGrantServer(t, map[string]string{"rt_work": "at_work"})
	f, _, out, errOut := testFactory(t, srv.URL)
	seedAccount(t, f, "work", "a@x.vn", time.Now().Add(30*time.Minute))
	f.Global.Profile = "work"

	require.NoError(t, run(NewCmdLogout(f), "--local"))
	assert.Zero(t, srv.count("revoke")+srv.count("revoke-all")+srv.count("refresh"), "--local sends nothing")
	assert.True(t, srv.alive("rt_work"))
	assert.Contains(t, out.String(), "Removed profile work from this machine")
	assert.NotContains(t, out.String(), "Logged out")
	assert.Contains(t, errOut.String(), "stays live")
	assert.Contains(t, errOut.String(), "sepay auth revoke --all")
	mgr, _ := f.Credentials()
	assert.Empty(t, mgr.Hosts().Profiles)
}

func TestLogoutAll_JSONReportsEachProfile(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/tokens/revoke-all", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer at_good" {
			writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"revoked": 1}})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{"status": "error", "error_code": "service_unavailable", "message": "down"})
	})
	mux.HandleFunc("/oauth/revoke", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{"error": "temporarily_unavailable"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f, _, out, _ := testFactory(t, srv.URL)
	f.Global.Yes = true
	f.Global.Output = "json"
	future := time.Now().Add(30 * time.Minute)
	seedAccount(t, f, "good", "a@x.vn", future)
	seedAccount(t, f, "bad", "b@x.vn", future)

	err := run(NewCmdLogout(f), "--all")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not revoke the session of bad")
	assert.NotContains(t, err.Error(), "good")

	var got []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
	assert.ElementsMatch(t, []map[string]any{
		{"profile": "good", "host": "my.sepay.vn", "revoked": true, "removed": true},
		{"profile": "bad", "host": "my.sepay.vn", "revoked": false, "removed": false},
	}, got)
	mgr, _ := f.Credentials()
	assert.NotContains(t, mgr.Hosts().Profiles, "good")
	assert.Contains(t, mgr.Hosts().Profiles, "bad")
}
