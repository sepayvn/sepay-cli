package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
)

func TestStatus_ListsProfilesNoToken(t *testing.T) {
	f, _, out, _ := testFactory(t, "")
	seedProfile(t, f, "work", []string{"read"})
	seedProfile(t, f, "personal", []string{"sandbox"})

	require.NoError(t, run(NewCmdStatus(f)))
	s := out.String()
	assert.Contains(t, s, "work")
	assert.Contains(t, s, "personal")
	assert.NotContains(t, s, "at_", "token must never be shown")
	assert.Contains(t, s, "*")
}

func TestStatus_TableShowsScopes(t *testing.T) {
	f, _, out, _ := testFactory(t, "")
	seedProfile(t, f, "work", []string{"read", "write"})

	require.NoError(t, run(NewCmdStatus(f)))
	assert.Contains(t, out.String(), "read,write")
}

func TestStatus_JSON(t *testing.T) {
	f, _, out, _ := testFactory(t, "")
	seedProfile(t, f, "work", []string{"read"})
	f.Global.Output = "json"

	require.NoError(t, run(NewCmdStatus(f)))
	s := out.String()
	assert.Contains(t, s, `"profile": "work"`)
	assert.Contains(t, s, `"active": true`)
	assert.NotContains(t, s, "at_")
}

func TestStatus_ShowsEmail(t *testing.T) {
	f, _, out, _ := testFactory(t, "")
	seedProfile(t, f, "work", []string{"read"})
	mgr, _ := f.Credentials()
	mgr.Hosts().Profiles["work"].Email = "dev@shop.vn"
	require.NoError(t, mgr.Hosts().Save())

	require.NoError(t, run(NewCmdStatus(f)))
	assert.Contains(t, out.String(), "dev@shop.vn", "the EMAIL column shows the profile email")
}

func TestStatus_JSONIncludesEmail(t *testing.T) {
	f, _, out, _ := testFactory(t, "")
	seedProfile(t, f, "work", []string{"read"})
	mgr, _ := f.Credentials()
	mgr.Hosts().Profiles["work"].Email = "dev@shop.vn"
	require.NoError(t, mgr.Hosts().Save())
	f.Global.Output = "json"

	require.NoError(t, run(NewCmdStatus(f)))
	assert.Contains(t, out.String(), `"email": "dev@shop.vn"`)
}

func TestStatus_NotLoggedIn_Exit4(t *testing.T) {
	f, _, _, _ := testFactory(t, "")

	err := run(NewCmdStatus(f))
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "not logged in")
}
