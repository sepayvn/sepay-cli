package einvoice

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Partner-written invoice strings must not reach the terminal raw.
const escapePayload = "\x1b[2J\x1b[H\x1b]0;pwn\x07"

func assertClean(t *testing.T, where, got string) {
	t.Helper()
	for _, seq := range []string{"\x1b[2J", "\x1b[H", "\x1b]0;", "\x07"} {
		if strings.Contains(got, seq) {
			t.Errorf("%s leaked %q: %q", where, seq, got)
		}
	}
}

func poison(s string) string { return s + escapePayload }

// jq quotes s as a JSON string; %q would emit \x1b, which JSON rejects.
func jq(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestInvoiceGet_SanitizesServerStrings(t *testing.T) {
	body := fmt.Sprintf(`{"success":true,"data":{"reference_code":%s,"invoice_number":"589","invoice_series":%s,"status":%s,"issued_date":"2026-01-26","payment_method":"TM","buyer":{"name":%s,"tax_code":%s},"pdf_url":%s,"total_before_tax":200000,"tax_amount":20000,"total_amount":220000,"items":[{"line_number":1,"item_name":%s,"quantity":1,"unit_price":200000,"tax_rate":10,"total_amount":220000}]}}`,
		jq(poison("084e179d")), jq(poison("C26TSE")), jq(poison("issued")),
		jq(poison("Cong ty ABC")), jq(poison("0123456789")),
		jq(poison("https://x.test/a.pdf")), jq(poison("San pham 1")))
	f, out := testFactory(t, okJSON(brokerEInvoiceBearer), routes(map[string]string{"/v1/invoices/084e179d": body}))
	require.NoError(t, run(f, "invoice", "get", "084e179d"))
	assertClean(t, "invoice get", out.String())
	require.Contains(t, out.String(), "084e179d")
	require.Contains(t, out.String(), "San pham 1")
	require.Contains(t, out.String(), "220.000")
}

func TestAccountGet_SanitizesServerStrings(t *testing.T) {
	body := fmt.Sprintf(`{"success":true,"data":{"id":%s,"provider":%s,"active":1,"tax_authority_approved_date":%s,"templates":[{"template_code":"1","invoice_series":"C26TSE","invoice_label":%s}]}}`,
		jq(poison("pa_1")), jq(poison("matbao")), jq(poison("2026-01-01")), jq(poison("Mau 01")))
	f, out := testFactory(t, okJSON(brokerEInvoiceBearer), routes(map[string]string{"/v1/provider-accounts/pa_1": body}))
	require.NoError(t, run(f, "account", "get", "pa_1"))
	assertClean(t, "account get", out.String())
	require.Contains(t, out.String(), "matbao")
	require.Contains(t, out.String(), "Mau 01")
}

func TestInvoiceCreateStatus_SanitizesServerStrings(t *testing.T) {
	body := fmt.Sprintf(`{"success":true,"data":{"reference_code":%s,"status":"Failed","message":%s}}`,
		jq(poison("trk_1")), jq(poison("provider rejected")))
	f, out := testFactory(t, okJSON(brokerEInvoiceBearer), routes(map[string]string{"/v1/invoices/create/check/trk_1": body}))
	require.Error(t, run(f, "invoice", "create-status", "trk_1")) // Failed exits non-zero
	assertClean(t, "invoice create-status", out.String())
	require.Contains(t, out.String(), "provider rejected")
}

func TestInvoiceList_SanitizesServerStrings(t *testing.T) {
	body := fmt.Sprintf(`{"success":true,"data":{"items":[{"reference_code":%s,"invoice_number":"589","status":%s,"issued_date":"2026-01-26","total_amount":220000}],"paging":{"current_page":1,"page_count":1,"total":1}}}`,
		jq(poison("084e179d")), jq(poison("issued")))
	f, out := testFactory(t, okJSON(brokerEInvoiceBearer), routes(map[string]string{"/v1/invoices": body}))
	require.NoError(t, run(f, "invoice", "list"))
	assertClean(t, "invoice list", out.String())
}

func TestQuota_SanitizesServerStrings(t *testing.T) {
	body := fmt.Sprintf(`{"success":true,"data":{"quota_remaining":5,"tax_authority_approved_date":%s}}`, jq(poison("2026-01-01")))
	f, out := testFactory(t, okJSON(brokerEInvoiceBearer), routes(map[string]string{"/v1/usage": body}))
	require.NoError(t, run(f, "quota"))
	assertClean(t, "quota", out.String())
}

// JSON escapes control bytes itself, so machine output stays raw.
func TestInvoiceGet_JSONKeepsRawValue(t *testing.T) {
	body := fmt.Sprintf(`{"success":true,"data":{"reference_code":%s,"invoice_number":"589","status":"issued","issued_date":"2026-01-26","total_before_tax":1,"tax_amount":0,"total_amount":2}}`,
		jq(poison("084e179d")))
	f, out := testFactory(t, okJSON(brokerEInvoiceBearer), routes(map[string]string{"/v1/invoices/084e179d": body}))
	f.Global.Output = "json"
	require.NoError(t, run(f, "invoice", "get", "084e179d"))

	var got struct {
		ReferenceCode string `json:"reference_code"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, poison("084e179d"), got.ReferenceCode)
	require.NotContains(t, out.String(), "\x1b", "the JSON text itself must carry no raw ESC byte")
}
