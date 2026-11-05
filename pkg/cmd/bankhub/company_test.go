package bankhub

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const companyList = `{"data":[{"xid":"co_1","full_name":"ACME Co","status":"Active","active":1,"created_at":"2026-01-02 10:00:00","updated_at":"2026-01-02 10:00:00"}],"meta":{"total":1,"per_page":20,"current_page":1,"page_count":1,"has_more":false}}`

const companyDetail = `{"data":{"xid":"co_1","full_name":"ACME Co","status":"Active","configuration":{"payment_code":"on","payment_code_prefix":"DH"},"created_at":"2026-01-02 10:00:00","updated_at":"2026-01-02 10:00:00"}}`

func TestCompanyList_Table(t *testing.T) {
	var gotQuery string
	f, out, errOut := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(companyList))
	})
	require.NoError(t, run(f, "company", "list", "--status", "Active", "--per-page", "20"))
	s := out.String()
	assert.Contains(t, s, "co_1")
	assert.Contains(t, s, "ACME Co")
	assert.Contains(t, s, "Active")
	assert.Contains(t, errOut.String(), "1 of 1")
	assert.Contains(t, gotQuery, "status=Active")
	assert.Contains(t, gotQuery, "per_page=20")
}

func TestCompanyList_JSONKeepsInactiveActive(t *testing.T) {
	body := `{"data":[{"xid":"co_2","full_name":"Inactive Co","status":"Suspended","active":0,"created_at":"2026-01-02 10:00:00","updated_at":"2026-01-02 10:00:00"}],"meta":{"total":1}}`
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(body))
	f.Global.Output = "json"
	require.NoError(t, run(f, "company", "list"))
	assert.Contains(t, out.String(), `"active": 0`)
}

func TestCompanyList_JSONEmptyIsArray(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{"data":[],"meta":{"total":0}}`))
	f.Global.Output = "json"
	require.NoError(t, run(f, "company", "list"))
	assert.Contains(t, out.String(), "[]")
}

func TestCompanyGet_ShowsConfiguration(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(companyDetail))
	require.NoError(t, run(f, "company", "get", "co_1"))
	s := out.String()
	assert.Contains(t, s, "co_1")
	assert.Contains(t, s, "ACME Co")
	assert.Contains(t, s, "payment_code")
	assert.Contains(t, s, "DH")
}

func TestCompanyGet_JSONHasConfiguration(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(companyDetail))
	f.Global.Output = "json"
	require.NoError(t, run(f, "company", "get", "co_1"))
	assert.Contains(t, out.String(), `"configuration"`)
	assert.Contains(t, out.String(), `"payment_code": "on"`)
}

func TestCompanyCreate_PostsBodyAndShowsXID(t *testing.T) {
	var body []byte
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"code":201,"message":"ok","data":{"xid":"co_new","full_name":"New Co"}}`))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "company", "create", "--name", "New Co", "--status", "Active"))
	assert.Contains(t, string(body), `"full_name":"New Co"`)
	assert.Contains(t, string(body), `"status":"Active"`)
	assert.Contains(t, out.String(), "co_new")
}

func TestCompanyCreate_JSONCarriesOnlyWhatTheAPIReturned(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"code":201,"message":"ok","data":{"xid":"co_new","full_name":"New Co"}}`))
	})
	f.Global.Yes = true
	f.Global.Output = "json"
	require.NoError(t, run(f, "company", "create", "--name", "New Co"))
	assert.Contains(t, out.String(), `"xid"`)
	assert.NotContains(t, out.String(), `"status"`)
	assert.NotContains(t, out.String(), `"created_at"`)
	assert.NotContains(t, out.String(), `"updated_at"`)
}

func TestCompanyCreate_NoNameNonInteractiveIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{}`))
	f.Global.Yes = true
	err := run(f, "company", "create")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestCompanyEdit_PostsChangedFieldsOnly(t *testing.T) {
	var body []byte
	var path string
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"code":200,"message":"Đã cập nhật"}`))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "company", "update", "co_1", "--status", "Suspended"))
	assert.Equal(t, "/v1/company/edit/co_1", path)
	assert.Contains(t, string(body), `"status":"Suspended"`)
	assert.NotContains(t, string(body), "full_name") // only changed flags sent
	assert.Contains(t, out.String(), "Company updated.")
	assert.NotContains(t, out.String(), "Đã cập nhật")
}

func TestCompanyEdit_NoFieldsIsUsageError(t *testing.T) {
	for _, tty := range []bool{false, true} {
		f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{}`))
		f.Global.Yes = true
		if tty {
			f.IOStreams.SetStdinTTY(true)
			f.IOStreams.SetStdoutTTY(true)
			f.Global.NoInput = true
		}
		err := run(f, "company", "update", "co_1")
		require.Error(t, err)
		assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		assert.ErrorContains(t, err, "nothing to update: pass at least one of --name/--status/--payment-code/--transaction-amount", "tty=%v", tty)
	}
}

