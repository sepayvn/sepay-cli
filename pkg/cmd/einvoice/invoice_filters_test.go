package einvoice

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const emptyInvoiceList = `{"status":"success","data":[],"meta":{"pagination":{"total":0,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`

// Every list filter reaches API v2 under the name the API reads, on both envs; an unset one is not sent.
func TestInvoiceList_SendsTheFilters(t *testing.T) {
	args := []string{
		"invoice", "list", "--source", "api", "--status", "issued", "--from", "2026-11-01", "--to", "2026-11-10",
		"--search", "  1C26TAA  ", "--type", "4", "--page", "2", "--per-page", "50",
	}
	want := url.Values{
		"source": {"api"}, "status": {"issued"}, "from_issued_date": {"2026-11-01"}, "to_issued_date": {"2026-11-10"},
		"q": {"1C26TAA"}, "document_type": {"4"}, "page": {"2"}, "per_page": {"50"},
	}
	for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
		t.Run(env, func(t *testing.T) {
			f, _, _, got := apiFactory(t, env, okJSON(emptyInvoiceList))
			require.NoError(t, run(f, args...))
			assert.Equal(t, []string{"/einvoice/invoices"}, got.paths)
			assert.Equal(t, want, got.query)

			f, _, _, got = apiFactory(t, env, okJSON(emptyInvoiceList))
			require.NoError(t, run(f, "invoice", "list", "--q", "DH1007"))
			assert.Equal(t, url.Values{"q": {"DH1007"}}, got.query, "--q is --search")

			for _, typed := range []string{"+4", "04"} {
				f, _, _, got = apiFactory(t, env, okJSON(emptyInvoiceList))
				require.NoError(t, run(f, "invoice", "list", "--type", typed))
				assert.Equal(t, url.Values{"document_type": {"4"}}, got.query, "--type sends the number it checked, not %q", typed)
			}

			f, _, _, got = apiFactory(t, env, okJSON(emptyInvoiceList))
			require.NoError(t, run(f, "invoice", "list", "--from", "2026-11-01"))
			assert.Equal(t, url.Values{"from_issued_date": {"2026-11-01"}}, got.query, "one end of the range alone")
		})
	}
}

// A filter the API would refuse, or that would not filter, is a usage error before any request.
func TestInvoiceList_RefusesBadFilters(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--status", "pending"}, "--status must be one of: draft, issued, failed, cancelled"},
		{[]string{"--status", "ISSUED"}, "--status must be one of: draft, issued, failed, cancelled"},
		{[]string{"--from", "2026-13-01"}, "--from must be a date (YYYY-MM-DD)"},
		{[]string{"--to", "2026-11-10 120000"}, "--to must be a date (YYYY-MM-DD)"},
		{[]string{"--from", "2026-11-10", "--to", "2026-11-01"}, "--from must not be after --to"},
		{[]string{"--search", "   "}, "--search must not be empty"},
		{[]string{"--search", ""}, "--search must not be empty"},
		{[]string{"--q", ""}, "--q must not be empty"},
		{[]string{"--type", "0"}, "--type must be a number from 1 to 10"},
		{[]string{"--type", "11"}, "--type must be a number from 1 to 10"},
		{[]string{"--type", "abc"}, "--type must be a number from 1 to 10"},
		{[]string{"--type", ""}, "--type must be a number from 1 to 10"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			f, _, _, got := prodFactory(t, okJSON(emptyInvoiceList))

			err := run(f, append([]string{"invoice", "list"}, tc.args...)...)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Equal(t, tc.want, err.Error())
			assert.Empty(t, got.paths, "no request may be made")
		})
	}
	for _, edge := range [][]string{{"--type", "1"}, {"--type", "10"}, {"--from", "2026-11-10", "--to", "2026-11-10"}} {
		f, _, _, _ := prodFactory(t, okJSON(emptyInvoiceList))
		require.NoError(t, run(f, append([]string{"invoice", "list"}, edge...)...), edge)
	}
}

