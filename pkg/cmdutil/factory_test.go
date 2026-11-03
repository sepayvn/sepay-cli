package cmdutil

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
)

func factoryFor(dir, oauthURL string) *Factory {
	keyring.MockInit()
	ios, _, _, _ := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}
	f.Credentials = func() (*auth.Manager, error) {
		h, err := config.LoadHostsFrom(dir)
		if err != nil {
			return nil, err
		}
		return auth.NewManager(h), nil
	}
	f.NewOAuthClient = func(string) *auth.Client { return auth.NewClient(oauthURL, ClientID, nil) }
	return f
}

func TestApiBaseURL_Precedence(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	t.Setenv("SEPAY_SANDBOX_API_URL", "")
	cfg, err := config.Load()
	require.NoError(t, err)
	ios, _, _, _ := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}

	f.Global.Env = "sandbox"
	_, url, src := f.apiBaseURL(cfg)
	assert.Equal(t, "https://cli-api-sandbox.sepay.vn/v2", url)
	assert.Equal(t, "default", src)

	f.Global.Env = "production"
	_, prodDefault, _ := f.apiBaseURL(cfg)
	assert.Equal(t, "https://cli-api.sepay.vn/v2", prodDefault)
	assert.NotEqual(t, url, prodDefault, "sandbox and production resolve to different hosts")

	cfg.Set("sandbox.api_url", "http://localhost:8079/api/v2")
	f.Global.Env = "sandbox"
	_, url, src = f.apiBaseURL(cfg)
	assert.Equal(t, "http://localhost:8079/api/v2", url)
	assert.Equal(t, "config", src)

	t.Setenv("SEPAY_SANDBOX_API_URL", "http://localhost:9999/v2")
	_, url, src = f.apiBaseURL(cfg)
	assert.Equal(t, "http://localhost:9999/v2", url)
	assert.Equal(t, "SEPAY_SANDBOX_API_URL", src)

	f.Global.APIBase = "http://localhost:1/x"
	_, url, src = f.apiBaseURL(cfg)
	assert.Equal(t, "http://localhost:1/x", url)
	assert.Equal(t, "--api-base", src)
}

func TestPrintEnvBanner_OncePerRunAndSuppressed(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: "table"}}
	f.printEnvBanner("sandbox", "http://h/v2", "config")
	f.printEnvBanner("sandbox", "http://h/v2", "config")
	assert.Equal(t, 1, strings.Count(errOut.String(), "env: sandbox"), "banner prints once per run")

	ios2, _, _, err2 := iostreams.Test()
	f2 := &Factory{IOStreams: ios2, Global: &GlobalOptions{Output: "json"}}
	f2.printEnvBanner("sandbox", "http://h/v2", "config")
	assert.Empty(t, err2.String(), "banner suppressed for machine output")
}

func seedExpired(t *testing.T, dir string) {
	t.Helper()
	h, err := config.LoadHostsFrom(dir)
	require.NoError(t, err)
	h.Active = "work"
	h.Profiles["work"] = &config.Profile{
		Host:         "my.sepay.vn",
		Insecure:     true, // inline storage → no keychain in the test
		OAuthToken:   "old_at",
		RefreshToken: "old_rt",
		ExpiresAt:    time.Now().Add(-time.Hour).Format(time.RFC3339),
		Scopes:       []string{"sandbox"},
	}
	require.NoError(t, h.Save())
}

func TestResolveToken_RefreshesExpired(t *testing.T) {
	dir := t.TempDir()
	seedExpired(t, dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new_at","refresh_token":"new_rt","expires_in":3600,"scope":"sandbox"}`)
	}))
	defer srv.Close()

	f := factoryFor(dir, srv.URL)
	assert.Equal(t, "new_at", f.resolveToken(), "expired token must be refreshed before use")

	h, err := config.LoadHostsFrom(dir)
	require.NoError(t, err)
	assert.Equal(t, "new_at", h.Profiles["work"].OAuthToken)
	assert.Equal(t, "new_rt", h.Profiles["work"].RefreshToken)
}

