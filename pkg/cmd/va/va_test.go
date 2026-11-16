package va

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const oneList = `{"status":"success","data":[{"id":"va_1","va":"VA001","sub_holder_name":"NGUYEN A","label":"Shop 1","active":1,"official":1,"static":0,"bank_account_id":"ba_1","created_at":"2025-06-01 09:00:00","updated_at":"2026-03-12 17:30:00"}],"meta":{"pagination":{"total":1,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`

func testFactory(t *testing.T, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(srv.URL, "test")
		c.MaxRetries = 0
		return c, nil
	}
	return f, out, errOut
}

func run(cmd *cobra.Command, args ...string) error {
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func TestList_NestedPath(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	f, out, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		_, _ = w.Write([]byte(oneList))
	})
	require.NoError(t, run(NewCmdVA(f), "list", "--bank-account", "ba_1",
		"--official", "1", "--static", "0", "--q", "shop"))

	assert.Equal(t, "/bank-accounts/ba_1/va", gotPath)
	assert.Equal(t, "1", gotQuery.Get("official"))
	assert.Equal(t, "0", gotQuery.Get("static"))
	assert.Equal(t, "shop", gotQuery.Get("q"))

	s := out.String()
	assert.Contains(t, s, "va_1")
	assert.Contains(t, s, "VA001")
	assert.Contains(t, s, "yes") // official=1
	assert.Contains(t, s, "no")  // static=0
}

func TestList_RequiresBankAccount(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {})
	err := run(NewCmdVA(f), "list")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestGet_NestedPath(t *testing.T) {
	var gotPath string
	f, out, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"va_5","va":"VA005","sub_holder_name":"NGUYEN B","label":"Shop 5","active":1,"official":0,"static":1,"bank_account_id":"ba_9","created_at":"2025-06-01 09:00:00","updated_at":"2026-03-12 17:30:00"}}`))
	})
	require.NoError(t, run(NewCmdVA(f), "get", "va_5", "--bank-account", "ba_9"))

	assert.Equal(t, "/bank-accounts/ba_9/va/va_5", gotPath)
	assert.Contains(t, out.String(), "VA005")
}

func TestGet_RequiresBankAccount(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {})
	err := run(NewCmdVA(f), "get", "va_5")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestList_JSON(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(oneList))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdVA(f), "list", "--bank-account", "ba_1"))

	s := out.String()
	assert.Contains(t, s, `"official": 1`)
	assert.Contains(t, s, `"bank_account_id": "ba_1"`)
}

func TestIssue_PostsAccountVA(t *testing.T) {
	var method, path, key string
	var body map[string]any
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, key = r.Method, r.URL.Path, r.Header.Get("X-Idempotency-Key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"otp_required":false,"id":"va_9","va":"SBSEPAYXYZ","sub_holder_name":null,"label":"Shop","active":"1","official":1,"static":1,"bank_account_id":"ba_1","created_at":"2026-07-15 10:00:00","updated_at":null}}`))
	})
	require.NoError(t, run(NewCmdVA(f), "issue", "--bank-account", "ba_1", "--label", "Shop"))
	assert.Equal(t, http.MethodPost, method)
	assert.Equal(t, "/bank-accounts/ba_1/va", path)
	assert.NotEmpty(t, key)
	assert.Equal(t, "Shop", body["label"])
	assert.Contains(t, errOut.String(), "Issued virtual account SBSEPAYXYZ")
}

func TestIssue_JSONIsTheVA(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"otp_required":false,"id":"va_9","va":"SB1","label":"","active":"1","official":0,"static":1,"bank_account_id":"ba_1","created_at":"x","updated_at":null}}`))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdVA(f), "issue", "--bank-account", "ba_1"))
	s := out.String()
	assert.NotContains(t, s, "otp_required")
	assert.Contains(t, s, `"va": "SB1"`)
	assert.Contains(t, s, `"active": 1`) // FlexInt string -> int
}

func TestIssue_MissingBankAccountNonInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server without a bank account")
	})
	err := run(NewCmdVA(f), "issue")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestRevoke_ConfirmedWithYes(t *testing.T) {
	var method, path, key string
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, key = r.Method, r.URL.Path, r.Header.Get("X-Idempotency-Key")
		w.WriteHeader(http.StatusNoContent)
	})
	f.Global.Yes = true
	require.NoError(t, run(NewCmdVA(f), "revoke", "SBSEPAY1", "--bank-account", "ba_1"))
	assert.Equal(t, http.MethodDelete, method)
	assert.Equal(t, "/bank-accounts/ba_1/va/SBSEPAY1", path)
	assert.NotEmpty(t, key)
	assert.Contains(t, errOut.String(), "Revoked virtual account SBSEPAY1")
}

func TestRevoke_NeedsConfirmNonInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not revoke without confirmation")
	})
	err := run(NewCmdVA(f), "revoke", "SBSEPAY1", "--bank-account", "ba_1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestList_SearchAndQSendTheSameParam(t *testing.T) {
	for _, name := range []string{"--search", "--q"} {
		t.Run(name, func(t *testing.T) {
			var got url.Values
			f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query()
				_, _ = w.Write([]byte(oneList))
			})
			require.NoError(t, run(NewCmdVA(f), "list", "--bank-account", "ba_1", name, "shop"))
			assert.Equal(t, url.Values{"q": {"shop"}}, got)
		})
	}
	f, _, _ := testFactory(t, func(http.ResponseWriter, *http.Request) { t.Error("no request expected") })
	err := run(NewCmdVA(f), "list", "--bank-account", "ba_1", "--search", "a", "--q", "a")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.ErrorContains(t, err, "--search and --q are the same flag; pass one")
}
