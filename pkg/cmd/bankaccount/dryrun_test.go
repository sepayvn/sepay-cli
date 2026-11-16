package bankaccount

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const dryXID = "11111111-2222-4333-8444-555555555555"

func readOnlyAccount(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("dry run sent %s %s", r.Method, r.URL.Path)
			return
		}
		if r.URL.Path != "/bank-accounts/"+dryXID {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(oneAccount))
	}
}

func TestDryRunUpdate_Label(t *testing.T) {
	f, out, _ := testFactory(t, readOnlyAccount(t))
	f.Global.Output = "json"
	f.Global.Env = config.EnvProduction
	require.NoError(t, run(NewCmdBankAccount(f), "update", dryXID, "--label", "New", "--dry-run"))
	var got struct {
		Request struct {
			Method string         `json:"method"`
			Path   string         `json:"path"`
			Body   map[string]any `json:"body"`
		} `json:"request"`
		Before map[string]any `json:"before"`
		After  map[string]any `json:"after"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, http.MethodPut, got.Request.Method)
	assert.Equal(t, "/bank-accounts/"+dryXID, got.Request.Path)
	assert.Equal(t, map[string]any{"label": "New"}, got.Request.Body)
	assert.Equal(t, "Shop", got.Before["label"])
	assert.Equal(t, "New", got.After["label"])
	got.Before["label"], got.After["label"] = nil, nil
	assert.Equal(t, got.Before, got.After, "only the label changes")
}

func TestDryRunUpdate_TableAndUnknownAccount(t *testing.T) {
	f, out, _ := testFactory(t, readOnlyAccount(t))
	require.NoError(t, run(NewCmdBankAccount(f), "update", dryXID, "--label", "New", "--dry-run"))
	assert.Regexp(t, `(?m)^label +Shop +New$`, out.String(), "the stored label, then the one sent")

	f, out, errOut := testFactory(t, readOnlyAccount(t))
	require.NoError(t, run(NewCmdBankAccount(f), "update", dryXID, "--label", "Shop", "--dry-run"))
	assert.Empty(t, out.String())
	assert.Equal(t, cmdutil.DryRunNotice+"\nNo field would change.\n! not checked: "+cmdutil.DryRunNotChecked+"\n", errOut.String())

	f, _, _ = testFactory(t, readOnlyAccount(t))
	err := run(NewCmdBankAccount(f), "update", "22222222-2222-4333-8444-555555555555", "--label", "x", "--dry-run")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
}

// After is the label as sent: what the server makes of it is not the preview's to guess.
func TestDryRunUpdate_LabelAsSent(t *testing.T) {
	for _, label := range []string{"  Shop #1 (main)  ", strings.Repeat("a", 101)} {
		f, out, _ := testFactory(t, readOnlyAccount(t))
		f.Global.Output = "json"
		require.NoError(t, run(NewCmdBankAccount(f), "update", dryXID, "--label", label, "--dry-run"))
		var got struct {
			After       map[string]any `json:"after"`
			NotChecked  []string       `json:"not_checked"`
			WouldRefuse []string       `json:"would_refuse"`
		}
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		assert.Equal(t, label, got.After["label"])
		assert.Equal(t, []string{cmdutil.DryRunNotChecked}, got.NotChecked)
		assert.Empty(t, got.WouldRefuse)
	}
}
