package einvoice

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/config"
	iei "github.com/sepayvn/sepay-cli/internal/einvoice"
	"github.com/sepayvn/sepay-cli/internal/golden"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const prodInvoiceFields = `"reference_code":"DH1007","invoice_number":"0000123","invoice_series":"1C26TSP","issued_date":"2026-11-10","status":"issued","document_type":2,"provider_status":null,"tax_authority_code":null,"payment_method":"TM/CK","source":"api","pdf_url":"https://files.example/DH1007.pdf","xml_url":null,` +
	`"buyer":{"type":"company","name":"Công ty A","legal_name":"Công ty TNHH A","tax_code":"0101234567","address":"1 Đường A","email":"a@example.com","phone":"0900000000"},` +
	`"total_before_tax":100000,"tax_amount":10000,"total_amount":110000,"currency":"VND","notes":null,"related_reference_code":null`

const prodInvoiceList = `{"status":"success","data":[{` + prodInvoiceFields + `},` +
	`{"reference_code":"DH1006","invoice_number":null,"invoice_series":null,"issued_date":null,"status":"draft","document_type":1,"provider_status":null,"tax_authority_code":null,"payment_method":null,"source":"manual","pdf_url":null,"xml_url":null,"buyer":{"name":"Khách lẻ"},"total_before_tax":null,"tax_amount":null,"total_amount":null,"currency":"VND","notes":null}` +
	`],"meta":{"pagination":{"total":2,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`

const prodInvoiceDetail = `{"status":"success","data":{` + prodInvoiceFields + `,"items":[` +
	`{"line_number":1,"line_type":1,"item_code":"SP01","item_name":"Sản phẩm A","unit":"cái","quantity":1,"unit_price":100000,"total_amount":100000,"tax_rate":10,"tax_amount":10000}]}}`

// invoiceScopeMessage is the invoice reads' exit-5 text, pinned word for word.
const invoiceScopeMessage = "this command needs the read and einvoice:read scopes, granted at sepay login to users with the permission «Hóa đơn điện tử (eInvoice) › Xem danh sách hóa đơn điện tử»; ask an admin of your company if you lack it"

// apiRequests records what the API v2 host received.
type apiRequests struct {
	paths []string
	query url.Values
}

// prodFactory points the API v2 client at h and makes the e-invoice client fail the test if used.
func prodFactory(t *testing.T, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer, *apiRequests) {
	t.Helper()
	return apiFactory(t, config.EnvProduction, h)
}

// sandboxAPIFactory is prodFactory on sandbox, where invoice list and get read API v2 too.
func sandboxAPIFactory(t *testing.T, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer, *apiRequests) {
	t.Helper()
	return apiFactory(t, config.EnvSandbox, h)
}

func apiFactory(t *testing.T, env string, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer, *apiRequests) {
	t.Helper()
	f, out := testFactory(t, okJSON(brokerEInvoiceBearer), func(http.ResponseWriter, *http.Request) {
		t.Error("the sandbox e-invoice API must not be called")
	})
	f.Global.Env = env
	got := &apiRequests{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.paths = append(got.paths, r.URL.EscapedPath())
		got.query = r.URL.Query()
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(srv.URL, "tok")
		c.MaxRetries = 0
		return c, nil
	}
	return f, out, f.IOStreams.ErrOut.(*bytes.Buffer), got
}

