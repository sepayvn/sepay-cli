package auth

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
)

func TestLogin_DeviceFlow_StoresAndRedacts(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, out, errOut := testFactory(t, srv.URL)

	require.NoError(t, run(NewCmdLogin(f), "--scopes", "read,write"))

	assert.Contains(t, errOut.String(), "WDJB-MJHT")
	assert.Contains(t, out.String(), "✓ Logged in")
	assert.NotContains(t, out.String()+errOut.String(), "at_", "access token must not be printed")
	assert.NotContains(t, out.String()+errOut.String(), "rt_", "refresh token must not be printed")

	mgr, _ := f.Credentials()
	got, err := mgr.Get("default")
	require.NoError(t, err)
	assert.Equal(t, "at_read_write", got.AccessToken)
	assert.Equal(t, []string{"read", "write"}, mgr.Hosts().Profiles["default"].Scopes)
}

func TestLogin_NamesProfileByEmail(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.email = "dev@shop.vn"
	f, _, out, _ := testFactory(t, srv.URL)

	require.NoError(t, run(NewCmdLogin(f)))

	assert.Contains(t, out.String(), "✓ Logged in as dev@shop.vn")
	mgr, _ := f.Credentials()
	p := mgr.Hosts().Profiles["dev@shop.vn"]
	require.NotNil(t, p, "profile is keyed by the token email")
	assert.Equal(t, "dev@shop.vn", p.Email)
}

func TestLogin_SecondAccountBecomesActive(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)

	srv.email = "alice@x.vn"
	require.NoError(t, run(NewCmdLogin(f)))
	srv.email = "bob@y.vn"
	require.NoError(t, run(NewCmdLogin(f)))

	mgr, _ := f.Credentials()
	assert.Equal(t, "bob@y.vn", mgr.Hosts().Active, "the account just logged in as is active")
	assert.NotNil(t, mgr.Hosts().Profiles["alice@x.vn"], "the first account is still stored")
}

func TestLogin_StoresTheHostItAuthenticatedAgainst(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, out, _ := testFactory(t, srv.URL) // stands in for SEPAY_OAUTH_URL

	require.NoError(t, run(NewCmdLogin(f)))

	mgr, _ := f.Credentials()
	// The issuing host, not the --hostname default.
	assert.Equal(t, srv.URL, mgr.Hosts().Profiles["default"].Host)
	assert.NotContains(t, out.String(), "my.sepay.vn")
}

func TestLogin_HonorsSepayProfile(t *testing.T) {
	t.Setenv("SEPAY_PROFILE", "work")
	srv := newFakeOAuth(t)
	srv.email = "alice@x.vn"
	f, _, _, _ := testFactory(t, srv.URL)

	require.NoError(t, run(NewCmdLogin(f)))

	mgr, _ := f.Credentials()
	require.NotNil(t, mgr.Hosts().Profiles["work"], "SEPAY_PROFILE names the profile (not the email)")
	assert.Equal(t, "work", mgr.Hosts().Active)
	assert.Equal(t, "alice@x.vn", mgr.Hosts().Profiles["work"].Email, "email is still recorded")
}

func TestLogin_AliasOverridesEmailName(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.email = "dev@shop.vn"
	f, _, _, _ := testFactory(t, srv.URL)
	f.Global.Profile = "work"

	require.NoError(t, run(NewCmdLogin(f)))

	mgr, _ := f.Credentials()
	require.NotNil(t, mgr.Hosts().Profiles["work"], "--profile aliases the profile name")
	assert.Equal(t, "dev@shop.vn", mgr.Hosts().Profiles["work"].Email, "email still recorded under the alias")
}

func TestLogin_DefaultScopesAreCapabilities(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)

	require.NoError(t, run(NewCmdLogin(f)))

	assert.Equal(t, "read webhook:write bank-account:write paycode:write pg:read einvoice:read", srv.deviceScopeValue())
	assert.NotContains(t, srv.deviceScopeValue(), "sandbox")
}

func TestLogin_UsesPlainVerificationURI(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, errOut := testFactory(t, srv.URL)

	require.NoError(t, run(NewCmdLogin(f)))

	// The pre-filled ?user_code= URL is never printed.
	assert.Contains(t, errOut.String(), "https://my.sepay.vn/activate")
	assert.NotContains(t, errOut.String(), "?user_code=")
}

func TestLogin_DeviceFlow_PendingThenApproved(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.pending = 1 // interval is 0 in the fake, so this returns promptly
	f, _, out, _ := testFactory(t, srv.URL)

	require.NoError(t, run(NewCmdLogin(f)))
	assert.Contains(t, out.String(), "✓ Logged in")
}

