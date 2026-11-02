package einvoice

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	iei "github.com/sepayvn/sepay-cli/internal/einvoice"
	"github.com/sepayvn/sepay-cli/internal/product"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

const brokerEInvoiceBearer = `{"status":"success","data":{"auth_type":"bearer","access_token":"eitok_SECRET","token_type":"Bearer","expires_at":"2999-01-01T00:00:00Z"}}`

// testFactory wires a fake Factory to a broker server and an E-Invoice host server.
func testFactory(t *testing.T, brokerH, eiH http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	keyring.MockInit()
	brokerSrv := httptest.NewServer(brokerH)
	t.Cleanup(brokerSrv.Close)
	eiSrv := httptest.NewServer(eiH)
	t.Cleanup(eiSrv.Close)

	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table", Env: "sandbox"}}

	mgr := product.NewManager(product.NewBroker(api.NewClient(brokerSrv.URL, "tok")), product.NewStore(t.TempDir()), "default", "sandbox")
	f.ProductManager = func() (*product.Manager, error) { return mgr, nil }
	f.NewEInvoiceClient = func() (*iei.Client, error) {
		c := iei.NewClient(eiSrv.URL, mgr)
		c.MaxRetries = 0
		return c, nil
	}
	return f, out, errOut
}

func run(f *cmdutil.Factory, args ...string) error {
	cmd := NewCmdEInvoice(f)
	cmd.SetArgs(args)
	cmd.SetOut(f.IOStreams.Out)
	cmd.SetErr(f.IOStreams.ErrOut)
	return cmd.ExecuteContext(context.Background())
}

func okJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func routes(m map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := m[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"error":{"code":"EINVOICE_DOCUMENT_NOT_FOUND","message":"not found"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestAuth_Table(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerEInvoiceBearer), okJSON(`{}`))
	require.NoError(t, run(f, "auth"))
	s := out.String()
	assert.Contains(t, s, "einvoice")
	assert.Contains(t, s, "sandbox")
	assert.Contains(t, s, "bearer")
	assert.NotContains(t, s, "eitok_SECRET", "token must never print")
}

func TestAuth_JSONHasNoSecret(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerEInvoiceBearer), okJSON(`{}`))
	f.Global.Output = "json"
	require.NoError(t, run(f, "auth"))
	s := out.String()
	assert.Contains(t, s, `"auth_type": "bearer"`)
	assert.NotContains(t, s, "eitok_SECRET")
	assert.NotContains(t, s, "access_token")
}

func TestAuth_ProductionRejected(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerEInvoiceBearer), okJSON(`{}`))
	f.Global.Env = "production"
	err := run(f, "auth")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestQuota_ReadsCorrectedKey(t *testing.T) {
	ei := routes(map[string]string{
		"/v1/usage": `{"data":{"quota_remaining":999,"tax_authority_approved_date":null}}`,
	})
	f, out, _ := testFactory(t, okJSON(brokerEInvoiceBearer), ei)
	require.NoError(t, run(f, "quota"))
	assert.Contains(t, out.String(), "999")
}

func TestQuota_JSONFieldSpelling(t *testing.T) {
	ei := routes(map[string]string{
		"/v1/usage": `{"data":{"quota_remaining":999,"tax_authority_approved_date":null}}`,
	})
	f, out, _ := testFactory(t, okJSON(brokerEInvoiceBearer), ei)
	f.Global.Output = "json"
	require.NoError(t, run(f, "quota"))
	assert.Contains(t, out.String(), `"quota_remaining": 999`)
}

func TestAccountList_Table(t *testing.T) {
	ei := routes(map[string]string{
		"/v1/provider-accounts": `{"data":{"paging":{"per_page":20,"total":1,"has_more":false,"current_page":1,"page_count":1},"items":[{"id":"0aea3134","provider":"matbao","active":true}]}}`,
	})
	f, out, _ := testFactory(t, okJSON(brokerEInvoiceBearer), ei)
	require.NoError(t, run(f, "account", "list"))
	s := out.String()
	assert.Contains(t, s, "0aea3134")
	assert.Contains(t, s, "matbao")
}