// A range the API finds too long is its own call: the command exits 1 with the server's field message.
func TestInvoiceList_ServerRefusesTheRange(t *testing.T) {
	f, _, errOut, _ := prodFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"validation_error","message":"Validation failed","errors":{"from_issued_date":"The date range must not exceed 1830 days"}}`))
	})

	err := run(f, "invoice", "list", "--from", "2020-01-01", "--to", "2026-11-10")

	assert.Equal(t, cmdutil.ExitError, cmdutil.HandleError(f, err))
	assert.Contains(t, errOut.String(), "the request failed validation")
	assert.Contains(t, errOut.String(), "from_issued_date: The date range must not exceed 1830 days")
}

const invoiceWithOriginal = `{"reference_code":"DH1007-TT1","invoice_number":"330","invoice_series":"C26MMM","issued_date":"2026-11-11","status":"issued","document_type":4,"total_amount":1100000,` +
	`"original_invoice":{"reference_code":"DH1007","invoice_number":"120","invoice_series":"C26MMM","issued_date":"2026-11-02"}}`

// ORIGINAL is the number of the invoice a replacement or adjustment points to, written as NUMBER is.
func TestInvoiceList_OriginalColumn(t *testing.T) {
	body := `{"status":"success","data":[` + invoiceWithOriginal + `,` +
		`{"reference_code":"DH2-TT1","invoice_number":"331","invoice_series":"C26MMM","status":"issued","document_type":6,"original_invoice":{"reference_code":"DH2","invoice_number":"0","invoice_series":"C26MMM","issued_date":null}},` +
		`{"reference_code":"DH3-TT1","invoice_number":"332","invoice_series":"C26MMM","status":"issued","document_type":7,"original_invoice":null},` +
		`{"reference_code":"DH4","invoice_number":"333","invoice_series":"C26MMM","status":"issued","document_type":2}` +
		`]}`
	f, out, _, _ := prodFactory(t, okJSON(body))

	require.NoError(t, run(f, "invoice", "list"))

	s := out.String()
	assert.Regexp(t, `(?m)^NUMBER +ORIGINAL +REF +TYPE`, s)
	assert.Regexp(t, `(?m)^C26MMM330 +C26MMM120 +DH1007-TT1 +Replacement`, s)
	assert.Regexp(t, `(?m)^C26MMM331 +- +DH2-TT1 `, s, "an original with no number yet")
	assert.Regexp(t, `(?m)^C26MMM332 +- +DH3-TT1 `, s, "an original the server could not find")
	assert.Regexp(t, `(?m)^C26MMM333 +- +DH4 `, s, "an invoice that replaces nothing")
}

// get prints the original under Number, with its reference code when it has one.
func TestInvoiceGet_OriginalLine(t *testing.T) {
	for _, tc := range []struct {
		name, original, want string
	}{
		{"with a reference code", `{"reference_code":"DH1007","invoice_number":"120","invoice_series":"C26MMM","issued_date":"2026-11-02"}`, "Number:     C26MMM330\nOriginal:   C26MMM120 (DH1007)\nRef:"},
		{"without one", `{"reference_code":null,"invoice_number":"120","invoice_series":"C26MMM","issued_date":null}`, "Number:     C26MMM330\nOriginal:   C26MMM120\nRef:"},
		{"without a number", `{"reference_code":"DH1007","invoice_number":null,"invoice_series":null,"issued_date":null}`, "Number:     C26MMM330\nOriginal:   - (DH1007)\nRef:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"status":"success","data":{"reference_code":"DH1007-TT1","invoice_number":"330","invoice_series":"C26MMM","status":"issued","document_type":4,"original_invoice":` + tc.original + `}}`
			f, out, _, _ := prodFactory(t, okJSON(body))
			require.NoError(t, run(f, "invoice", "get", "DH1007-TT1"))
			assert.Contains(t, out.String(), tc.want)
		})
	}

	f, out, _, _ := prodFactory(t, okJSON(prodInvoiceDetail))
	require.NoError(t, run(f, "invoice", "get", "DH1007"))
	assert.NotContains(t, out.String(), "Original:", "an invoice that replaces nothing has no line")
}

// -o json carries original_invoice as the server sent it, nulls included, and leaves it out when absent.
func TestInvoice_JSONCarriesTheOriginal(t *testing.T) {
	const want = `"original_invoice": {
      "reference_code": null,
      "invoice_number": "120",
      "invoice_series": "C26MMM",
      "issued_date": null
    }`
	body := `{"status":"success","data":[` + strings.Replace(invoiceWithOriginal, `"reference_code":"DH1007","invoice_number":"120","invoice_series":"C26MMM","issued_date":"2026-11-02"`,
		`"reference_code":null,"invoice_number":"120","invoice_series":"C26MMM","issued_date":null`, 1) + `]}`
	for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
		f, out, _, _ := apiFactory(t, env, okJSON(body))
		f.Global.Output = "json"
		require.NoError(t, run(f, "invoice", "list"))
		assert.Contains(t, out.String(), want, env)
	}

	f, out, _, _ := prodFactory(t, okJSON(prodInvoiceList))
	f.Global.Output = "json"
	require.NoError(t, run(f, "invoice", "list"))
	assert.NotContains(t, out.String(), "original_invoice")
}

// The original's fields are server text: the table and the get line strip terminal controls from them.
func TestInvoice_OriginalIsSanitized(t *testing.T) {
	original := `"original_invoice":{"reference_code":` + jq(poison("DH1007")) + `,"invoice_number":"120","invoice_series":` + jq(poison("C26MMM")) + `,"issued_date":null}`
	for _, tc := range []struct {
		args []string
		body string
	}{
		{[]string{"invoice", "list"}, `{"status":"success","data":[{"reference_code":"DH1007-TT1","invoice_number":"330","status":"issued",` + original + `}]}`},
		{[]string{"invoice", "get", "DH1007-TT1"}, `{"status":"success","data":{"reference_code":"DH1007-TT1","invoice_number":"330","status":"issued",` + original + `}}`},
	} {
		f, out, _, _ := prodFactory(t, okJSON(tc.body))
		require.NoError(t, run(f, tc.args...))
		assertClean(t, strings.Join(tc.args, " "), out.String())
		assert.Contains(t, out.String(), "C26MMM", tc.args)
	}
}

// Both envs print the same table from the same API v2 response, for list and for get.
func TestInvoiceRead_BothEnvsReadTheSameShape(t *testing.T) {
	for _, tc := range []struct {
		args []string
		body string
	}{{[]string{"invoice", "list"}, prodInvoiceList}, {[]string{"invoice", "get", "DH1009"}, prodInvoiceFull}} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var outs []string
			for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
				f, out, _, _ := apiFactory(t, env, okJSON(tc.body))
				require.NoError(t, run(f, tc.args...))
				outs = append(outs, out.String())
			}
			assert.Equal(t, outs[0], outs[1])
			assert.NotEmpty(t, bytes.TrimSpace([]byte(outs[0])))
		})
	}
}
