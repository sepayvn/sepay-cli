package auth

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/zalando/go-keyring"
)

func TestSwitch_ChangesActive(t *testing.T) {
	f, _, out, _ := testFactory(t, "")
	seedProfile(t, f, "work", []string{"read"})
	seedProfile(t, f, "personal", []string{"sandbox"})

	require.NoError(t, run(NewCmdSwitch(f), "personal"))
	assert.Contains(t, out.String(), "personal")

	mgr, _ := f.Credentials()
	assert.Equal(t, "personal", mgr.Hosts().Active)
}

// Retrying a wrong name never succeeds, so exit 1 (retryable) would loop a script.
func TestAuth_UnknownProfileIsAUsageError(t *testing.T) {
	cases := map[string]func(*cmdutil.Factory) error{
		"switch": func(f *cmdutil.Factory) error { return run(NewCmdSwitch(f), "ghost") },
		"revoke": func(f *cmdutil.Factory) error { return run(NewCmdRevoke(f), "ghost") },
		"logout": func(f *cmdutil.Factory) error {
			f.Global.Profile = "ghost"
			return run(NewCmdLogout(f))
		},
	}
	for name, invoke := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newFakeOAuth(t)
			f, _, out, errOut := testFactory(t, srv.URL)
			seedProfile(t, f, "work", []string{"read"})
			f.Global.Output = "json"

			err := invoke(f)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `unknown profile "ghost" (see: sepay auth status)`)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.HandleError(f, err))
			assert.Empty(t, out.String(), "stdout stays data-only")

			var got struct {
				Status    string `json:"status"`
				ErrorCode string `json:"error_code"`
				ExitCode  int    `json:"exit_code"`
			}
			require.NoError(t, json.Unmarshal(errOut.Bytes(), &got), errOut.String())
			assert.Equal(t, "error", got.Status)
			assert.Equal(t, "usage_error", got.ErrorCode)
			assert.Equal(t, 2, got.ExitCode)
		})
	}
}

func TestSwitch_NoArgNonInteractive(t *testing.T) {
	f, _, _, _ := testFactory(t, "")
	seedProfile(t, f, "work", []string{"read"})

	err := run(NewCmdSwitch(f))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a profile name is required")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestSwitch_NoArgNoProfiles(t *testing.T) {
	f, _, _, _ := testFactory(t, "")

	err := run(NewCmdSwitch(f))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no stored profiles")
}

func TestLogout_RevokesAndForgets(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, out, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", []string{"read"})
	f.Global.Profile = "work"

	require.NoError(t, run(NewCmdLogout(f)))
	assert.Contains(t, out.String(), "Logged out")
	assert.Equal(t, 1, srv.revokeCount(), "logout must revoke server-side")
	assert.Equal(t, []string{"rt_work"}, srv.revoked, "must send the refresh token so the grant is revoked even if the access token has expired")

	mgr, _ := f.Credentials()
	_, err := mgr.Get("work")
	assert.ErrorIs(t, err, auth.ErrNotFound, "logout must forget the credential locally")
}

func TestLogout_All(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	f.Global.Yes = true
	seedProfile(t, f, "work", []string{"read"})
	seedProfile(t, f, "personal", []string{"sandbox"})

	require.NoError(t, run(NewCmdLogout(f), "--all"))
	assert.Equal(t, 2, srv.revokeAllCalls(), "logout --all revokes every session of each account server-side")

	mgr, _ := f.Credentials()
	assert.Empty(t, mgr.Hosts().Profiles)
}

func TestLogout_RevokeFailureKeepsTheCredential(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.revokeStatus = 500 // server-side revoke fails
	f, _, out, errOut := testFactory(t, srv.URL)
	seedProfile(t, f, "work", []string{"read"})
	f.Global.Profile = "work"

	err := run(NewCmdLogout(f))
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
	assert.Contains(t, errOut.String(), "could not revoke token server-side")
	assert.NotContains(t, out.String(), "Logged out")

	mgr, _ := f.Credentials()
	_, gerr := mgr.Get("work")
	assert.NoError(t, gerr)
}

// A file-fallback profile fails every keyring call, so warning there would cry wolf. The warn direction is
// covered by TestStore_EvictProfileReportsAKeychainFailure.
func TestLogout_FileFallbackProfileGetsNoEvictionWarning(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, out, errOut := testFactory(t, srv.URL)
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	f.Config = config.Load

	mgr, err := f.Credentials()
	require.NoError(t, err)
	_, err = mgr.Set("work", "my.sepay.vn", &auth.Credential{AccessToken: "at", RefreshToken: "rt"}, []string{"read"}, true, "")
	require.NoError(t, err)
	f.Global.Profile = "work"

	keyring.MockInitWithError(errors.New("no secret service"))

	require.NoError(t, run(NewCmdLogout(f)))
	assert.Contains(t, out.String(), "Logged out")
	assert.NotContains(t, errOut.String(), "could not clear the cached product credentials")
}