func TestCompanyGet_NotFoundMapsToError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":404,"message":"Công ty (tổ chức) không tồn tại hoặc không thuộc merchant này"}`))
	})
	err := run(f, "company", "get", "NOPE")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
}

func TestCompanyList_StatusValidation(t *testing.T) {
	cases := []struct {
		name  string
		value string
		query string
		ok    bool
	}{
		{"valid", "Suspended", "Suspended", true},
		{"wrong case", "active", "", false},
		{"bogus", "BOGUS", "", false},
		{"empty is unset", "", "", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var got url.Values
			f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query()
				_, _ = w.Write([]byte(`{"data":[],"meta":{"total":0}}`))
			})

			err := run(f, "company", "list", "--status", tt.value)

			if !tt.ok {
				require.Error(t, err)
				assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
				assert.Contains(t, err.Error(), "Pending, Active, Suspended, Terminated, Cancelled, Fraud")
				assert.Nil(t, got, "no request may be made")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.query, got.Get("status"))
		})
	}
}

func TestCompanyWrite_StatusValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"create bogus", []string{"company", "create", "--name", "X", "--status", "BOGUS"}, "Pending, Active"},
		{"create non-initial state", []string{"company", "create", "--name", "X", "--status", "Suspended"}, "Pending, Active"},
		{"update bogus", []string{"company", "update", "co_1", "--status", "BOGUS"}, "Pending, Active, Suspended"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not call the server on an invalid status")
			})
			f.Global.Yes = true

			err := run(f, tt.args...)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// --search replaced --q on the Bank Hub lists; both must send the same q param.
func TestLists_SearchAndQSendTheSameParam(t *testing.T) {
	for _, path := range [][]string{{"company", "list"}, {"bank-account", "list"}, {"transaction", "list"}} {
		for _, name := range []string{"--search", "--q"} {
			t.Run(strings.Join(path, " ")+" "+name, func(t *testing.T) {
				var got url.Values
				f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
					got = r.URL.Query()
					_, _ = w.Write([]byte(`{"data":[],"meta":{"total":0}}`))
				})
				require.NoError(t, run(f, append(path, name, "shop")...))
				assert.Equal(t, "shop", got.Get("q"))
			})
		}
		t.Run(strings.Join(path, " ")+" both", func(t *testing.T) {
			f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(http.ResponseWriter, *http.Request) { t.Error("no request expected") })
			err := run(f, append(path, "--search", "a", "--q", "a")...)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.ErrorContains(t, err, "--search and --q are the same flag; pass one")
		})
	}
}

