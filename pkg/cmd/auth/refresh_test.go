package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefresh_AddsScope(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, out, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", "my.sepay.vn", []string{"read"})
	f.Global.Profile = "work"

	require.NoError(t, run(NewCmdRefresh(f), "--scopes", "webhook:write"))
	assert.Contains(t, out.String(), "Refreshed token")

	mgr, _ := f.Credentials()
	assert.Equal(t, []string{"read", "webhook:write"}, mgr.Hosts().Profiles["work"].Scopes)
}

func TestRefresh_ResetScopes(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", "my.sepay.vn", []string{"sandbox", "read"})
	f.Global.Profile = "work"

	require.NoError(t, run(NewCmdRefresh(f), "--reset-scopes", "--scopes", "read"))

	mgr, _ := f.Credentials()
	assert.Equal(t, []string{"read"}, mgr.Hosts().Profiles["work"].Scopes)
}

func TestRefresh_RemoveScopes(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", "my.sepay.vn", []string{"read", "webhook:write", "va:write"})
	f.Global.Profile = "work"

	require.NoError(t, run(NewCmdRefresh(f), "--remove-scopes", "va:write"))

	mgr, _ := f.Credentials()
	assert.Equal(t, []string{"read", "webhook:write"}, mgr.Hosts().Profiles["work"].Scopes)
}

func TestRefresh_NotLoggedIn(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)

	err := run(NewCmdRefresh(f))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not logged in")
}

func TestRefresh_RejectsUnknownScope(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", "my.sepay.vn", []string{"read"})
	f.Global.Profile = "work"

	err := run(NewCmdRefresh(f), "--scopes", "webhook:writ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown scope webhook:writ")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

// Clearing every scope would omit scope, which the server reads as "keep the original grant".
func TestRefresh_RejectsClearingEveryScope(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", "my.sepay.vn", []string{"read", "webhook:write"})
	f.Global.Profile = "work"

	err := run(NewCmdRefresh(f), "--remove-scopes", "read,webhook:write")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clear every scope")

	mgr, _ := f.Credentials()
	assert.Equal(t, []string{"read", "webhook:write"}, mgr.Hosts().Profiles["work"].Scopes)
}

func TestRefresh_RenewsEmptyGrant(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", "my.sepay.vn", nil)
	f.Global.Profile = "work"

	require.NoError(t, run(NewCmdRefresh(f)))
}

func TestRefresh_KeepsServerScopeItDoesNotKnow(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", "my.sepay.vn", []string{"read", "future:write"})
	f.Global.Profile = "work"

	require.NoError(t, run(NewCmdRefresh(f)))
}

func TestRefresh_RejectsUnknownRemoveScope(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", "my.sepay.vn", []string{"read", "webhook:write"})
	f.Global.Profile = "work"

	err := run(NewCmdRefresh(f), "--remove-scopes", "webook:write")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown scope webook:write")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))

	mgr, _ := f.Credentials()
	assert.Equal(t, []string{"read", "webhook:write"}, mgr.Hosts().Profiles["work"].Scopes)
}

func TestRefresh_RemovesServerScopeItDoesNotKnow(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", "my.sepay.vn", []string{"read", "future:write"})
	f.Global.Profile = "work"

	require.NoError(t, run(NewCmdRefresh(f), "--remove-scopes", "future:write"))

	mgr, _ := f.Credentials()
	assert.Equal(t, []string{"read"}, mgr.Hosts().Profiles["work"].Scopes)
}

// Exit 4 would send a wrapper script into the device flow over a typo.
func TestRefresh_UnknownProfileIsUsageError(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", "my.sepay.vn", []string{"read"})
	f.Global.Profile = "nosuch"

	err := run(NewCmdRefresh(f))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown profile "nosuch"`)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

// Ctrl-C while another sepay process holds the refresh lock ends the wait at once and spends nothing.
func TestRefresh_InterruptStopsTheLockWait(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hits, 1) }))
	t.Cleanup(srv.Close)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", "my.sepay.vn", []string{"read"})
	f.Global.Profile = "work"

	mgr, err := f.Credentials()
	require.NoError(t, err)
	held, ok, err := config.TryLockFile(filepath.Join(mgr.Hosts().Dir(), ".refresh.lock"))
	require.True(t, ok)
	require.NoError(t, err)
	defer held.Release()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	cmd := NewCmdRefresh(f)
	cmd.SetArgs(nil)
	start := time.Now()
	err = cmd.ExecuteContext(ctx)

	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 2*time.Second, "the wait must not run out the 30s lock timeout")
	assert.Zero(t, atomic.LoadInt32(&hits), "no refresh token is spent after Ctrl-C")
}