// Two processes that find the same expired token must spend its refresh token once: the server
// rotates it on use, and the one that spends it second gets invalid_grant and revokes the profile.
func TestResolveToken_ConcurrentRefreshSpendsTokenOnce(t *testing.T) {
	dir := t.TempDir()
	seedExpired(t, dir)

	var mu sync.Mutex
	spent := map[string]bool{}
	exchanges := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		rt := r.PostForm.Get("refresh_token")
		mu.Lock()
		reused := spent[rt]
		spent[rt] = true
		exchanges++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if reused {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		// Hold the answer so the other process reaches the server before this pair is stored.
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, `{"access_token":"new_at","refresh_token":"new_rt","expires_in":3600,"scope":"sandbox"}`)
	}))
	defer srv.Close()

	got := make([]string, 2)
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = factoryFor(dir, srv.URL).resolveToken()
		}()
	}
	wg.Wait()

	assert.Equal(t, []string{"new_at", "new_at"}, got)
	assert.Equal(t, 1, exchanges, "the second process must reuse the stored pair")
	h, err := config.LoadHostsFrom(dir)
	require.NoError(t, err)
	assert.Equal(t, "new_rt", h.Profiles["work"].RefreshToken, "the rotated pair survives")
}

func TestResolveToken_RefreshFailureFallsBack(t *testing.T) {
	dir := t.TempDir()
	seedExpired(t, dir)

	// A failed refresh falls back to the stored token and lets the API return 401.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"temporarily_unavailable"}`)
	}))
	defer srv.Close()

	f := factoryFor(dir, srv.URL)
	assert.Equal(t, "old_at", f.resolveToken())
}

func TestCheckExplicitProfile_UnknownIsUsageError(t *testing.T) {
	dir := t.TempDir()
	seedExpired(t, dir) // creates profile "work"
	t.Setenv("SEPAY_PROFILE", "")
	f := factoryFor(dir, "")
	f.Global.Profile = "nope"

	err := f.CheckExplicitProfile()
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCodeForError(err), "must be a usage error (exit 2), not auth (exit 4)")
	assert.Contains(t, err.Error(), "unknown profile")
	assert.Contains(t, err.Error(), "nope")
}

func TestCheckExplicitProfile_ExistingPasses(t *testing.T) {
	dir := t.TempDir()
	seedExpired(t, dir) // profile "work"
	t.Setenv("SEPAY_PROFILE", "")
	f := factoryFor(dir, "")
	f.Global.Profile = "work"
	assert.NoError(t, f.CheckExplicitProfile())
}

func TestCheckExplicitProfile_NoExplicitProfilePasses(t *testing.T) {
	dir := t.TempDir()
	seedExpired(t, dir)
	t.Setenv("SEPAY_PROFILE", "")
	f := factoryFor(dir, "")
	assert.NoError(t, f.CheckExplicitProfile())
}

func TestResolveToken_UnknownProfileYieldsEmpty(t *testing.T) {
	dir := t.TempDir()
	seedExpired(t, dir)
	t.Setenv("SEPAY_PROFILE", "nope")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a missing credential must not trigger a refresh")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := factoryFor(dir, srv.URL)
	assert.Empty(t, f.resolveToken())
}

func TestWarnLoosePerms_AlwaysEmittedOnStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}

	for _, global := range []*GlobalOptions{
		{Output: "table"},
		{Output: "json"},
		{Output: "table", Quiet: true},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, config.HostsFile)
		require.NoError(t, os.WriteFile(path, []byte("active: work\n"), 0o600))
		require.NoError(t, os.Chmod(path, 0o644))

		ios, _, out, errOut := iostreams.Test()
		f := &Factory{IOStreams: ios, Global: global}
		f.warnLoosePerms(ios, dir)

		assert.Contains(t, errOut.String(), config.HostsFile,
			"loose-perms warning must survive output=%q quiet=%v", global.Output, global.Quiet)
		assert.Empty(t, out.String(), "warning must never reach stdout")
	}
}

