package bankaccount

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const oneList = `{"status":"success","data":[{"id":"ba_1","account_holder_name":"CONG TY ABC","account_number":"0123456789","accumulated":15000000,"last_transaction":"2026-02-18 09:35:12","label":"TK chinh","account_type":"personal","active":1,"created_at":"2025-01-15 10:30:00","bank_short_name":"ACB","bank_full_name":"Ngan hang ACB","bank_bin":"970416","bank_code":"ACB"}],"meta":{"pagination":{"total":3,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`

const banksJSON = `{"status":"success","data":[{"code":"ACB","short_name":"ACB"},{"code":"BIDV","short_name":"BIDV"}]}`

func testFactory(t *testing.T, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	return bankListFactory(t, banksJSON, h)
}

// bankListFactory is testFactory serving banks as the supported bank list.
func bankListFactory(t *testing.T, banks string, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The bank list a --bank code is checked against.
		if r.Method == http.MethodGet && r.URL.Path == "/banks" {
			_, _ = w.Write([]byte(banks))
			return
		}
		h(w, r)
	}))
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

func TestList_RenamedFlagsSendTheSameParam(t *testing.T) {
	for _, tc := range []struct{ flag, old, param, value string }{
		{"--search", "--q", "q", "abc"},
		{"--bank", "--bank-short-name", "bank_short_name", "ACB"},
	} {
		for _, name := range []string{tc.flag, tc.old} {
			t.Run(name, func(t *testing.T) {
				var got url.Values
				f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
					got = r.URL.Query()
					_, _ = w.Write([]byte(oneList))
				})
				require.NoError(t, run(NewCmdBankAccount(f), "list", name, tc.value))
				assert.Equal(t, url.Values{tc.param: {tc.value}}, got)
			})
		}
		t.Run(tc.flag+" and "+tc.old, func(t *testing.T) {
			f, _, _ := testFactory(t, func(http.ResponseWriter, *http.Request) { t.Error("no request expected") })
			err := run(NewCmdBankAccount(f), "list", tc.flag, tc.value, tc.old, tc.value)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.ErrorContains(t, err, tc.flag+" and "+tc.old+" are the same flag; pass one")
		})
	}
}

func TestList_RefusesReversedRanges(t *testing.T) {
	for args, want := range map[string]string{
		"--accumulated-min 9 --accumulated-max 1":                             "--accumulated-min must not be greater than --accumulated-max",
		"--last-transaction-from 2026-11-06 --last-transaction-to 2026-11-05": "--last-transaction-from must not be after --last-transaction-to",
	} {
		f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) { t.Error("must not reach the server") })
		err := run(NewCmdBankAccount(f), append([]string{"list"}, strings.Fields(args)...)...)
		require.EqualError(t, err, want)
	}
}

func TestList_BareEndDateCoversTheWholeDay(t *testing.T) {
	for value, want := range map[string]string{
		"2026-10-20":          "2026-10-20 23:59:59",
		"2026-10-20 12:00:00": "2026-10-20 12:00:00",
	} {
		var got url.Values
		f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Query()
			_, _ = w.Write([]byte(oneList))
		})
		require.NoError(t, run(NewCmdBankAccount(f), "list", "--last-transaction-from", "2026-10-20", "--last-transaction-to", value))
		assert.Equal(t, "2026-10-20", got.Get("last_transaction_from"))
		assert.Equal(t, want, got.Get("last_transaction_to"))
	}
}

func TestCreate_ChecksTheBankCode(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("must not create with an unknown bank: %s %s", r.Method, r.URL.Path)
	})
	err := run(NewCmdBankAccount(f), "create", "--bank", "XYZ", "--account-type", "individual")
	require.EqualError(t, err, `--bank "XYZ" is not a supported bank code (have: ACB, BIDV)`)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))

	var body map[string]any
	f, _, _ = testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(oneAccount))
	})
	require.NoError(t, run(NewCmdBankAccount(f), "create", "--bank", "acb", "--account-type", "individual"))
	assert.Equal(t, "ACB", body["bank"], "a code in another case is sent as the server spells it")
}

func TestCreate_BankMatchesTheShortName(t *testing.T) {
	var body map[string]any
	f, _, _ := bankListFactory(t, `{"status":"success","data":[{"code":"ACBE2E","short_name":"ACB","brand_name":"ACB"}]}`, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(oneAccount))
	})
	require.NoError(t, run(NewCmdBankAccount(f), "create", "--bank", "ACB", "--account-type", "individual"))
	assert.Equal(t, "ACBE2E", body["bank"], "the short name resolves to the code the picker would send")
}

func TestGetAndList_ShowWhatTheAccountNotifies(t *testing.T) {
	const acct = `{"id":"ba_1","bank_short_name":"MSB","bank_full_name":"MSB","account_number":"222","account_holder_name":"A","label":"","account_type":"individual","active":1,"accumulated":0,"created_at":"x","connection_type":"api","official_va_supported":true,"va_only":true}`
	f, out, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bank-accounts" {
			_, _ = w.Write([]byte(`{"status":"success","data":[` + acct + `,` + strings.Replace(acct, `"connection_type":"api"`, `"connection_type":null`, 1) + `]}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":` + acct + `}`))
	})
	require.NoError(t, run(NewCmdBankAccount(f), "get", "ba_1"))
	assert.Regexp(t, `Connection\s+api`, out.String())
	assert.Regexp(t, `Notifies\s+virtual accounts only`, out.String())
	assert.Regexp(t, `Official VA\s+yes`, out.String())

	out.Reset()
	require.NoError(t, run(NewCmdBankAccount(f), "list"))
	assert.Contains(t, out.String(), "CONNECTION")
	assert.Regexp(t, `222\s+A\s+api`, out.String())

	out.Reset()
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdBankAccount(f), "get", "ba_1"))
	assert.Contains(t, out.String(), `"connection_type": "api"`)
	assert.Contains(t, out.String(), `"official_va_supported": true`)
	assert.NotContains(t, out.String(), "supports_va")
	assert.Contains(t, out.String(), `"va_only": true`)
}

func TestNotifies_ReadsTheServersRoutes(t *testing.T) {
	conn := func(s string) *string { return &s }
	cases := []struct {
		name string
		a    BankAccount
		want string
	}{
		{"va only", BankAccount{VAOnly: true, ConnectionType: conn("api")}, "virtual accounts only"},
		{"official VAs", BankAccount{OfficialVASupported: true, ConnectionType: conn("api")}, "account number and virtual accounts"},
		{"sms", BankAccount{ConnectionType: conn("sms")}, "account number and virtual accounts"},
		{"api+sms", BankAccount{ConnectionType: conn("api+sms")}, "account number and virtual accounts"},
		{"api without official VAs", BankAccount{ConnectionType: conn("api")}, "account number, and any virtual account it has"},
		{"not connected", BankAccount{}, "account number, and any virtual account it has"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, c.a.Notifies())
		})
	}
}