func TestAccountGet_ShowsTemplates(t *testing.T) {
	ei := routes(map[string]string{
		"/v1/provider-accounts/0aea3134": `{"success":true,"data":{"id":"0aea3134","provider":"matbao","active":true,"tax_authority_approved_date":"2026-04-20","templates":[{"template_code":"1","invoice_series":"C26TSE","invoice_label":"1 - C26TSE - HĐ GTGT"}]}}`,
	})
	f, out, _ := testFactory(t, okJSON(brokerEInvoiceBearer), ei)
	require.NoError(t, run(f, "account", "get", "0aea3134"))
	s := out.String()
	assert.Contains(t, s, "0aea3134")
	assert.Contains(t, s, "C26TSE")
}

func TestInvoiceList_Table(t *testing.T) {
	ei := routes(map[string]string{
		"/v1/invoices": `{"data":{"paging":{"per_page":20,"total":1,"has_more":false,"current_page":1,"page_count":1},"items":[{"reference_code":"084e179d","invoice_number":"589","status":"issued","issued_date":"2026-01-26","total_amount":220000}]}}`,
	})
	f, out, _ := testFactory(t, okJSON(brokerEInvoiceBearer), ei)
	require.NoError(t, run(f, "invoice", "list"))
	s := out.String()
	assert.Contains(t, s, "084e179d")
	assert.Contains(t, s, "589")
	assert.Contains(t, s, "220.000")
}

func TestInvoiceGet_ShowsItems(t *testing.T) {
	ei := routes(map[string]string{
		"/v1/invoices/084e179d": `{"success":true,"data":{"reference_code":"084e179d","invoice_number":"589","invoice_series":"C26TSE","status":"issued","issued_date":"2026-01-26","payment_method":"TM","buyer":{"name":"Cong ty ABC","tax_code":"0123456789"},"total_before_tax":200000,"tax_amount":20000,"total_amount":220000,"items":[{"line_number":1,"line_type":1,"item_code":"SP001","item_name":"San pham 1","unit":"cai","quantity":1,"unit_price":200000,"total_amount":220000,"tax_rate":10,"tax_amount":20000}]}}`,
	})
	f, out, _ := testFactory(t, okJSON(brokerEInvoiceBearer), ei)
	require.NoError(t, run(f, "invoice", "get", "084e179d"))
	s := out.String()
	assert.Contains(t, s, "084e179d")
	assert.Contains(t, s, "San pham 1")
	assert.Contains(t, s, "220.000")
}

func TestInvoiceGet_NotFoundMapsToError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerEInvoiceBearer), routes(map[string]string{}))
	err := run(f, "invoice", "get", "NOPE")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
}

