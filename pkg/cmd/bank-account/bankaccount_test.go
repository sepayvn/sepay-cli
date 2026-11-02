package bankaccount

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

const oneList = `{"status":"success","data":[{"id":"ba_1","account_holder_name":"CONG TY ABC","account_number":"0123456789","accumulated":15000000,"last_transaction":"2026-02-18 09:35:12","label":"TK chinh","account_type":"personal","active":1,"created_at":"2025-01-15 10:30:00","bank_short_name":"ACB","bank_full_name":"Ngan hang ACB","bank_bin":"970416","bank_code":"ACB"}],"meta":{"pagination":{"total":3,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`

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

func TestList_Table(t *testing.T) {
	f, out, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(oneList))
	})
	require.NoError(t, run(NewCmdBankAccount(f), "list"))

	s := out.String()
	assert.Contains(t, s, "ba_1")
	assert.Contains(t, s, "ACB")
	assert.Contains(t, s, "15.000.000") // grouped VND
	assert.Contains(t, s, "yes")        // active
	assert.Contains(t, errOut.String(), "page 1/1 · 1 of 3")
}

// The v2 backend sends active as the string "1".
func TestList_ActiveAsString(t *testing.T) {
	body := `{"status":"success","data":[{"id":"ba_1","account_holder_name":"X","account_number":"1","accumulated":0,"last_transaction":null,"label":null,"account_type":"individual","active":"1","created_at":"2026-07-14 17:04:48","bank_short_name":"OCB","bank_full_name":"OCB","bank_bin":"970448","bank_code":"OCB"}],"meta":{"pagination":{"total":1,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	require.NoError(t, run(NewCmdBankAccount(f), "list"))
	assert.Contains(t, out.String(), "yes")

	f2, out2, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	f2.Global.Output = "json"
	require.NoError(t, run(NewCmdBankAccount(f2), "list"))
	assert.Contains(t, out2.String(), `"active": 1`)
}

func TestList_FlagToQueryMapping(t *testing.T) {
	var got url.Values
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(oneList))
	})
	require.NoError(t, run(NewCmdBankAccount(f), "list",
		"--bank-short-name", "ACB", "--q", "abc", "--active", "1",
		"--accumulated-min", "1000", "--per-page", "50"))

	assert.Equal(t, "ACB", got.Get("bank_short_name"))
	assert.Equal(t, "abc", got.Get("q"))
	assert.Equal(t, "1", got.Get("active"))
	assert.Equal(t, "1000", got.Get("accumulated_min"))
	assert.Equal(t, "50", got.Get("per_page"))
}

func TestList_Empty(t *testing.T) {
	f, out, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":[],"meta":{"pagination":{"total":0,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`))
	})
	require.NoError(t, run(NewCmdBankAccount(f), "list"))
	assert.Contains(t, errOut.String(), "No bank accounts found.")
	assert.Empty(t, out.String())
}

func TestGet(t *testing.T) {
	var gotPath string
	f, out, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"ba_1","account_holder_name":"CONG TY ABC","account_number":"0123456789","accumulated":15000000,"last_transaction":null,"label":"TK chinh","account_type":"personal","active":0,"created_at":"2025-01-15 10:30:00","bank_short_name":"ACB","bank_full_name":"Ngan hang ACB","bank_bin":"970416","bank_code":"ACB"}}`))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdBankAccount(f), "get", "ba_1"))

	assert.Equal(t, "/bank-accounts/ba_1", gotPath)
	assert.Contains(t, out.String(), `"last_transaction": null`)
	assert.Contains(t, out.String(), `"active": 0`)
}

const oneAccount = `{"status":"success","data":{"id":"ba_new","account_holder_name":"NGUYEN VAN A","account_number":"0000000009","accumulated":0,"last_transaction":null,"label":"Shop","account_type":"individual","active":"1","created_at":"2026-07-15 10:00:00","bank_short_name":"ACB","bank_full_name":"ACB","bank_bin":"970416","bank_code":"ACB"}}`

func TestCreate_PostsBodyWithIdempotencyKey(t *testing.T) {
	var method, path, key string
	var body map[string]any
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, key = r.Method, r.URL.Path, r.Header.Get("X-Idempotency-Key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(oneAccount))
	})
	require.NoError(t, run(NewCmdBankAccount(f), "create",
		"--bank", "ACB", "--account-type", "individual", "--connection", "sms", "--label", "Shop"))

	assert.Equal(t, http.MethodPost, method)
	assert.Equal(t, "/bank-accounts", path)
	assert.NotEmpty(t, key, "create must send an idempotency key")
	assert.Equal(t, "ACB", body["bank"])
	assert.Equal(t, "individual", body["account_type"])
	assert.Equal(t, "sms", body["connection_type"])
	assert.Contains(t, errOut.String(), "Created bank account ba_new")
}

func TestCreate_MissingRequiredNonInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server without required fields")
	})
	err := run(NewCmdBankAccount(f), "create", "--bank", "ACB") // missing account-type
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestUpdate_SendsOnlyChangedFields(t *testing.T) {
	var method string
	var body map[string]any
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"ba_1"}}`))
	})
	require.NoError(t, run(NewCmdBankAccount(f), "update", "ba_1", "--label", "New label"))

	assert.Equal(t, http.MethodPut, method)
	assert.Equal(t, "New label", body["label"])
	_, hasHolder := body["account_holder_name"]
	assert.False(t, hasHolder, "unchanged fields must not be sent")
	assert.Contains(t, errOut.String(), "Updated bank account ba_1")
}

func TestUpdate_NoFieldsIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server with no fields to update")
	})
	err := run(NewCmdBankAccount(f), "update", "ba_1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestDelete_ConfirmedWithYes(t *testing.T) {
	var method, path string
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	f.Global.Yes = true
	require.NoError(t, run(NewCmdBankAccount(f), "delete", "ba_1"))

	assert.Equal(t, http.MethodDelete, method)
	assert.Equal(t, "/bank-accounts/ba_1", path)
	assert.Contains(t, errOut.String(), "Deleted bank account ba_1")
}

func TestDelete_NeedsConfirmNonInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not delete without confirmation")
	})
	err := run(NewCmdBankAccount(f), "delete", "ba_1") // no --yes, non-TTY
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestCreate_EnumValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"bogus account type", []string{"--account-type", "personal", "--connection", "api"}, "individual, business_household, enterprise"},
		{"wrong-case account type", []string{"--account-type", "Individual", "--connection", "api"}, "individual"},
		{"bogus connection", []string{"--account-type", "individual", "--connection", "http"}, "sms, api"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not call the server on an invalid enum")
			})

			err := run(NewCmdBankAccount(f), append([]string{"create", "--bank", "ACB"}, tt.args...)...)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestCreate_ConnectionIsLowercased(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(oneAccount))
	})
	require.NoError(t, run(NewCmdBankAccount(f), "create",
		"--bank", "ACB", "--account-type", "individual", "--connection", "SMS"))
	assert.Equal(t, "sms", body["connection_type"])
}

func TestCreate_EmptyEnumIsUnset(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server without required fields")
	})
	err := run(NewCmdBankAccount(f), "create", "--bank", "ACB", "--account-type", "", "--connection", "")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "--account-type is required")
}

func TestCreate_ConnectionDefaultsToAPI(t *testing.T) {
	for _, args := range [][]string{nil, {"--connection", ""}} {
		var body map[string]any
		f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(oneAccount))
		})
		require.NoError(t, run(NewCmdBankAccount(f), append([]string{"create", "--bank", "ACB", "--account-type", "individual"}, args...)...))
		assert.Equal(t, "api", body["connection_type"])
	}
}