// status, token and --map=json must agree that nothing is active.
func TestLogout_LeavesNoActiveProfile_EverySurfaceAgrees(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, out, errOut := testFactory(t, srv.URL)
	seedProfile(t, f, "work", []string{"read"})
	seedProfile(t, f, "personal", []string{"read"})
	mgr, _ := f.Credentials()
	require.NoError(t, mgr.SetActive("work"))
	f.Global.Profile = "work"

	require.NoError(t, run(NewCmdLogout(f)))
	f.Global.Profile = "" // the shell's next command carries no --profile

	require.NoError(t, run(NewCmdStatus(f)))
	assert.Contains(t, errOut.String(), "no active profile; run: sepay auth switch",
		"the table has no marker to show, so it has to say so")
	assert.Contains(t, out.String(), "personal", "the surviving profile is still listed")

	err := run(NewCmdToken(f))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sepay auth switch",
		"a stored profile needs switching to, not a fresh login")
	assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(err))
}

func TestRevoke_All(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	f.Global.Yes = true
	seedProfile(t, f, "work", []string{"read"})
	seedProfile(t, f, "personal", []string{"sandbox"})

	require.NoError(t, run(NewCmdRevoke(f), "--all"))
	assert.Equal(t, 1, srv.revokeAllCalls(), "--all revokes every session of the active account in one call")

	mgr, _ := f.Credentials()
	assert.Len(t, mgr.Hosts().Profiles, 2)
}

func TestRevoke_TokenID(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, out, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", []string{"read"})

	require.NoError(t, run(NewCmdRevoke(f), "--token", "sess_1"))
	assert.Contains(t, out.String(), "Revoked session sess_1")
	assert.Equal(t, []string{"sess_1"}, srv.revokedSessionIDs())
}

func TestRevoke_MutuallyExclusive(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", []string{"read"})

	err := run(NewCmdRevoke(f), "--all", "--token", "sess_1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestRevoke_ProfileKeepsLocal(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", []string{"read"})

	require.NoError(t, run(NewCmdRevoke(f), "work"))
	assert.Equal(t, 1, srv.revokeCount())

	mgr, _ := f.Credentials()
	_, err := mgr.Get("work")
	require.NoError(t, err)
}

func storedRevoked(t *testing.T, f *cmdutil.Factory, name string) bool {
	t.Helper()
	mgr, err := f.Credentials()
	require.NoError(t, err)
	cred, err := mgr.Get(name)
	require.NoError(t, err)
	return cred.Revoked()
}

func TestRevoke_ProfileIsShownRevoked(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, out, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", []string{"read"})

	require.NoError(t, run(NewCmdRevoke(f), "work"))
	assert.True(t, storedRevoked(t, f, "work"))

	out.Reset()
	require.NoError(t, run(NewCmdStatus(f)))
	assert.Contains(t, out.String(), "revoked, run: sepay login")
}

func TestRevoke_AllMarksEveryProfileOfTheAccount(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	f.Global.Yes = true
	mgr, err := f.Credentials()
	require.NoError(t, err)
	for _, p := range []struct{ name, host, email string }{
		{"work", "my.sepay.vn", "dev@example.com"},
		{"alias", "https://my.sepay.vn", "dev@example.com"},
		{"other", "my.sepay.vn", "someone@example.com"},
	} {
		_, err := mgr.Set(p.name, p.host, &auth.Credential{AccessToken: "at_" + p.name, RefreshToken: "rt_" + p.name}, nil, false, p.email)
		require.NoError(t, err)
	}
	require.NoError(t, mgr.SetActive("work"))

	require.NoError(t, run(NewCmdRevoke(f), "--all"))
	assert.True(t, storedRevoked(t, f, "work"))
	assert.True(t, storedRevoked(t, f, "alias"))
	assert.False(t, storedRevoked(t, f, "other"))
}

func TestRevoke_TokenIDMarksOnlyTheCurrentSession(t *testing.T) {
	for _, tc := range []struct {
		id      string
		revoked bool
	}{{"sess_current", true}, {"sess_other", false}} {
		t.Run(tc.id, func(t *testing.T) {
			srv := newFakeOAuth(t)
			srv.sessions = []auth.TokenSession{{ID: "sess_current", Current: true}, {ID: "sess_other"}}
			f, _, _, _ := testFactory(t, srv.URL)
			seedProfile(t, f, "work", []string{"read"})

			require.NoError(t, run(NewCmdRevoke(f), "--token", tc.id))
			assert.Equal(t, tc.revoked, storedRevoked(t, f, "work"))
		})
	}
}