func apiError(status int, code, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"` + code + `","message":"` + message + `"}`))
	}
}

func TestInvoiceList_ProductionReadsAPIv2(t *testing.T) {
	f, out, errOut, got := prodFactory(t, okJSON(prodInvoiceList))

	require.NoError(t, run(f, "invoice", "list", "--source", "api", "--page", "1", "--per-page", "20"))

	assert.Equal(t, []string{"/einvoice/invoices"}, got.paths)
	assert.Equal(t, url.Values{"source": {"api"}, "page": {"1"}, "per_page": {"20"}}, got.query)
	s := out.String()
	assert.Contains(t, s, "DH1007")
	assert.Contains(t, s, "0000123")
	assert.Contains(t, s, "110.000")
	assert.Contains(t, s, "DH1006")
	assert.Contains(t, errOut.String(), "page 1/1 · 2 of 2")
}

// An unset flag sends no parameter: API v2 refuses an empty source and a page or per_page of 0.
func TestInvoiceList_ProductionSendsNoUnsetFilter(t *testing.T) {
	f, _, _, got := prodFactory(t, okJSON(prodInvoiceList))

	require.NoError(t, run(f, "invoice", "list"))

	assert.Empty(t, got.query)
}

func TestInvoiceList_ProductionFooterUsesLastPage(t *testing.T) {
	body := strings.Replace(prodInvoiceList, `"pagination":{"total":2,"per_page":20,"current_page":1,"last_page":1,"has_more":false}`,
		`"pagination":{"total":45,"per_page":20,"current_page":2,"last_page":3,"has_more":true}`, 1)
	f, _, errOut, _ := prodFactory(t, okJSON(body))

	require.NoError(t, run(f, "invoice", "list", "--page", "2"))

	assert.Contains(t, errOut.String(), "page 2/3 · 2 of 45")
}

// A page past the last one comes back empty: the table says so and JSON is [].
func TestInvoiceList_ProductionPastTheLastPageIsEmpty(t *testing.T) {
	body := `{"status":"success","data":[],"meta":{"pagination":{"total":2,"per_page":20,"current_page":5,"last_page":1,"has_more":false}}}`
	f, out, errOut, _ := prodFactory(t, okJSON(body))
	require.NoError(t, run(f, "invoice", "list", "--page", "5"))
	assert.Contains(t, errOut.String(), "No invoices found.")
	assert.Empty(t, out.String())

	f, out, _, _ = prodFactory(t, okJSON(body))
	f.Global.Output = "json"
	require.NoError(t, run(f, "invoice", "list", "--page", "5"))
	assert.Equal(t, "[]", strings.TrimSpace(out.String()))
}

func TestInvoiceGet_ProductionReadsAPIv2(t *testing.T) {
	f, out, _, got := prodFactory(t, okJSON(prodInvoiceDetail))

	require.NoError(t, run(f, "invoice", "get", "DH1007"))

	assert.Equal(t, []string{"/einvoice/invoices/DH1007"}, got.paths)
	assert.Contains(t, out.String(), "Sản phẩm A (SP01)")
}

// Every field the server sends has a line; every kind of line item has its own rendering.
const prodInvoiceFull = `{"status":"success","data":{"reference_code":"DH1009","invoice_number":"0000125","invoice_series":"1C26TSP","issued_date":"2026-11-10","status":"issued","document_type":4,` +
	`"provider_status":"4","tax_authority_code":"M2-26-ABCDE-0000125","payment_method":"TM/CK","source":"api","pdf_url":"https://files.example/DH1009.pdf","xml_url":"https://files.example/DH1009.xml",` +
	`"buyer":{"type":"company","name":"Công ty A","legal_name":"Công ty TNHH A","tax_code":"0101234567","address":"12 Lê Lợi, Quận 1","email":"ketoan@congtya.vn","phone":"0900000000"},` +
	`"total_before_tax":290000,"tax_amount":29000,"total_amount":319000,"currency":"VND","notes":"Giao trong 3 ngày","items":[` +
	`{"line_number":1,"line_type":1,"item_code":"SP01","item_name":"Áo thun cotton","unit":"cái","quantity":2,"unit_price":100000,"total_amount":200000,"tax_rate":10,"tax_amount":20000},` +
	`{"line_number":2,"line_type":2,"item_code":"KM01","item_name":"Móc khóa quà tặng","unit":"cái","quantity":1,"unit_price":0,"total_amount":0,"tax_rate":10,"tax_amount":0},` +
	`{"line_number":3,"line_type":3,"item_name":"Chiết khấu thương mại","quantity":0,"unit_price":0,"total_amount":-10000,"tax_rate":10,"tax_amount":-1000},` +
	`{"line_number":4,"line_type":1,"item_name":"Phí giao hàng","unit":"lần","quantity":1,"unit_price":100000,"total_amount":100000,"tax_rate":-2,"tax_amount":0},` +
	`{"line_number":5,"line_type":1,"item_name":"Gói tư vấn","quantity":"1.5","unit_price":"20000.00","total_amount":"30000.00","tax_rate":"-1","tax_amount":0},` +
	`{"line_number":6,"line_type":4,"item_name":"Giao trong 3 ngày","quantity":0,"unit_price":0,"total_amount":0,"tax_rate":null,"tax_amount":0}]}}`

func TestInvoiceGet_TableShowsEveryField(t *testing.T) {
	f, out, _, _ := prodFactory(t, okJSON(prodInvoiceFull))

	require.NoError(t, run(f, "invoice", "get", "DH1009"))

	golden.Assert(t, "invoice_get_table", out.Bytes())
}

// A draft has no number, series, date or payment yet: those print "-"; empty optional lines are left out.
func TestInvoiceGet_TableOfADraft(t *testing.T) {
	const draft = `{"status":"success","data":{"reference_code":"DH1006","invoice_number":null,"invoice_series":null,"issued_date":null,"status":"draft","document_type":1,"provider_status":null,"tax_authority_code":null,"payment_method":null,"source":"manual","pdf_url":null,"xml_url":null,"buyer":{"name":"Khách lẻ"},"total_before_tax":null,"tax_amount":null,"total_amount":null,"notes":null}}`
	f, out, _, _ := prodFactory(t, okJSON(draft))

	require.NoError(t, run(f, "invoice", "get", "DH1006"))

	golden.Assert(t, "invoice_get_draft_table", out.Bytes())
}

func TestInvoiceList_Columns(t *testing.T) {
	f, out, _, _ := prodFactory(t, okJSON(prodInvoiceList))

	require.NoError(t, run(f, "invoice", "list"))

	golden.Assert(t, "invoice_list_table", out.Bytes())
}

func TestInvoiceGet_RefusesAPathAsTheReferenceCode(t *testing.T) {
	for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
		for _, ref := range []string{"..", "%2e%2e", "a/b", "a%2Fb", `a\b`} {
			t.Run(env+" "+ref, func(t *testing.T) {
				f, _, _, got := apiFactory(t, env, okJSON(prodInvoiceDetail))
				err := run(f, "invoice", "get", ref)
				require.Error(t, err)
				assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
				assert.Empty(t, got.paths, "nothing may reach the API")
			})
		}
	}
}

// The server urldecodes the path once, so every "+" must arrive as %2B to stay a "+", on both envs.
func TestInvoiceGet_SendsAPlusEscaped(t *testing.T) {
	for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
		t.Run(env, func(t *testing.T) {
			f, _, _, got := apiFactory(t, env, okJSON(prodInvoiceDetail))

			require.NoError(t, run(f, "invoice", "get", "DH+1+2 3"))

			assert.Equal(t, []string{"/einvoice/invoices/DH%2B1%2B2%203"}, got.paths)
		})
	}
}

// Both envs print one JSON shape for the same invoices.
func TestInvoice_JSONGolden(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		golden  string
		factory func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer)
	}{
		{"production get", []string{"invoice", "get", "DH1007"}, "invoice_get_json", func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer) {
			f, out, _, _ := prodFactory(t, okJSON(prodInvoiceDetail))
			return f, out
		}},
		{"sandbox get", []string{"invoice", "get", "DH1007"}, "invoice_get_json", func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer) {
			f, out, _, _ := sandboxAPIFactory(t, okJSON(prodInvoiceDetail))
			return f, out
		}},
		{"production list", []string{"invoice", "list"}, "invoice_list_json", func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer) {
			f, out, _, _ := prodFactory(t, okJSON(prodInvoiceList))
			return f, out
		}},
		{"sandbox list", []string{"invoice", "list"}, "invoice_list_json", func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer) {
			f, out, _, _ := sandboxAPIFactory(t, okJSON(prodInvoiceList))
			return f, out
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, out := tc.factory(t)
			f.Global.Output = "json"
			require.NoError(t, run(f, tc.args...))
			golden.Assert(t, tc.golden, out.Bytes())
		})
	}
}

func TestInvoiceRead_ProductionErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		h        http.HandlerFunc
		wantExit cmdutil.ExitCode
		wantMsg  string
	}{
		{"list without a scope", []string{"invoice", "list"}, apiError(403, "insufficient_scope", "Missing required scope: einvoice:read"), cmdutil.ExitScope, invoiceScopeMessage},
		{"get without a scope", []string{"invoice", "get", "DH1007"}, apiError(403, "insufficient_scope", "Missing required scope: einvoice:read"), cmdutil.ExitScope, invoiceScopeMessage},
		{"list without read", []string{"invoice", "list"}, apiError(403, "insufficient_scope", "Token thiếu quyền đọc dữ liệu"), cmdutil.ExitScope, invoiceScopeMessage},
		{"get without the permission", []string{"invoice", "get", "DH1007"}, apiError(403, "forbidden", "You do not have permission to perform this action"), cmdutil.ExitBlocked, ""},
		{"list without the permission", []string{"invoice", "list"}, apiError(403, "forbidden", "You do not have permission to perform this action"), cmdutil.ExitBlocked, ""},
		{"get of an unknown invoice", []string{"invoice", "get", "DH404"}, apiError(404, "not_found", "Invoice not found"), cmdutil.ExitError, "Invoice not found"},
		{"list with a parameter the server refuses", []string{"invoice", "list"}, apiError(422, "validation_error", "The per_page parameter must be between 1 and 100"), cmdutil.ExitError, "the request failed validation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, errOut, _ := prodFactory(t, tc.h)
			f.Global.Output = "json"

			err := run(f, tc.args...)

			require.Error(t, err)
			errOut.Reset() // drop cobra's own "Error:" line; only the JSON error remains
			assert.Equal(t, tc.wantExit, cmdutil.HandleError(f, err))
			if tc.wantMsg != "" {
				var got struct {
					Message string `json:"message"`
				}
				require.NoError(t, json.Unmarshal(errOut.Bytes(), &got), errOut.String())
				assert.Equal(t, tc.wantMsg, got.Message, "the text the user reads")
			}
		})
	}
}

// The invoice reads' exit-5 text replaces the generic one on both envs, and machine output keeps the
// request id.
func TestInvoiceRead_ScopeErrorKeepsTheRequestID(t *testing.T) {
	for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
		for _, args := range [][]string{{"invoice", "list"}, {"invoice", "get", "DH1007"}} {
			t.Run(env+" "+strings.Join(args, " "), func(t *testing.T) {
				f, _, errOut, _ := apiFactory(t, env, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("X-Request-Id", "req_abc123")
					apiError(403, "insufficient_scope", "Missing required scope: einvoice:read")(w, nil)
				})
				f.Global.Output = "json"

				err := run(f, args...)

				require.Error(t, err)
				errOut.Reset()
				assert.Equal(t, cmdutil.ExitScope, cmdutil.HandleError(f, err))
				var got struct {
					ErrorCode string `json:"error_code"`
					Message   string `json:"message"`
					RequestID string `json:"request_id"`
				}
				require.NoError(t, json.Unmarshal(errOut.Bytes(), &got), errOut.String())
				assert.Equal(t, "insufficient_scope", got.ErrorCode)
				assert.Equal(t, invoiceScopeMessage, got.Message)
				assert.Equal(t, "req_abc123", got.RequestID)
			})
		}
	}
}

// Not logged in on production: the reads stop at the token with exit 4 and call nothing.
func TestInvoiceRead_ProductionWithoutALoginExits4(t *testing.T) {
	for _, args := range [][]string{{"invoice", "list"}, {"invoice", "get", "DH1007"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f, _, _, got := prodFactory(t, okJSON(prodInvoiceList))
			f.NewAPIClient = func() (*api.Client, error) {
				return nil, cmdutil.AuthError{Err: errors.New("not logged in, run: sepay login")}
			}

			err := run(f, args...)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(err))
			assert.Empty(t, got.paths)
		})
	}
}

// A body that does not decode fails the command instead of printing an empty result.
func TestInvoiceRead_ProductionUndecodableBodyFails(t *testing.T) {
	for _, args := range [][]string{{"invoice", "list"}, {"invoice", "get", "DH1007"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f, _, _, _ := prodFactory(t, okJSON(`{"status":"success","data":"x"}`))

			err := run(f, args...)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "could not decode response")
		})
	}
}

// A list without paging data still prints its rows, with no footer.
func TestInvoiceList_ProductionWithoutPaging(t *testing.T) {
	f, out, errOut, _ := prodFactory(t, okJSON(`{"status":"success","data":[{"reference_code":"DH1","status":"issued"}]}`))

	require.NoError(t, run(f, "invoice", "list"))

	assert.Regexp(t, `(?m)^- +- +DH1 +issued +- +- +0$`, out.String(), "no number, original, date or buyer prints -")
	assert.NotContains(t, errOut.String(), "page ")
}

// An invoice without a buyer has no buyer block.
func TestInvoiceGet_WithoutABuyer(t *testing.T) {
	const body = `{"status":"success","data":{"reference_code":"DH1006","status":"draft","source":"manual","total_amount":0}}`
	f, out, _, _ := prodFactory(t, okJSON(body))

	require.NoError(t, run(f, "invoice", "get", "DH1006"))

	assert.NotContains(t, out.String(), "Buyer:")
	assert.Contains(t, out.String(), "Source:     manual\n\nSubtotal:")
}

// Only the two invoice reads may run on production; every other einvoice leaf keeps the sandbox
// pin. The hook is called on each leaf directly: running the leaf would fail its own checks first.
func TestEInvoice_OnlyInvoiceListAndGetRunOnProduction(t *testing.T) {
	f, _ := testFactory(t, okJSON(brokerEInvoiceBearer), okJSON(`{}`))
	ei := NewCmdEInvoice(f)
	allowed := map[string]bool{"einvoice invoice list": true, "einvoice invoice get": true}

	var leaves []*cobra.Command
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if !c.HasSubCommands() {
			leaves = append(leaves, c)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(ei)
	require.Greater(t, len(leaves), len(allowed))

	seen := 0
	for _, leaf := range leaves {
		t.Run(leaf.CommandPath(), func(t *testing.T) {
			f.Global.Env = config.EnvProduction

			err := ei.PersistentPreRunE(leaf, nil)

			if allowed[leaf.CommandPath()] {
				seen++
				require.NoError(t, err)
				assert.Equal(t, config.EnvProduction, f.Global.Env, "the env must not be rewritten")
				return
			}
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "einvoice commands are sandbox-only; drop --env production")
		})
	}
	assert.Equal(t, len(allowed), seen, "both invoice reads are leaves of the tree")
}

// A command named like an invoice read but not that command keeps the sandbox pin.
func TestEInvoice_ProductionHookMatchesTheCommandNotTheName(t *testing.T) {
	f, _ := testFactory(t, okJSON(brokerEInvoiceBearer), okJSON(`{}`))
	f.Global.Env = config.EnvProduction
	ei := NewCmdEInvoice(f)
	impostor := &cobra.Command{Use: "list"}
	(&cobra.Command{Use: "invoice"}).AddCommand(impostor)

	err := ei.PersistentPreRunE(impostor, nil)

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestInvoiceRead_SandboxKeepsTheSandboxGuard(t *testing.T) {
	for _, verb := range []string{"list", "get"} {
		t.Run(verb, func(t *testing.T) {
			f, _ := testFactory(t, okJSON(brokerEInvoiceBearer), okJSON(`{}`))
			f.Global.APIBase = "https://cli-api.sepay.vn/v2"
			ei := NewCmdEInvoice(f)
			leaf, _, err := ei.Find([]string{"invoice", verb})
			require.NoError(t, err)

			err = ei.PersistentPreRunE(leaf, nil)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "sandbox commands refuse the non-sandbox host")
		})
	}
}

func TestInvoiceRead_DefaultEnvProductionReadsProduction(t *testing.T) {
	for _, args := range [][]string{{"invoice", "list"}, {"invoice", "get", "DH1007"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			body := prodInvoiceList
			if args[1] == "get" {
				body = prodInvoiceDetail
			}
			f, _, _, got := prodFactory(t, okJSON(body))
			f.Global.Env = ""
			t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
			f.Config = func() (*config.Config, error) {
				cfg, err := config.Load()
				if err == nil {
					cfg.Set("default_env", config.EnvProduction)
				}
				return cfg, err
			}

			require.NoError(t, run(f, args...))
			assert.Len(t, got.paths, 1)
		})
	}
}

// get reads API v2 on sandbox with the user's token and never builds an e-invoice API client, so no
// product token is brokered for it.
func TestInvoiceGet_SandboxReadsAPIv2(t *testing.T) {
	f, out, _, got := sandboxAPIFactory(t, okJSON(prodInvoiceDetail))
	f.NewEInvoiceClient = func() (*iei.Client, error) {
		t.Error("get must not use the e-invoice API on sandbox")
		return nil, errors.New("e-invoice API client requested")
	}

	require.NoError(t, run(f, "invoice", "get", "DH1007"))

	assert.Equal(t, []string{"/einvoice/invoices/DH1007"}, got.paths)
	assert.Contains(t, out.String(), "Sản phẩm A (SP01)")
}

// The e-invoice API has no date, status or search filter, so list reads API v2 on sandbox too.
func TestInvoiceList_SandboxReadsAPIv2(t *testing.T) {
	f, out, _, got := sandboxAPIFactory(t, okJSON(prodInvoiceList))

	require.NoError(t, run(f, "invoice", "list", "--status", "issued"))

	assert.Equal(t, []string{"/einvoice/invoices"}, got.paths)
	assert.Equal(t, "issued", got.query.Get("status"))
	assert.Contains(t, out.String(), "1C26TSP0000123")
}

// On sandbox both reads keep the sandbox pin: an API host that is not the sandbox one is refused
// before any request leaves.
func TestInvoiceRead_SandboxRefusesAProductionHost(t *testing.T) {
	for _, tc := range []struct {
		args []string
		body string
	}{{[]string{"invoice", "list"}, prodInvoiceList}, {[]string{"invoice", "get", "DH1007"}, prodInvoiceDetail}} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			f, _, _, got := sandboxAPIFactory(t, okJSON(tc.body))
			f.Global.APIBase = "https://cli-api.sepay.vn/v2"

			err := run(f, tc.args...)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "sandbox commands refuse the non-sandbox host")
			assert.Empty(t, got.paths)
		})
	}
}

func TestInvoiceRead_ProductionSanitizesServerStrings(t *testing.T) {
	body := strings.Replace(prodInvoiceDetail, `"name":"Công ty A"`, `"name":"Công ty A\u001b[2J\u001b]0;pwn\u0007\u202e"`, 1)
	f, out, _, _ := prodFactory(t, okJSON(body))

	require.NoError(t, run(f, "invoice", "get", "DH1007"))

	for _, seq := range []string{"\x1b[2J", "\x1b]0;", "\x07", "\u202e"} {
		assert.NotContains(t, out.String(), seq)
	}
	assert.Contains(t, out.String(), "Công ty A")
}

// A sandbox login or API failure fails the read instead of printing an empty result.
func TestInvoiceRead_SandboxErrorsAreNotAnEmptyResult(t *testing.T) {
	t.Run("get without a login", func(t *testing.T) {
		f, out, _, got := sandboxAPIFactory(t, okJSON(prodInvoiceDetail))
		f.NewAPIClient = func() (*api.Client, error) {
			return nil, cmdutil.AuthError{Err: errors.New("not logged in, run: sepay login")}
		}

		err := run(f, "invoice", "get", "DH1007")

		assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(err))
		assert.NotContains(t, out.String(), "Ref:")
		assert.Empty(t, got.paths)
	})
	t.Run("get the API fails", func(t *testing.T) {
		f, out, _, _ := sandboxAPIFactory(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})

		err := run(f, "invoice", "get", "DH1007")

		assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
		assert.NotContains(t, out.String(), "Ref:")
	})
	t.Run("list without a login", func(t *testing.T) {
		f, out, errOut, _ := sandboxAPIFactory(t, okJSON(prodInvoiceList))
		f.NewAPIClient = func() (*api.Client, error) {
			return nil, cmdutil.AuthError{Err: errors.New("not logged in, run: sepay login")}
		}

		err := run(f, "invoice", "list")

		assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(err))
		assert.NotContains(t, out.String(), "REF")
		assert.NotContains(t, errOut.String(), "No invoices found.")
	})
	t.Run("list the API fails", func(t *testing.T) {
		f, out, errOut, _ := sandboxAPIFactory(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})

		err := run(f, "invoice", "list")

		assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
		assert.NotContains(t, out.String(), "REF")
		assert.NotContains(t, errOut.String(), "No invoices found.")
	})
}

// The REF column feeds get on both envs, and issue only on sandbox.
func TestInvoice_HelpTiesTheReferenceColumnToTheSandboxIssue(t *testing.T) {
	f, _ := testFactory(t, okJSON(brokerEInvoiceBearer), okJSON(`{}`))
	root := NewCmdEInvoice(f)
	long := func(path ...string) string {
		c, _, err := root.Find(path)
		require.NoError(t, err)
		return strings.Join(strings.Fields(c.Long), " ")
	}

	assert.Contains(t, long("invoice", "list"), `The REF column is the <reference-code> "einvoice invoice get" takes; on sandbox, "issue" takes it too.`)
	assert.Contains(t, long("invoice", "issue"), `<reference-code> is the REF column of "sepay einvoice invoice list" on sandbox.`)
}