func TestLogin_FollowsAVietnameseLocale(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.email = "dev@shop.vn"
	f, _, out, errOut := testFactory(t, srv.URL)
	t.Setenv("SEPAY_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "vi_VN.UTF-8")

	require.NoError(t, run(NewCmdLogin(f)))
	assert.Contains(t, errOut.String(), "Mã của bạn: WDJB-MJHT")
	assert.Contains(t, errOut.String(), "Truy cập: ")
	assert.Contains(t, out.String(), "✓ Đã đăng nhập: dev@shop.vn")
}

func TestLogin_JSONIsTheSameInEveryLanguage(t *testing.T) {
	srv := newFakeOAuth(t)
	var got []string
	for _, lang := range []string{"en", "vi"} {
		f, _, out, _ := testFactory(t, srv.URL)
		t.Setenv("SEPAY_LANG", lang)
		f.Global.Output = "json"
		require.NoError(t, run(NewCmdLogin(f)))
		got = append(got, out.String())
	}
	assert.Equal(t, got[0], got[1])
}

func TestLoginText_MachineOutputStaysEnglish(t *testing.T) {
	t.Setenv("SEPAY_LANG", "vi")
	for _, output := range []string{"json", "yaml"} {
		f := &cmdutil.Factory{Global: &cmdutil.GlobalOptions{Output: output}}
		assert.Equal(t, loginTextEN.denied, currentLoginText(f).denied, output)
	}
	f := &cmdutil.Factory{Global: &cmdutil.GlobalOptions{Output: "table"}}
	assert.Equal(t, loginTextVI.denied, currentLoginText(f).denied)
}

func TestLoginPollError_SpeaksTheScreenLanguage(t *testing.T) {
	denied := &auth.Error{Code: auth.CodeAccessDenied}
	require.EqualError(t, loginPollError(denied, loginTextEN), "authorization denied")
	require.EqualError(t, loginPollError(denied, loginTextVI), "yêu cầu đăng nhập đã bị từ chối")
	expired := &auth.Error{Code: auth.CodeExpiredToken}
	assert.EqualError(t, loginPollError(expired, loginTextVI), "mã đăng nhập đã hết hạn, chạy lại: sepay login")
}

func TestLogin_WithToken_CIPath(t *testing.T) {
	srv := newFakeOAuth(t)
	f, in, out, _ := testFactory(t, srv.URL)
	in.WriteString("rt_ci_secret\n")

	require.NoError(t, run(NewCmdLogin(f), "--with-token", "--scopes", "read"))
	assert.Contains(t, out.String(), "✓ Logged in")

	mgr, _ := f.Credentials()
	got, err := mgr.Get("default")
	require.NoError(t, err)
	assert.Equal(t, "read", got.Scope)

	sent, value := srv.refreshScopeValue()
	assert.True(t, sent, "an explicit --scopes must still narrow the grant")
	assert.Equal(t, "read", value)
}

// A refresh grant can only narrow, so --with-token must not request the default (widest) scope set.
func TestLogin_WithToken_DefaultOmitsScope(t *testing.T) {
	srv := newFakeOAuth(t)
	f, in, out, _ := testFactory(t, srv.URL)
	in.WriteString("rt_ci_secret\n")

	require.NoError(t, run(NewCmdLogin(f), "--with-token"))
	assert.Contains(t, out.String(), "✓ Logged in")

	sent, _ := srv.refreshScopeValue()
	assert.False(t, sent, "no --scopes must omit the scope parameter, which keeps the original grant")

	mgr, _ := f.Credentials()
	assert.Equal(t, []string{"sandbox"}, mgr.Hosts().Profiles["default"].Scopes)
}

func TestLogin_WithToken_WideningScopesIsAScopeFailure(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.grantedScopes = "read"
	f, in, _, _ := testFactory(t, srv.URL)
	in.WriteString("rt_ci_secret\n")

	err := run(NewCmdLogin(f), "--with-token", "--scopes", "read,webhook:write")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitScope, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "cannot widen beyond what was granted at login")
	assert.Contains(t, err.Error(), "drop --scopes")
	assert.Contains(t, err.Error(), "webhook:write", "the server's description names the offending scope")

	mgr, _ := f.Credentials()
	assert.Empty(t, mgr.Hosts().Profiles, "a refused exchange stores nothing")
}

// Guards against the widening check passing by refusing everything.
func TestLogin_WithToken_NarrowingWithinTheGrantSucceeds(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.grantedScopes = "read webhook:write"
	f, in, out, _ := testFactory(t, srv.URL)
	in.WriteString("rt_ci_secret\n")

	require.NoError(t, run(NewCmdLogin(f), "--with-token", "--scopes", "read"))
	assert.Contains(t, out.String(), "✓ Logged in")

	mgr, _ := f.Credentials()
	assert.Equal(t, []string{"read"}, mgr.Hosts().Profiles["default"].Scopes)
}

