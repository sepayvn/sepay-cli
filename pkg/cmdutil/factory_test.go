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

	"github.com/spf13/cobra"
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

func TestPrintEnvBanner(t *testing.T) {
	tests := []struct {
		name   string
		env    string
		source string
		want   string
	}{
		{"default host", "sandbox", "default", "env: sandbox\n"},
		{"production default", "production", "default", "env: production\n"},
		{"env var override", "sandbox", "SEPAY_SANDBOX_API_URL", "env: sandbox · host from SEPAY_SANDBOX_API_URL\n"},
		{"flag override", "production", "--api-base", "env: production · host from --api-base\n"},
		{"config override", "sandbox", "config", "env: sandbox · host from config\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ios, _, _, errOut := iostreams.Test()
			f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: "table"}}
			f.printEnvBanner(ios.ErrOut, tt.env, tt.source, "")
			assert.Equal(t, tt.want, errOut.String())
		})
	}
}

func TestPrintEnvBanner_OncePerRunAndSuppressed(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	f := &Factory{IOStreams: ios, Global: &GlobalOptions{Output: "table"}}
	f.printEnvBanner(ios.ErrOut, "sandbox", "config", "")
	f.printEnvBanner(ios.ErrOut, "sandbox", "config", "")
	assert.Equal(t, 1, strings.Count(errOut.String(), "env: sandbox"), "banner prints once per run")

	for _, g := range []*GlobalOptions{{Output: "json"}, {Output: "yaml"}, {Output: "table", Quiet: true}} {
		ios2, _, _, err2 := iostreams.Test()
		f2 := &Factory{IOStreams: ios2, Global: g}
		f2.printEnvBanner(ios2.ErrOut, "sandbox", "config", "")
		assert.Empty(t, err2.String(), "banner suppressed for %+v", *g)
	}
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

	// Built before the goroutines: factoryFor resets the shared keyring mock.
	factories := []*Factory{factoryFor(dir, srv.URL), factoryFor(dir, srv.URL)}
	got := make([]string, len(factories))
	var wg sync.WaitGroup
	for i, f := range factories {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = f.resolveToken()
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
		if !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.Profiles["work"] = &config.Profile{
			Host:         "my.sepay.vn",
			Insecure:     true,
			OAuthToken:   "other_at",
			RefreshToken: "other_rt",
			ExpiresAt:    time.Now().Add(time.Hour).Format(time.RFC3339),
			Scopes:       []string{"sandbox"},
		}
		assert.NoError(t, h.Save())

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
		if !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		delete(h.Profiles, "work")
		h.Active = ""
		assert.NoError(t, h.Save())

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
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory does not stop file creation on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
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
	url, src := f.pgCheckoutURL(cfg)
	assert.Equal(t, "https://pay-sandbox.sepay.vn", url)
	assert.Equal(t, "default", src)
	_, apiURL, _ := f.pgBaseURL(cfg)
	assert.NotEqual(t, apiURL, url, "checkout and REST API are different hosts")

	f.Global.Env = "production"
	prod, _ := f.pgCheckoutURL(cfg)
	assert.Equal(t, "https://pay.sepay.vn", prod)

	f.Global.Env = "sandbox"
	cfg.Set("sandbox.pg_checkout_url", "http://localhost:8079")
	url, src = f.pgCheckoutURL(cfg)
	assert.Equal(t, "http://localhost:8079", url)
	assert.Equal(t, "config", src)

	t.Setenv("SEPAY_SANDBOX_PG_CHECKOUT_URL", "http://localhost:9999")
	url, src = f.pgCheckoutURL(cfg)
	assert.Equal(t, "http://localhost:9999", url)
	assert.Equal(t, "SEPAY_SANDBOX_PG_CHECKOUT_URL", src)

	t.Setenv("SEPAY_SANDBOX_PG_CHECKOUT_URL", "")
	cfg.Set("sandbox.pg_checkout_url", "")
	t.Setenv("SEPAY_SANDBOX_PG_API_URL", "http://localhost:7777")
	url, src = f.pgCheckoutURL(cfg)
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
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { exchanges++ }))
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