// listen re-enters this path on every reconnect, so the window reopens during a session.
func TestResolveToken_LateRefreshDoesNotReplaceAReusedLabel(t *testing.T) {
	dir := t.TempDir()
	seedExpired(t, dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		h, err := config.LoadHostsFrom(dir)
		require.NoError(t, err)
		h.Profiles["work"] = &config.Profile{
			Host:         "my.sepay.vn",
			Insecure:     true,
			OAuthToken:   "other_at",
			RefreshToken: "other_rt",
			ExpiresAt:    time.Now().Add(time.Hour).Format(time.RFC3339),
			Scopes:       []string{"sandbox"},
		}
		require.NoError(t, h.Save())

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"stale_at","refresh_token":"stale_rt","expires_in":3600,"scope":"sandbox"}`)
	}))
	defer srv.Close()

	f := factoryFor(dir, srv.URL)
	assert.Equal(t, "other_at", f.resolveToken(),
		"the command runs under the account the label names now, not the exchange's result")

	h, err := config.LoadHostsFrom(dir)
	require.NoError(t, err)
	assert.Equal(t, "other_at", h.Profiles["work"].OAuthToken, "the stale token was not written")
	assert.Equal(t, "other_rt", h.Profiles["work"].RefreshToken)
}

func TestResolveToken_LateRefreshDoesNotUndoALogout(t *testing.T) {
	dir := t.TempDir()
	seedExpired(t, dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		h, err := config.LoadHostsFrom(dir)
		require.NoError(t, err)
		delete(h.Profiles, "work")
		h.Active = ""
		require.NoError(t, h.Save())

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"stale_at","refresh_token":"stale_rt","expires_in":3600,"scope":"sandbox"}`)
	}))
	defer srv.Close()

	f := factoryFor(dir, srv.URL)
	assert.NotEqual(t, "stale_at", f.resolveToken(), "the exchange's result must not be used")

	h, err := config.LoadHostsFrom(dir)
	require.NoError(t, err)
	assert.NotContains(t, h.Profiles, "work", "the logged-out profile stays gone")
}

func TestTokenForProfile_LogoutBeforeRefreshKeepsTheResolvedProfile(t *testing.T) {
	dir := t.TempDir()
	seedExpired(t, dir)

	f := factoryFor(dir, "http://127.0.0.1:1")
	f.Credentials = func() (*auth.Manager, error) {
		h, err := config.LoadHostsFrom(dir)
		if err != nil {
			return nil, err
		}
		// Another process logs the profile out after this one loaded it.
		other, err := config.LoadHostsFrom(dir)
		require.NoError(t, err)
		delete(other.Profiles, "work")
		other.Active = ""
		require.NoError(t, other.Save())
		return auth.NewManager(h), nil
	}

	host, token, err := f.TokenForProfile("work")
	require.NoError(t, err)
	assert.Equal(t, "my.sepay.vn", host)
	assert.Equal(t, "old_at", token, "an unreachable refresh leaves the stored token")

	h, err := config.LoadHostsFrom(dir)
	require.NoError(t, err)
	assert.NotContains(t, h.Profiles, "work", "the logged-out profile stays gone")
}

func TestResolveToken_StaleRefreshIsReportedOnlyWhenTheHostWasNotReached(t *testing.T) {
	cases := []struct {
		name  string
		reply func(w http.ResponseWriter)
		stale bool
		token string
	}{
		{"grant refused", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
		}, false, ""},
		{"host unavailable", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"temporarily_unavailable"}`)
		}, true, "old_at"},
		{"host throttling", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"slow_down"}`)
		}, true, "old_at"},
		{"refusal behind an HTML error page", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `<html><body>Bad Request</body></html>`)
		}, false, "old_at"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			seedExpired(t, dir)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { tc.reply(w) }))
			defer srv.Close()

			f := factoryFor(dir, srv.URL)
			assert.Equal(t, tc.token, f.resolveToken())
			if tc.stale {
				assert.ErrorIs(t, f.StaleTokenRefresh(), ErrTokenRefreshUnreachable)
			} else {
				assert.NoError(t, f.StaleTokenRefresh())
			}
		})
	}
}

