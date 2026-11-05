package auth

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
)

func TestSessions_Lists(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.sessions = []auth.TokenSession{
		{ID: "sess_1", Scopes: []string{"read"}, Current: true, CreatedAt: "2026-07-14T00:00:00Z"},
		{ID: "sess_2", Scopes: []string{"read", "va:write"}, CreatedAt: "2026-07-13T00:00:00Z"},
	}
	f, _, out, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", []string{"read"})

	require.NoError(t, run(NewCmdSessions(f)))
	s := out.String()
	assert.Contains(t, s, "sess_1")
	assert.Contains(t, s, "sess_2")
	assert.Contains(t, s, "*", "the current session is marked")
	assert.NotContains(t, s, "at_", "token values must never be shown")
}

func TestSessions_JSON(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.sessions = []auth.TokenSession{{ID: "sess_1", Scopes: []string{"read"}, Current: true}}
	f, _, out, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", []string{"read"})
	f.Global.Output = "json"

	require.NoError(t, run(NewCmdSessions(f)))
	s := out.String()
	assert.Contains(t, s, `"id": "sess_1"`)
	assert.Contains(t, s, `"current": true`)
}

func TestSessions_NotLoggedIn(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)

	err := run(NewCmdSessions(f))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not logged in")
}

func TestSessions_Unauthorized_Exit4(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.tokensStatus = 401
	f, _, _, errOut := testFactory(t, srv.URL)
	seedProfile(t, f, "work", []string{"read"})

	err := run(NewCmdSessions(f))
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitAuth, cmdutil.HandleError(f, err))
	assert.Contains(t, errOut.String(), "session expired, run: sepay login")
}

// Under --json the 401 is the error object alone; a prose line before it breaks the parse.
func TestSessions_UnauthorizedJSONIsAnErrorObject(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.tokensStatus = 401
	f, out, _, errOut := testFactory(t, srv.URL)
	f.Global.Output = "json"
	seedProfile(t, f, "work", []string{"read"})

	err := run(NewCmdSessions(f))
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitAuth, cmdutil.HandleError(f, err))
	var got map[string]any
	require.NoError(t, json.Unmarshal(errOut.Bytes(), &got), errOut.String())
	assert.Equal(t, "unauthenticated", got["error_code"])
	assert.Equal(t, "session expired, run: sepay login", got["message"])
	assert.Empty(t, out.String())
}

func TestSessions_FitsANarrowTerminal(t *testing.T) {
	srv := newFakeOAuth(t)
	srv.sessions = []auth.TokenSession{{
		ID:        "sess_1",
		Scopes:    []string{"read", "webhook:write", "bank-account:write", "va:write", "refund:write", "order:write"},
		CreatedAt: "2026-07-14T00:00:00Z", LastUsedAt: "2026-07-15T00:00:00Z",
	}}
	f, _, out, _ := testFactory(t, srv.URL)
	f.IOStreams.SetTerminalWidth(80)
	seedProfile(t, f, "work", []string{"read"})

	require.NoError(t, run(NewCmdSessions(f)))
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		assert.LessOrEqual(t, utf8.RuneCountInString(l), 80, "line %q overflows", l)
	}
	assert.Contains(t, out.String(), "sess_1", "the id stays whole for auth revoke --token")
	assert.Contains(t, out.String(), "2026-07-14T00:00:00Z", "dates are never cut")
	assert.Contains(t, out.String(), "active")
	assert.Contains(t, out.String(), "read,webhook:writ…", "only the scopes give way")
}
