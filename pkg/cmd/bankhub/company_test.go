package bankhub

import (
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

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
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
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
	require.NoError(t, run(f, "company", "edit", "co_1", "--status", "Suspended"))
	assert.Equal(t, "/v1/company/edit/co_1", path)
	assert.Contains(t, string(body), `"status":"Suspended"`)
	assert.NotContains(t, string(body), "full_name") // only changed flags sent
	assert.Contains(t, out.String(), "Company updated.")
	assert.NotContains(t, out.String(), "Đã cập nhật")
}

func TestCompanyEdit_NoFieldsIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{}`))
	f.Global.Yes = true
	err := run(f, "company", "edit", "co_1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
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
		{"edit bogus", []string{"company", "edit", "co_1", "--status", "BOGUS"}, "Pending, Active, Suspended"},
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