func TestResolveToken_StoreFailureIsNotReportedAsUnreachable(t *testing.T) {
	dir := t.TempDir()
	seedExpired(t, dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"new_at","refresh_token":"new_rt","expires_in":3600,"scope":"read"}`)
	}))
	defer srv.Close()

	f := factoryFor(dir, srv.URL)
	require.NoError(t, os.Chmod(dir, 0o500)) // no writes: the exchange succeeds, the store cannot
	defer func() { _ = os.Chmod(dir, 0o700) }()

	assert.Equal(t, "old_at", f.resolveToken())
	assert.NoError(t, f.StaleTokenRefresh())
}

func TestIdempotencyKey_RejectsAMalformedKey(t *testing.T) {
	cases := []struct {
		name, key string
	}{
		{"too short", "abcdefg"},
		{"disallowed characters", "co khoang trang!"},
		{"too long", strings.Repeat("k", 256)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			f := &Factory{IOStreams: ios, Global: &GlobalOptions{IdempotencyKey: tc.key}}

			got, err := f.IdempotencyKey()

			require.Error(t, err)
			assert.Empty(t, got)
			assert.Equal(t, ExitUsage, ExitCodeForError(err))
			var flagErr *FlagError
			assert.ErrorAs(t, err, &flagErr)
		})
	}
}

func TestIdempotencyKey_AcceptsAValidKey(t *testing.T) {
	ios, _, _, _ := iostreams.Test()

	f := &Factory{IOStreams: ios, Global: &GlobalOptions{IdempotencyKey: " e2e-idem-1789282392 "}}
	got, err := f.IdempotencyKey()
	require.NoError(t, err)
	assert.Equal(t, "e2e-idem-1789282392", got)

	f = &Factory{IOStreams: ios, Global: &GlobalOptions{}}
	minted, err := f.IdempotencyKey()
	require.NoError(t, err)
	assert.NotEmpty(t, minted)
}

func TestPgCheckoutURL_Precedence(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	t.Setenv("SEPAY_SANDBOX_PG_CHECKOUT_URL", "")
	t.Setenv("SEPAY_SANDBOX_PG_API_URL", "")
	cfg, err := config.Load()
	require.NoError(t, err)
	ios, _, _, _ := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{}}

	f.Global.Env = "sandbox"
	_, url, src := f.pgCheckoutURL(cfg)
	assert.Equal(t, "https://pay-sandbox.sepay.vn", url)
	assert.Equal(t, "default", src)
	_, apiURL, _ := f.pgBaseURL(cfg)
	assert.NotEqual(t, apiURL, url, "checkout and REST API are different hosts")

	f.Global.Env = "production"
	_, prod, _ := f.pgCheckoutURL(cfg)
	assert.Equal(t, "https://pay.sepay.vn", prod)

	f.Global.Env = "sandbox"
	cfg.Set("sandbox.pg_checkout_url", "http://localhost:8079")
	_, url, src = f.pgCheckoutURL(cfg)
	assert.Equal(t, "http://localhost:8079", url)
	assert.Equal(t, "config", src)

	t.Setenv("SEPAY_SANDBOX_PG_CHECKOUT_URL", "http://localhost:9999")
	_, url, src = f.pgCheckoutURL(cfg)
	assert.Equal(t, "http://localhost:9999", url)
	assert.Equal(t, "SEPAY_SANDBOX_PG_CHECKOUT_URL", src)

	t.Setenv("SEPAY_SANDBOX_PG_CHECKOUT_URL", "")
	cfg.Set("sandbox.pg_checkout_url", "")
	t.Setenv("SEPAY_SANDBOX_PG_API_URL", "http://localhost:7777")
	_, url, src = f.pgCheckoutURL(cfg)
	assert.Equal(t, "https://pay-sandbox.sepay.vn", url, "a custom REST API host must not redirect checkout/init")
	assert.Equal(t, "default", src)
}

func seedLive(t *testing.T, dir string) {
	t.Helper()
	seedExpired(t, dir)
	h, err := config.LoadHostsFrom(dir)
	require.NoError(t, err)
	h.Profiles["work"].ExpiresAt = time.Now().Add(time.Hour).Format(time.RFC3339)
	require.NoError(t, h.Save())
}

func oauthReplying(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func TestRenewRejectedToken_RefreshesATokenTheClockCallsLive(t *testing.T) {
	dir := t.TempDir()
	seedLive(t, dir)
	srv := oauthReplying(http.StatusOK, `{"access_token":"new_at","refresh_token":"new_rt","expires_in":3600,"scope":"sandbox"}`)
	defer srv.Close()

	f := factoryFor(dir, srv.URL)
	assert.Equal(t, "new_at", f.renewRejectedToken(context.Background(), "old_at"))

	h, err := config.LoadHostsFrom(dir)
	require.NoError(t, err)
	assert.Equal(t, "new_rt", h.Profiles["work"].RefreshToken)
}

func TestRenewRejectedToken_UsesTheTokenAPeerStored(t *testing.T) {
	dir := t.TempDir()
	seedLive(t, dir)
	var exchanges int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { exchanges++ }))
	defer srv.Close()

	f := factoryFor(dir, srv.URL)
	assert.Equal(t, "old_at", f.renewRejectedToken(context.Background(), "an_older_at"))
	assert.Zero(t, exchanges)
}

func TestRefresh_DeadGrantIsDroppedLocally(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		dropped bool
	}{
		{"revoked, as SePay answers", http.StatusUnauthorized, `{"error":"invalid_request","hint":"Token has been revoked"}`, true},
		{"invalid_grant", http.StatusBadRequest, `{"error":"invalid_grant"}`, true},
		{"invalid_client is not the grant", http.StatusUnauthorized, `{"error":"invalid_client"}`, false},
		{"host unavailable", http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`, false},
		{"proxy error page", http.StatusBadRequest, `<html>Bad Request</html>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			seedLive(t, dir)
			srv := oauthReplying(tc.status, tc.body)
			defer srv.Close()

			f := factoryFor(dir, srv.URL)
			assert.Empty(t, f.renewRejectedToken(context.Background(), "old_at"))

			mgr, err := f.Credentials()
			require.NoError(t, err)
			cred, err := mgr.Get("work")
			require.NoError(t, err)
			assert.Equal(t, tc.dropped, cred.Revoked())
			if tc.dropped {
				assert.ErrorContains(t, f.NotLoggedInError(), "session revoked, run: sepay login")
			}
		})
	}
}

func TestRenewRejectedToken_UnreachableHostIsReportedStale(t *testing.T) {
	dir := t.TempDir()
	seedLive(t, dir)
	srv := oauthReplying(http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`)
	defer srv.Close()

	f := factoryFor(dir, srv.URL)
	assert.Empty(t, f.renewRejectedToken(context.Background(), "old_at"))
	assert.ErrorIs(t, f.StaleTokenRefresh(), ErrTokenRefreshUnreachable)
}

// Another process holds the refresh lock and the user presses Ctrl-C: automatic renewal stops at
// once instead of waiting out the lock, and spends no refresh token.
func TestRefreshIfExpired_InterruptStopsTheLockWait(t *testing.T) {
	dir := t.TempDir()
	seedExpired(t, dir)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()

	held, ok, err := config.TryLockFile(filepath.Join(dir, ".refresh.lock"))
	require.NoError(t, err)
	require.True(t, ok)
	defer held.Release()

	f := factoryFor(dir, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	f.SetContext(ctx)

	start := time.Now()
	_, token, err := f.TokenForProfile("work")
	require.NoError(t, err)
	assert.Equal(t, "old_at", token, "the stored token is used; the caller's request then stops on the same Ctrl-C")
	assert.Less(t, time.Since(start), 2*time.Second, "the wait must not run out the 30s refresh budget")
	assert.Zero(t, atomic.LoadInt32(&hits), "no refresh token is spent after Ctrl-C")
}