func TestLogin_WithToken_DefaultKeepsTheWholeGrant(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.grantedScopes = "read webhook:write"
	f, in, _, _ := testFactory(t, srv.URL)
	in.WriteString("rt_ci_secret\n")

	require.NoError(t, run(NewCmdLogin(f), "--with-token"))

	mgr, _ := f.Credentials()
	assert.Equal(t, []string{"read", "webhook:write"}, mgr.Hosts().Profiles["default"].Scopes)
}

func TestLogin_WithToken_EmptyStdin(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)

	err := run(NewCmdLogin(f), "--with-token")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestLogin_InsecureStorageWarns(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, out, errOut := testFactory(t, srv.URL)

	require.NoError(t, run(NewCmdLogin(f), "--insecure-storage"))
	assert.Contains(t, out.String(), "✓ Logged in")
	assert.Contains(t, errOut.String(), "! your login was saved in a file only your user account can read: ")
	assert.NotContains(t, errOut.String(), "password store", "a file the user asked for is not blamed on the keychain")

	mgr, _ := f.Credentials()
	assert.True(t, mgr.Hosts().Profiles["default"].Insecure)
}

// No keychain means the same outcome as --insecure-storage, with a yellow reason the user can act on.
func TestLogin_NoKeychainFallsBackToFile(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, out, errOut := testFactory(t, srv.URL)
	f.IOStreams.SetColorEnabled(true)
	keyring.MockInitWithError(errors.New("no secret service"))
	t.Cleanup(keyring.MockInit)

	require.NoError(t, run(NewCmdLogin(f)))
	assert.Contains(t, out.String(), "Logged in as default")

	mgr, _ := f.Credentials()
	hostsPath := filepath.Join(mgr.Hosts().Dir(), config.HostsFile)
	assert.Contains(t, errOut.String(), "\x1b[33m! this computer has no password store sepay can use (OS keychain), so your login was saved\x1b[0m\n"+
		"\x1b[33m  in a file only your user account can read: "+hostsPath+"\x1b[0m\n"+
		"\x1b[33m  nothing else to do; on a shared computer, run sepay logout when you are done\x1b[0m\n")
	assert.NotContains(t, errOut.String(), "--insecure-storage", "the user is never told to re-run with a flag")

	p := mgr.Hosts().Profiles["default"]
	assert.True(t, p.Insecure)
	assert.NotEmpty(t, p.RefreshToken)
}

func TestLogin_JSONOutput(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, out, _ := testFactory(t, srv.URL)
	f.Global.Output = "json"

	require.NoError(t, run(NewCmdLogin(f), "--scopes", "sandbox"))
	s := out.String()
	assert.Contains(t, s, `"logged_in": true`)
	assert.Contains(t, s, `"host": "`+srv.URL+`"`)
	assert.NotContains(t, s, "at_", "no token in json output")
}

func TestLogin_RefusesToOpenAnUntrustedVerificationURI(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.verificationURI = "https://evil.example.com/activate"
	f, _, _, errOut := testFactory(t, srv.URL)

	require.NoError(t, run(NewCmdLogin(f)))

	assert.Contains(t, errOut.String(), "not opening a browser")
	assert.Contains(t, errOut.String(), "https://evil.example.com/activate",
		"the URL is still shown; refusing to auto-open must not hide it")
}

func TestLogin_AcceptsTheCanonicalVerificationURI(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, errOut := testFactory(t, srv.URL)

	require.NoError(t, run(NewCmdLogin(f)))

	assert.NotContains(t, errOut.String(), "not opening a browser")
	assert.Contains(t, errOut.String(), "https://my.sepay.vn/activate")
}

func TestLogin_WithToken_RefusesATerminal(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	f.IOStreams.SetStdinTTY(true)

	err := run(NewCmdLogin(f), "--with-token")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "stdin is a terminal")
}

// The consent screen renders the intersection with PRODUCTION_ACTIONS, so an unlisted scope can never be granted.
func TestKnownScopes_AreServerProductionActions(t *testing.T) {
	productionActions := []string{"read", "webhook:write", "bank-account:write", "paycode:write", "va:write", "order:write", "refund:write", "pg:read", "einvoice:read"}

	for _, s := range knownScopes {
		assert.Contains(t, productionActions, s, "the server does not grant %q", s)
	}
	assert.NotContains(t, knownScopes, "va:write")
	assert.NotContains(t, knownScopes, "order:write")
	assert.NotContains(t, knownScopes, "refund:write")
	assert.Contains(t, knownScopes, "pg:read", "pg order list/get need it on production")
	assert.Contains(t, knownScopes, "einvoice:read", "einvoice invoice list/get need it on production")
}
