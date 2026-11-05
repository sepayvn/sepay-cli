package auth

import (
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToken_PrintsStoredRefreshToken(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, out, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", []string{"read"})
	f.Global.Profile = "work"

	require.NoError(t, run(NewCmdToken(f)))
	assert.Equal(t, "rt_work", strings.TrimSpace(out.String()))
	assert.Zero(t, srv.revokeCount())
}

func TestToken_NotLoggedIn(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)

	err := run(NewCmdToken(f))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not logged in")
}

func TestToken_UnknownProfileIsUsageError(t *testing.T) {
	srv := newFakeOAuth(t)
	f, _, _, _ := testFactory(t, srv.URL)
	seedProfile(t, f, "work", []string{"read"})
	f.Global.Profile = "nosuch"

	err := run(NewCmdToken(f))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown profile "nosuch"`)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}