func TestInvoiceCreate_PostsPayloadAndPrintsTracking(t *testing.T) {
	var body []byte
	ei := func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"success":true,"data":{"tracking_code":"trk_1","tracking_url":"https://x/y","message":"đã tạo"}}`))
	}
	f, out, _ := testFactory(t, okJSON(brokerEInvoiceBearer), ei)
	require.NoError(t, run(f, "invoice", "create", `{"template_code":"1","is_draft":true,"currency":"VND","ref_num":123456789012345678}`))
	assert.Contains(t, string(body), `"is_draft":true`)
	// A >2^53 id must survive (payload sent verbatim).
	assert.Contains(t, string(body), `123456789012345678`)
	assert.Contains(t, out.String(), "trk_1")
	assert.NotContains(t, out.String(), "đã tạo")
}

func TestInvoiceCreateStatus_UsesGET(t *testing.T) {
	var method string
	ei := func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_, _ = w.Write([]byte(`{"success":true,"data":{"reference_code":"trk_1","status":"Pending"}}`))
	}
	f, _, _ := testFactory(t, okJSON(brokerEInvoiceBearer), ei)
	require.NoError(t, run(f, "invoice", "create-status", "trk_1"))
	assert.Equal(t, http.MethodGet, method, "check endpoints must use GET (controller enforces it)")
}

func TestInvoiceCreate_MissingIsDraftIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerEInvoiceBearer), okJSON(`{}`))
	err := run(f, "invoice", "create", `{"template_code":"1"}`)
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestInvoiceCreate_IsDraftWrongTypeIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerEInvoiceBearer), okJSON(`{}`))
	err := run(f, "invoice", "create", `{"is_draft":"yes"}`)
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestInvoiceCreate_BadJSONIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerEInvoiceBearer), okJSON(`{}`))
	err := run(f, "invoice", "create", `not json`)
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestInvoiceCreateStatus_ShowsStatus(t *testing.T) {
	ei := routes(map[string]string{
		"/v1/invoices/create/check/trk_1": `{"success":true,"data":{"reference_code":"trk_1","status":"Success","message":"Xuất hóa đơn điện tử thành công","invoice":{"reference_code":"trk_1","invoice_number":"589","total_amount":220000}}}`,
	})
	f, out, _ := testFactory(t, okJSON(brokerEInvoiceBearer), ei)
	require.NoError(t, run(f, "invoice", "create-status", "trk_1"))
	s := out.String()
	assert.Contains(t, s, "Success")
	assert.Contains(t, s, "589")
	assert.NotContains(t, s, "thành công")
}

func TestInvoiceCreateStatus_FailedExitsNonZero(t *testing.T) {
	ei := routes(map[string]string{
		"/v1/invoices/create/check/trk_1": `{"success":true,"data":{"reference_code":"trk_1","status":"Failed","message":"provider rejected"}}`,
	})
	f, out, _ := testFactory(t, okJSON(brokerEInvoiceBearer), ei)
	err := run(f, "invoice", "create-status", "trk_1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
	assert.Contains(t, out.String(), "Failed")
	assert.Contains(t, out.String(), "provider rejected")
}

func TestInvoiceIssue_PostsRefAndPrintsTracking(t *testing.T) {
	var body []byte
	ei := func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"success":true,"data":{"tracking_code":"trk_2","message":"đã phát hành"}}`))
	}
	f, out, _ := testFactory(t, okJSON(brokerEInvoiceBearer), ei)
	f.Global.Yes = true // issuing to the tax authority confirms, in sandbox too
	require.NoError(t, run(f, "invoice", "issue", "ref_9"))
	assert.Contains(t, string(body), `"reference_code":"ref_9"`)
	assert.Contains(t, out.String(), "trk_2")
	assert.NotContains(t, out.String(), "đã phát hành")
}

func TestInvoiceIssue_ConfirmsButCreateDoesNot(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerEInvoiceBearer), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"success":true,"data":{"tracking_code":"trk_3"}}`))
	})
	err := run(f, "invoice", "issue", "ref_9")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires confirmation")

	f2, _, _ := testFactory(t, okJSON(brokerEInvoiceBearer), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"success":true,"data":{"tracking_code":"trk_4"}}`))
	})
	require.NoError(t, run(f2, "invoice", "create", `{"template_code":"1","is_draft":true,"currency":"VND"}`))
}

func TestInvoiceList_SourceValidation(t *testing.T) {
	cases := []struct {
		name  string
		value string
		query string // "" = the request must carry no source at all
		ok    bool
	}{
		{"valid", "manual", "manual", true},
		{"wrong case", "MANUAL", "", false},
		{"bogus", "ZZZ", "", false},
		{"empty is unset", "", "", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var got url.Values
			f, _, _ := testFactory(t, okJSON(brokerEInvoiceBearer), func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query()
				_, _ = w.Write([]byte(`{"data":{"paging":{"total":0},"items":[]}}`))
			})

			err := run(f, "invoice", "list", "--source", tt.value)

			if !tt.ok {
				require.Error(t, err)
				assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
				assert.Contains(t, err.Error(), "manual, api")
				assert.Nil(t, got, "no request may be made")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.query, got.Get("source"))
		})
	}
}

func TestInvoiceGet_NoArgNonInteractiveIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerEInvoiceBearer), func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server without a reference code")
	})
	err := run(f, "invoice", "get")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}