// companyServer answers the company read and captures the edit.
func companyServer(t *testing.T, detail string) (*cmdutil.Factory, *[]byte, *string) {
	t.Helper()
	var body []byte
	var edited string
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(detail))
			return
		}
		edited = r.URL.Path
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"code":200,"message":"ok"}`))
	})
	return f, &body, &edited
}

const companyWithCap = `{"data":{"xid":"co_1","full_name":"ACME Co","status":"Active","configuration":{"payment_code":"on","transaction_amount":500000},"created_at":"2026-01-02 10:00:00","updated_at":"2026-01-02 10:00:00"}}`

func TestCompanyEdit_TerminalAsksWhatToEdit(t *testing.T) {
	f, body, edited := companyServer(t, companyWithCap)
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Name (ACME Co)", "Pay code matching (on)", "Transaction amount (500000)"}},
		prompttest.Step{Label: "Company full name", Answer: "ACME Group"},
		prompttest.Step{Label: "Pay code matching", Answer: "off"},
		prompttest.Step{Label: "Max transaction amount (whole VND or Unlimited)", Answer: "Unlimited"},
		prompttest.Step{Label: "update company co_1?", Answer: true},
	)

	require.NoError(t, run(f, "company", "update", "co_1"))

	assert.Equal(t, "ACME Co", prompts.Defaults["Company full name"])
	assert.Equal(t, "500000", prompts.Defaults["Max transaction amount (whole VND or Unlimited)"])
	assert.Equal(t, "/v1/company/edit/co_1", *edited)
	assert.JSONEq(t, `{"full_name":"ACME Group","payment_code":"off","transaction_amount":"Unlimited"}`, string(*body))
}

func TestCompanyEdit_TerminalDeclinedConfirmSendsNothing(t *testing.T) {
	f, _, edited := companyServer(t, companyDetail)
	prompttest.Use(t, f,
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Status (Active)"}},
		prompttest.Step{Label: "Status", Answer: "Suspended"},
		prompttest.Step{Label: "update company co_1?", Answer: false},
	)
	require.Error(t, run(f, "company", "update", "co_1"))
	assert.Empty(t, *edited, "the confirmation still guards the write")
}

func TestCompanyEdit_TerminalNothingPicked(t *testing.T) {
	f, _, edited := companyServer(t, companyDetail)
	prompttest.Use(t, f, prompttest.Step{Label: "What do you want to edit?", Answer: []string{}})
	err := run(f, "company", "update", "co_1")
	require.EqualError(t, err, "provide at least one field to update")
	assert.Empty(t, *edited)
}

func TestCompanyEdit_TerminalWithAFlagAsksNothing(t *testing.T) {
	f, body, _ := companyServer(t, companyDetail)
	prompttest.Use(t, f)
	f.Global.Yes = true
	require.NoError(t, run(f, "company", "update", "co_1", "--name", "X"))
	assert.JSONEq(t, `{"full_name":"X"}`, string(*body))
}

func TestCompanyEdit_ChecksPayCodeAndAmountLocally(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--payment-code", "yes"}, "--payment-code must be one of: on, off"},
		{[]string{"--transaction-amount", "1.5"}, `--transaction-amount must be a whole amount in VND or Unlimited, got "1.5"`},
		{[]string{"--transaction-amount", "-1"}, `--transaction-amount must be a whole amount in VND or Unlimited, got "-1"`},
		{[]string{"--transaction-amount", "unlimited"}, `--transaction-amount must be a whole amount in VND or Unlimited, got "unlimited"`},
	}
	for _, c := range cases {
		f, _, edited := companyServer(t, companyDetail)
		f.Global.Yes = true
		err := run(f, append([]string{"company", "update", "co_1"}, c.args...)...)
		require.EqualError(t, err, c.want)
		assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		assert.Empty(t, *edited)
	}

	f, body, _ := companyServer(t, companyDetail)
	f.Global.Yes = true
	require.NoError(t, run(f, "company", "update", "co_1", "--transaction-amount", "5000000", "--payment-code", "off"))
	assert.JSONEq(t, `{"transaction_amount":"5000000","payment_code":"off"}`, string(*body))
}

func TestCompanyEdit_TerminalChecksTheAmountAnswer(t *testing.T) {
	f, _, edited := companyServer(t, companyDetail)
	prompttest.Use(t, f,
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Transaction amount"}},
		prompttest.Step{Label: "Max transaction amount (whole VND or Unlimited)", Answer: "lots"},
	)
	err := run(f, "company", "update", "co_1")
	require.EqualError(t, err, `--transaction-amount must be a whole amount in VND or Unlimited, got "lots"`)
	assert.Empty(t, *edited)
}

const companyWithBigCap = `{"data":{"xid":"co_1","full_name":"ACME Co","status":"Suspended","configuration":{"payment_code":"off","transaction_amount":5000000},"created_at":"2026-01-02 10:00:00","updated_at":"2026-01-02 10:00:00"}}`

func TestCompanyEdit_ChoicesStartOnTheStoredValues(t *testing.T) {
	f, body, _ := companyServer(t, companyWithBigCap)
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Status (Suspended)", "Pay code matching (off)", "Transaction amount (5000000)"}},
		prompttest.Step{Label: "Status", Answer: "Suspended"},
		prompttest.Step{Label: "Pay code matching", Answer: "off"},
		prompttest.Step{Label: "Max transaction amount (whole VND or Unlimited)", Answer: ""},
		prompttest.Step{Label: "update company co_1?", Answer: true},
	)
	require.NoError(t, run(f, "company", "update", "co_1"))
	assert.Equal(t, "Suspended", prompts.Defaults["Status"])
	assert.Equal(t, "off", prompts.Defaults["Pay code matching"])
	assert.Equal(t, "5000000", prompts.Defaults["Max transaction amount (whole VND or Unlimited)"], "not 5e+06")
	assert.JSONEq(t, `{"status":"Suspended","payment_code":"off","transaction_amount":"5000000"}`, string(*body))
}

func TestCompanyGet_PrintsWholeAmounts(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(companyWithBigCap))
	require.NoError(t, run(f, "company", "get", "co_1"))
	assert.Contains(t, out.String(), "5000000")
	assert.NotContains(t, out.String(), "e+06")
}