// A product client mints its credential at the API host with the CLI token, so the banner names an
// overridden API host too, not only the product's own.
func TestProductBanner_NamesAnOverriddenAPIHost(t *testing.T) {
	builders := map[string]func(*Factory) error{
		"bankhub":  func(f *Factory) error { _, err := f.NewBankHubClient(); return err },
		"pg":       func(f *Factory) error { _, err := f.NewPGClient(); return err },
		"einvoice": func(f *Factory) error { _, err := f.NewEInvoiceClient(); return err },
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
			t.Setenv("SEPAY_SANDBOX_API_URL", "http://127.0.0.1:9000/v2")
			f := New()
			ios, _, _, errOut := iostreams.Test()
			f.IOStreams = ios
			f.Global.Output = "table"
			_ = build(f)
			assert.Equal(t, "env: sandbox · API host from SEPAY_SANDBOX_API_URL\n", errOut.String())
		})
	}
}

// --api-base looks like it sends the whole command there; a product command only mints there, so the
// banner says where its own requests still go.
func TestProductBanner_APIBaseNotesTheProductHostIsUnmoved(t *testing.T) {
	builders := []struct {
		name  string
		key   string
		build func(*Factory) error
	}{
		{"bankhub", "BANKHUB_API_URL", func(f *Factory) error { _, err := f.NewBankHubClient(); return err }},
		{"pg", "PG_API_URL", func(f *Factory) error { _, err := f.NewPGClient(); return err }},
		{"einvoice", "EINVOICE_API_URL", func(f *Factory) error { _, err := f.NewEInvoiceClient(); return err }},
	}
	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
			t.Setenv("SEPAY_SANDBOX_"+b.key, "")
			f := New()
			ios, _, _, errOut := iostreams.Test()
			f.IOStreams = ios
			f.Global.Output = "table"
			f.Global.APIBase = "http://127.0.0.1:38799/v2"
			_ = b.build(f)
			assert.Equal(t, "env: sandbox · API host from --api-base\n"+
				"note: --api-base moves only the API host; this command still calls its default host (set SEPAY_SANDBOX_"+b.key+" to move it)\n",
				errOut.String())
		})
		t.Run(b.name+" with its own host moved", func(t *testing.T) {
			t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
			t.Setenv("SEPAY_SANDBOX_"+b.key, "http://127.0.0.1:38800")
			f := New()
			ios, _, _, errOut := iostreams.Test()
			f.IOStreams = ios
			f.Global.Output = "table"
			f.Global.APIBase = "http://127.0.0.1:38799/v2"
			_ = b.build(f)
			assert.Equal(t, "env: sandbox · host from SEPAY_SANDBOX_"+b.key+" · API host from --api-base\n", errOut.String())
		})
	}
}

// checkout init posts to the hosted-checkout host as well as the PG API host, so the banner names
// where each came from and the --api-base note lists the variable of each one still on its default.
func TestProductBanner_CheckoutInitNamesTheCheckoutHost(t *testing.T) {
	tests := []struct {
		name, pgAPI, checkout, want string
	}{
		{
			"both default", "", "",
			"env: sandbox · API host from --api-base\n" +
				"note: --api-base moves only the API host; this command still calls its default host and checkout host " +
				"(set SEPAY_SANDBOX_PG_API_URL and SEPAY_SANDBOX_PG_CHECKOUT_URL to move them)\n",
		},
		{
			"PG API host moved", "http://127.0.0.1:38800", "",
			"env: sandbox · host from SEPAY_SANDBOX_PG_API_URL · API host from --api-base\n" +
				"note: --api-base moves only the API host; this command still calls its default checkout host " +
				"(set SEPAY_SANDBOX_PG_CHECKOUT_URL to move it)\n",
		},
		{
			"both moved", "http://127.0.0.1:38800", "http://127.0.0.1:38801",
			"env: sandbox · host from SEPAY_SANDBOX_PG_API_URL · checkout host from SEPAY_SANDBOX_PG_CHECKOUT_URL · API host from --api-base\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
			t.Setenv("SEPAY_SANDBOX_PG_API_URL", tt.pgAPI)
			t.Setenv("SEPAY_SANDBOX_PG_CHECKOUT_URL", tt.checkout)
			f := New()
			ios, _, _, errOut := iostreams.Test()
			f.IOStreams = ios
			f.Global.Output = "table"
			f.Global.APIBase = "http://127.0.0.1:38799/v2"
			root := &cobra.Command{Use: "sepay"}
			pgCmd := &cobra.Command{Use: "pg"}
			checkout := &cobra.Command{Use: "checkout"}
			initCmd := Annotate(&cobra.Command{Use: "init"}, map[string]string{AnnHost: HostPGCheckout})
			root.AddCommand(pgCmd)
			pgCmd.AddCommand(checkout)
			checkout.AddCommand(initCmd)
			f.BindCommand(initCmd)

			_, _ = f.NewPGClient()
			assert.Equal(t, tt.want, errOut.String())
		})
	}
}

// Every client builder routes its progress notes through progressf, so the gate cannot be bypassed by
// one builder keeping its own writer.
func TestClientBuilders_RouteProgressThroughTheGate(t *testing.T) {
	builders := map[string]func(*Factory) (func(string, ...any), error){
		"api": func(f *Factory) (func(string, ...any), error) {
			c, err := f.NewAPIClient()
			if err != nil {
				return nil, err
			}
			return c.Logf, nil
		},
		"bankhub": func(f *Factory) (func(string, ...any), error) {
			c, err := f.NewBankHubClient()
			if err != nil {
				return nil, err
			}
			return c.Logf, nil
		},
		"pg": func(f *Factory) (func(string, ...any), error) {
			c, err := f.NewPGClient()
			if err != nil {
				return nil, err
			}
			return c.Logf, nil
		},
		"einvoice": func(f *Factory) (func(string, ...any), error) {
			c, err := f.NewEInvoiceClient()
			if err != nil {
				return nil, err
			}
			return c.Logf, nil
		},
	}
	for name, build := range builders {
		for _, output := range []string{"table", "json"} {
			t.Run(name+" "+output, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("SEPAY_CONFIG_DIR", dir)
				keyring.MockInit()
				h, err := config.LoadHostsFrom(dir)
				require.NoError(t, err)
				h.Active = "work"
				h.Profiles["work"] = &config.Profile{
					Host: "my.sepay.vn", Insecure: true, OAuthToken: "at", RefreshToken: "rt",
					ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339), Scopes: []string{"sandbox"},
				}
				require.NoError(t, h.Save())
				f := New()
				ios, _, _, errOut := iostreams.Test()
				f.IOStreams = ios
				f.Global.Output = output
				f.Global.Quiet = true // keeps the env banner out of errOut
				logf, err := build(f)
				require.NoError(t, err)
				f.Global.Quiet = false
				logf("Rate limited (429).")
				if output == "table" {
					assert.Equal(t, "Rate limited (429).\n", errOut.String())
				} else {
					assert.Empty(t, errOut.String())
				}
			})
		}
	}
}

// A 429 or network retry note must not precede the error object machine output parses off stderr.
func TestProgressf_SilentUnderMachineOutputAndQuietUnlessVerbose(t *testing.T) {
	cases := []struct {
		g    GlobalOptions
		want string
	}{
		{GlobalOptions{Output: "table"}, "Rate limited (429).\n"},
		{GlobalOptions{Output: "table", Quiet: true}, ""},
		{GlobalOptions{Output: "json"}, ""},
		{GlobalOptions{Output: "yaml"}, ""},
		{GlobalOptions{Output: "json", Verbose: true}, "Rate limited (429).\n"},
		{GlobalOptions{Output: "table", Quiet: true, Verbose: true}, "Rate limited (429).\n"},
	}
	for _, c := range cases {
		ios, _, out, errOut := iostreams.Test()
		f := &Factory{IOStreams: ios, Global: &c.g}
		f.progressf("Rate limited (%d).", 429)
		assert.Equal(t, c.want, errOut.String(), "%+v", c.g)
		assert.Empty(t, out.String())
	}
}
