package pg

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/internal/golden"
	ipg "github.com/sepayvn/sepay-cli/internal/pg"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const prodOrderList = `{"status":"success","data":[` +
	`{"id":"ord-2","customer_id":null,"order_id":"ORD-2","order_invoice_number":"INV-B","order_status":"CANCELLED","order_amount":200000,"order_currency":"VND","order_description":"Order 2","authentication_status":null,"created_at":"2026-11-02 10:00:00","updated_at":"2026-11-02 10:00:00","payment_method":"BANK_TRANSFER","merchant":{"name":"Shop B"}},` +
	`{"id":"ord-1","customer_id":"CUST-1","order_id":"ORD-1","order_invoice_number":"INV-A","order_status":"CAPTURED","order_amount":150000,"order_currency":"VND","order_description":"Order 1","authentication_status":null,"created_at":"2026-11-01 10:00:00","updated_at":"2026-11-01 10:00:00","payment_method":"CARD","merchant":{"name":"Shop A"}}` +
	`],"meta":{"pagination":{"total":2,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`

const prodOrderDetail = `{"status":"success","data":{"id":"ord-1","customer_id":"CUST-1","order_id":"ORD-1","order_invoice_number":"INV-A","order_status":"CAPTURED","order_amount":150000,"order_currency":"VND","order_description":"Order 1","authentication_status":"AUTHENTICATION_SUCCESSFUL","created_at":"2026-11-01 10:00:00","updated_at":"2026-11-01 10:00:00","payment_method":"CARD","merchant":{"name":"Shop A"},"transactions":[` +
	`{"id":"tx-12","payment_method":"CARD","transaction_type":"PAYMENT","transaction_amount":100,"transaction_currency":"VND","transaction_status":"APPROVED","authentication_status":null,"transaction_date":"2026-11-01 10:01:02","transaction_last_updated_date":"2026-11-01 10:02:00"},` +
	`{"id":"tx-11","payment_method":"CARD","transaction_type":"PAYMENT","transaction_amount":150000,"transaction_currency":"VND","transaction_status":"APPROVED","authentication_status":"AUTHENTICATION_SUCCESSFUL","card":{"brand":"VISA","last4":"1234"},"transaction_date":"2026-11-01 10:01:01","transaction_last_updated_date":"2026-11-01 10:02:00"}` +
	`]}}`

// apiRequests records what the API v2 host received.
type apiRequests struct {
	paths []string
	query url.Values
}

// prodFactory points the API v2 client at h on production and makes the gateway fail the test if used.
func prodFactory(t *testing.T, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer, *apiRequests) {
	t.Helper()
	return apiFactory(t, config.EnvProduction, h)
}

// sandboxFactory is prodFactory on sandbox, where order list and get read API v2 as well.
func sandboxFactory(t *testing.T, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer, *apiRequests) {
	t.Helper()
	return apiFactory(t, config.EnvSandbox, h)
}

func apiFactory(t *testing.T, env string, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer, *apiRequests) {
	t.Helper()
	f, out, errOut := testFactory(t, okJSON(brokerPGBasic), func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("order list and get must not call the sandbox gateway: %s", r.URL.EscapedPath())
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
	return f, out, errOut, got
}

func apiError(status int, code, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"` + code + `","message":"` + message + `"}`))
	}
}

func TestOrderList_ProductionReadsAPIv2(t *testing.T) {
	f, out, errOut, got := prodFactory(t, okJSON(prodOrderList))

	require.NoError(t, run(f, "order", "list", "--status", "CAPTURED", "--from", "2026-11-01", "--to", "2026-11-05",
		"--customer", "CUST-1", "--search", "INV_A", "--page", "2", "--per-page", "20"))

	assert.Equal(t, []string{"/pg/orders"}, got.paths)
	assert.Equal(t, url.Values{
		"order_status": {"CAPTURED"}, "from_created_at": {"2026-11-01"}, "to_created_at": {"2026-11-05"},
		"customer_id": {"CUST-1"}, "q": {"INV_A"}, "page": {"2"}, "per_page": {"20"},
	}, got.query)
	golden.Assert(t, "order_list_table", out.Bytes())
	assert.Contains(t, errOut.String(), "page 1/1 · 2 of 2")
}

func TestOrderGet_ProductionReadsAPIv2(t *testing.T) {
	f, out, _, got := prodFactory(t, okJSON(prodOrderDetail))

	require.NoError(t, run(f, "order", "get", "INV-A"))

	assert.Equal(t, []string{"/pg/orders/INV-A"}, got.paths)
	golden.Assert(t, "order_get_table", out.Bytes())
}

// An order line the server sends as null is left out, never printed empty.
func TestOrderGet_SkipsLinesTheServerLeftEmpty(t *testing.T) {
	body := strings.NewReplacer(`"customer_id":"CUST-1"`, `"customer_id":null`,
		`"order_description":"Order 1","authentication_status":"AUTHENTICATION_SUCCESSFUL","created_at":"2026-11-01 10:00:00","updated_at":"2026-11-01 10:00:00"`,
		`"order_description":"","authentication_status":null,"created_at":"","updated_at":""`).Replace(prodOrderDetail)
	f, out, _, _ := prodFactory(t, okJSON(body))

	require.NoError(t, run(f, "order", "get", "INV-A"))

	s := out.String()
	for _, label := range []string{"Customer:", "Description:", "3-D Secure:", "Created:", "Updated:"} {
		assert.NotContains(t, s, label)
	}
	assert.Contains(t, s, "Order code:   INV-A\nPay code:     ORD-1\nStatus:       CAPTURED\nAmount:       150.000 VND\n\n")
}

func TestOrderGet_RefusesAPathAsTheInvoice(t *testing.T) {
	for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
		for _, inv := range []string{"..", "%2e%2e", "a/b", "a%2Fb", `a\b`} {
			t.Run(env+" "+inv, func(t *testing.T) {
				f, _, _, got := apiFactory(t, env, okJSON(prodOrderDetail))
				err := run(f, "order", "get", inv)
				require.Error(t, err)
				assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
				assert.Empty(t, got.paths, "nothing may reach the API")
			})
		}
	}
}

// The server urldecodes the path once, so a "+" must arrive as %2B to stay a "+", on both envs.
func TestOrderGet_SendsAPlusInTheInvoiceEscaped(t *testing.T) {
	for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
		t.Run(env, func(t *testing.T) {
			f, _, _, got := apiFactory(t, env, okJSON(prodOrderDetail))
			require.NoError(t, run(f, "order", "get", "INV+1+2 3"))
			assert.Equal(t, []string{"/pg/orders/INV%2B1%2B2%203"}, got.paths)
		})
	}
}

// Both envs read API v2 and print one JSON shape: integer amounts, card {brand,last4}, no card on a
// non-card row.
func TestOrder_JSONGolden(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		golden  string
		factory func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer)
	}{
		{"production get", []string{"order", "get", "INV-A"}, "order_get_json", func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer) {
			f, out, _, _ := prodFactory(t, okJSON(prodOrderDetail))
			return f, out
		}},
		{"sandbox get", []string{"order", "get", "INV-A"}, "order_get_json", func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer) {
			f, out, _, _ := sandboxFactory(t, okJSON(prodOrderDetail))
			return f, out
		}},
		{"production list", []string{"order", "list"}, "order_list_json", func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer) {
			f, out, _, _ := prodFactory(t, okJSON(prodOrderList))
			return f, out
		}},
		{"sandbox list", []string{"order", "list"}, "order_list_json", func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer) {
			f, out, _, _ := sandboxFactory(t, okJSON(prodOrderList))
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

// orderScopeMessage is the order reads' exit-5 text, pinned word for word.
const orderScopeMessage = "this command needs the read and pg:read scopes, granted at sepay login to users with the permission «Cổng thanh toán › Xem danh sách đơn hàng»; ask an admin of your company if you lack it"

// API v2 errors map to the same exit codes and text on both envs.
func TestOrderRead_APIv2Errors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		h        http.HandlerFunc
		wantExit cmdutil.ExitCode
		wantMsg  string
	}{
		{"list without a scope", []string{"order", "list"}, apiError(403, "insufficient_scope", "Missing required scope: pg:read"), cmdutil.ExitScope, orderScopeMessage},
		{"get without a scope", []string{"order", "get", "INV-A"}, apiError(403, "insufficient_scope", "Missing required scope: pg:read"), cmdutil.ExitScope, orderScopeMessage},
		{"get without read", []string{"order", "get", "INV-A"}, apiError(403, "insufficient_scope", "Token thiếu quyền đọc dữ liệu"), cmdutil.ExitScope, orderScopeMessage},
		{"list without the permission", []string{"order", "list"}, apiError(403, "forbidden", "You do not have permission to perform this action"), cmdutil.ExitBlocked, ""},
		{"get of an unknown invoice", []string{"order", "get", "INV-X"}, apiError(404, "not_found", "Order not found"), cmdutil.ExitError, "Order not found"},
		{"list with a parameter the server refuses", []string{"order", "list", "--search", "x"}, apiError(422, "validation_error", "The q parameter is too long"), cmdutil.ExitError, "the request failed validation"},
	} {
		for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
			t.Run(env+" "+tc.name, func(t *testing.T) {
				f, _, errOut, _ := apiFactory(t, env, tc.h)
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
}

// Not logged in: the reads stop at the token with exit 4 on both envs, call nothing and print no
// empty result.
func TestOrderRead_WithoutALoginExits4(t *testing.T) {
	for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
		for _, args := range [][]string{{"order", "list"}, {"order", "get", "INV-A"}} {
			t.Run(env+" "+strings.Join(args, " "), func(t *testing.T) {
				f, out, errOut, got := apiFactory(t, env, okJSON(prodOrderList))
				f.NewAPIClient = func() (*api.Client, error) {
					return nil, cmdutil.AuthError{Err: errors.New("not logged in, run: sepay login")}
				}

				err := run(f, args...)

				require.Error(t, err)
				assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(err))
				assert.Empty(t, got.paths)
				assert.NotContains(t, out.String(), "Order code:")
				assert.NotContains(t, errOut.String(), "No orders found.")
			})
		}
	}
}

// The largest amount the gateway stores (decimal(20,2)) reads exactly on both envs, and one such order
// must not fail the whole read: a float64 cannot hold it.
func TestOrderRead_LargestStoredAmountKeepsTheRead(t *testing.T) {
	huge := strings.NewReplacer(
		`"order_amount":200000,`, `"order_amount":1000000000000000000,`,
		`"order_amount":150000,`, `"order_amount":1000000000000000000,`,
	).Replace
	for _, tc := range []struct {
		name    string
		args    []string
		factory func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer)
	}{
		{"production list", []string{"order", "list"}, func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer) {
			f, out, _, _ := prodFactory(t, okJSON(huge(prodOrderList)))
			return f, out
		}},
		{"production get", []string{"order", "get", "INV-A"}, func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer) {
			f, out, _, _ := prodFactory(t, okJSON(huge(prodOrderDetail)))
			return f, out
		}},
		{"sandbox list", []string{"order", "list"}, func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer) {
			f, out, _, _ := sandboxFactory(t, okJSON(huge(prodOrderList)))
			return f, out
		}},
		{"sandbox get", []string{"order", "get", "INV-A"}, func(t *testing.T) (*cmdutil.Factory, *bytes.Buffer) {
			f, out, _, _ := sandboxFactory(t, okJSON(huge(prodOrderDetail)))
			return f, out
		}},
	} {
		t.Run(tc.name+" table", func(t *testing.T) {
			f, out := tc.factory(t)
			require.NoError(t, run(f, tc.args...))
			assert.Contains(t, out.String(), "1.000.000.000.000.000.000")
			assert.Contains(t, out.String(), "INV-A")
		})
		t.Run(tc.name+" json", func(t *testing.T) {
			f, out := tc.factory(t)
			f.Global.Output = "json"
			require.NoError(t, run(f, tc.args...))
			assert.Contains(t, out.String(), `"order_amount": 1000000000000000000`)
		})
	}
}

// An amount the decoder refuses fails the command on both envs instead of printing an empty result.
func TestOrderRead_MalformedAmountFails(t *testing.T) {
	bad := func(body string) string {
		return strings.ReplaceAll(body, `"order_amount":150000`, `"order_amount":"abc"`)
	}
	for _, tc := range []struct {
		name    string
		args    []string
		factory func(t *testing.T) *cmdutil.Factory
	}{
		{"production list", []string{"order", "list"}, func(t *testing.T) *cmdutil.Factory {
			f, _, _, _ := prodFactory(t, okJSON(bad(prodOrderList)))
			return f
		}},
		{"production get", []string{"order", "get", "INV-A"}, func(t *testing.T) *cmdutil.Factory {
			f, _, _, _ := prodFactory(t, okJSON(bad(prodOrderDetail)))
			return f
		}},
		{"sandbox list", []string{"order", "list"}, func(t *testing.T) *cmdutil.Factory {
			f, _, _, _ := sandboxFactory(t, okJSON(bad(prodOrderList)))
			return f
		}},
		{"sandbox get", []string{"order", "get", "INV-A"}, func(t *testing.T) *cmdutil.Factory {
			f, _, _, _ := sandboxFactory(t, okJSON(bad(prodOrderDetail)))
			return f
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.factory(t), tc.args...)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), `amount "abc" is not a VND amount`)
		})
	}
}

// Only the two order reads may run on production; every other pg leaf keeps the sandbox pin. The
// hook is called on each leaf directly: running the leaf would fail its own argument checks first.
func TestPG_OnlyOrderListAndGetRunOnProduction(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(`{}`))
	pg := NewCmdPG(f)
	allowed := map[string]bool{"pg order list": true, "pg order get": true}

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
	walk(pg)
	require.Greater(t, len(leaves), len(allowed))

	seen := 0
	for _, leaf := range leaves {
		t.Run(leaf.CommandPath(), func(t *testing.T) {
			f.Global.Env = config.EnvProduction

			err := pg.PersistentPreRunE(leaf, nil)

			if allowed[leaf.CommandPath()] {
				seen++
				require.NoError(t, err)
				assert.Equal(t, config.EnvProduction, f.Global.Env, "the env must not be rewritten")
				return
			}
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "pg commands are sandbox-only; drop --env production")
		})
	}
	assert.Equal(t, len(allowed), seen, "both order reads are leaves of the tree")
}

// A command named like an order read but not that command keeps the sandbox pin.
func TestPG_ProductionHookMatchesTheCommandNotTheName(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(`{}`))
	f.Global.Env = config.EnvProduction
	pg := NewCmdPG(f)
	impostor := &cobra.Command{Use: "list"}
	(&cobra.Command{Use: "order"}).AddCommand(impostor)

	err := pg.PersistentPreRunE(impostor, nil)

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

// On sandbox the reads keep the sandbox pin: a production API host is refused before any request.
func TestOrderRead_SandboxKeepsTheSandboxGuard(t *testing.T) {
	for _, args := range [][]string{{"order", "list"}, {"order", "get", "INV-A"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f, _, _, got := sandboxFactory(t, okJSON(prodOrderDetail))
			f.Global.APIBase = "https://cli-api.sepay.vn/v2"

			err := run(f, args...)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "sandbox commands refuse the non-sandbox host")
			assert.Empty(t, got.paths, "nothing may reach API v2")
		})
	}
}

func TestOrderRead_DefaultEnvProductionReadsProduction(t *testing.T) {
	for _, args := range [][]string{{"order", "list"}, {"order", "get", "INV-A"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			body := prodOrderList
			if args[1] == "get" {
				body = prodOrderDetail
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

// On sandbox list and get read API v2 with the user's token, the same paths and query as production,
// and print the same table; the gateway and its brokered credential are never touched.
func TestOrderRead_SandboxReadsAPIv2NotTheGateway(t *testing.T) {
	noGateway := func(f *cmdutil.Factory) {
		f.NewPGClient = func() (*ipg.Client, error) {
			t.Error("order list and get must not build a gateway client")
			return nil, errors.New("no gateway")
		}
	}

	f, out, _, got := sandboxFactory(t, okJSON(prodOrderList))
	noGateway(f)
	require.NoError(t, run(f, "order", "list", "--status", "CAPTURED", "--from", "2026-11-01", "--to", "2026-11-05",
		"--customer", "CUST-1", "--search", "INV_A", "--page", "2", "--per-page", "20"))
	assert.Equal(t, []string{"/pg/orders"}, got.paths)
	assert.Equal(t, url.Values{
		"order_status": {"CAPTURED"}, "from_created_at": {"2026-11-01"}, "to_created_at": {"2026-11-05"},
		"customer_id": {"CUST-1"}, "q": {"INV_A"}, "page": {"2"}, "per_page": {"20"},
	}, got.query)
	golden.Assert(t, "order_list_table", out.Bytes())

	f, out, _, got = sandboxFactory(t, okJSON(prodOrderDetail))
	noGateway(f)
	require.NoError(t, run(f, "order", "get", "INV-A"))
	assert.Equal(t, []string{"/pg/orders/INV-A"}, got.paths)
	golden.Assert(t, "order_get_table", out.Bytes())
}

func TestOrderGet_ProductionSanitizesCardBrand(t *testing.T) {
	body := strings.Replace(prodOrderDetail, `"brand":"VISA"`, `"brand":"VISA\u001b[2J\u001b]0;pwn\u0007"`, 1)
	f, out, _, _ := prodFactory(t, okJSON(body))

	require.NoError(t, run(f, "order", "get", "INV-A"))

	for _, seq := range []string{"\x1b[2J", "\x1b]0;", "\x07"} {
		assert.NotContains(t, out.String(), seq)
	}
	assert.Contains(t, out.String(), "****1234")
}

// The footer reads last_page for the page total on both envs; a zero total prints no footer.
func TestOrderList_FooterUsesLastPage(t *testing.T) {
	for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
		t.Run(env, func(t *testing.T) {
			body := strings.Replace(prodOrderList, `"pagination":{"total":2,"per_page":20,"current_page":1,"last_page":1,"has_more":false}`,
				`"pagination":{"total":45,"per_page":20,"current_page":2,"last_page":3,"has_more":true}`, 1)
			f, _, errOut, _ := apiFactory(t, env, okJSON(body))
			require.NoError(t, run(f, "order", "list", "--page", "2"))
			assert.Contains(t, errOut.String(), "page 2/3 · 2 of 45")

			body = strings.Replace(prodOrderList, `"total":2`, `"total":0`, 1)
			f, out, errOut, _ := apiFactory(t, env, okJSON(body))
			require.NoError(t, run(f, "order", "list"))
			assert.Contains(t, out.String(), "INV-A")
			assert.NotContains(t, errOut.String(), "page ")
		})
	}
}

// The get picker pages through API v2 on both envs, 100 rows a page until has_more is false, then
// reads the pick; the gateway is never listed.
func TestOrderGet_PickerPagesAPIv2(t *testing.T) {
	page := func(invoice string, hasMore bool) string {
		return `{"status":"success","data":[{"id":"o-` + invoice + `","order_id":"ORD","order_invoice_number":"` + invoice +
			`","order_status":"CAPTURED","order_amount":150000,"order_currency":"VND"}],"meta":{"pagination":{"total":2,"per_page":100,"current_page":1,"last_page":2,"has_more":` +
			strconv.FormatBool(hasMore) + `}}}`
	}
	for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
		t.Run(env, func(t *testing.T) {
			var perPage []string
			f, _, _, got := apiFactory(t, env, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/pg/orders" && r.URL.Query().Get("page") == "1":
					perPage = append(perPage, r.URL.Query().Get("per_page"))
					_, _ = w.Write([]byte(page("INV-1", true)))
				case r.URL.Path == "/pg/orders":
					perPage = append(perPage, r.URL.Query().Get("per_page"))
					_, _ = w.Write([]byte(page("INV-2", false)))
				default:
					_, _ = w.Write([]byte(prodOrderDetail))
				}
			})
			f.IOStreams.SetColorEnabled(true)
			prompts := prompttest.Use(t, f, prompttest.Step{Label: "Select an order", Answer: "INV-2 · CAPTURED · 150.000 VND"})

			require.NoError(t, run(f, "order", "get"))

			assert.Equal(t, []string{"/pg/orders", "/pg/orders", "/pg/orders/INV-2"}, got.paths)
			assert.Equal(t, []string{"100", "100"}, perPage)
			assert.Equal(t, []string{"INV-1 · CAPTURED · 150.000 VND", "INV-2 · CAPTURED · 150.000 VND"}, prompts.Offered["Select an order"])
		})
	}
}

// A page without paging data ends the picker's loop after one request.
func TestOrderGet_PickerStopsOnAPageWithoutPaging(t *testing.T) {
	var lists int
	f, _, _, _ := prodFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pg/orders" {
			lists++
			_, _ = w.Write([]byte(`{"status":"success","data":[{"id":"o-1","order_invoice_number":"INV-1","order_status":"CAPTURED","order_amount":1,"order_currency":"VND"}]}`))
			return
		}
		_, _ = w.Write([]byte(prodOrderDetail))
	})
	prompttest.Use(t, f, prompttest.Step{Label: "Select an order", Answer: "INV-1 · CAPTURED · 1 VND"})

	require.NoError(t, run(f, "order", "get"))

	assert.Equal(t, 1, lists)
}

// cancel and void stay on the sandbox gateway, picker included: they list the gateway's orders, which
// are the ones its brokered merchant can change, page by page with its page_count paging, and never
// API v2.
func TestOrderCancelAndVoid_PickersListTheGateway(t *testing.T) {
	gatewayPage := func(invoice string, hasMore bool) string {
		return `{"data":[{"id":"o-` + invoice + `","order_id":"ORD","order_invoice_number":"` + invoice +
			`","order_status":"CAPTURED","order_amount":"150000.00","order_currency":"VND"}],"meta":{"per_page":100,"total":2,"current_page":1,"page_count":2,"has_more":` +
			strconv.FormatBool(hasMore) + `}}`
	}
	for _, tc := range []struct {
		verb, label, post string
	}{
		{"cancel", "Select an order to cancel", "/v1/order/cancel"},
		{"void", "Select an order to void", "/v1/order/voidTransaction"},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			var paths, perPage []string
			f, _, _ := testFactory(t, okJSON(brokerPGBasic), func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				switch {
				case r.URL.Path == "/v1/order" && r.URL.Query().Get("page") == "1":
					perPage = append(perPage, r.URL.Query().Get("per_page"))
					_, _ = w.Write([]byte(gatewayPage("INV-1", true)))
				case r.URL.Path == "/v1/order":
					perPage = append(perPage, r.URL.Query().Get("per_page"))
					_, _ = w.Write([]byte(gatewayPage("INV-2", false)))
				default:
					_, _ = w.Write([]byte(`{"message":"ok"}`))
				}
			})
			apiSrv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				t.Errorf("%s must not call API v2: %s", tc.verb, r.URL.Path)
			}))
			t.Cleanup(apiSrv.Close)
			f.NewAPIClient = func() (*api.Client, error) {
				c := api.NewClient(apiSrv.URL, "tok")
				c.MaxRetries = 0
				return c, nil
			}
			f.Global.Yes = true
			prompts := prompttest.Use(t, f, prompttest.Step{Label: tc.label, Answer: "INV-2 · CAPTURED · 150.000 VND"})

			require.NoError(t, run(f, "order", tc.verb))

			assert.Equal(t, []string{"/v1/order", "/v1/order", tc.post}, paths)
			assert.Equal(t, []string{"100", "100"}, perPage)
			assert.Equal(t, []string{"INV-1 · CAPTURED · 150.000 VND", "INV-2 · CAPTURED · 150.000 VND"}, prompts.Offered[tc.label])
		})
	}
}

// The order reads' exit-5 text replaces the generic one, and machine output keeps the request id.
func TestOrderRead_ScopeErrorKeepsTheRequestID(t *testing.T) {
	f, _, errOut, _ := prodFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "req_abc123")
		apiError(403, "insufficient_scope", "Missing required scope: pg:read")(w, nil)
	})
	f.Global.Output = "json"

	err := run(f, "order", "list")

	require.Error(t, err)
	errOut.Reset() // drop cobra's own "Error:" line; only the JSON error remains
	assert.Equal(t, cmdutil.ExitScope, cmdutil.HandleError(f, err))
	var got struct {
		ErrorCode string `json:"error_code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	require.NoError(t, json.Unmarshal(errOut.Bytes(), &got), errOut.String())
	assert.Equal(t, "insufficient_scope", got.ErrorCode)
	assert.Equal(t, orderScopeMessage, got.Message)
	assert.Equal(t, "req_abc123", got.RequestID)
}

// An invoice number on two orders is offered once, as the newest order, which is the one get reads.
func TestOrderGet_PickerOffersEachInvoiceOnce(t *testing.T) {
	list := `{"status":"success","data":[` +
		`{"id":"o-4","order_id":"ORD-4","order_invoice_number":"INV-1","order_status":"CAPTURED","order_amount":250000,"order_currency":"VND"},` +
		`{"id":"o-2","order_id":"ORD-2","order_invoice_number":"INV-2","order_status":"CAPTURED","order_amount":150000,"order_currency":"VND"},` +
		`{"id":"o-1","order_id":"ORD-1","order_invoice_number":"INV-1","order_status":"CANCELLED","order_amount":250000,"order_currency":"VND"}` +
		`],"meta":{"pagination":{"total":3,"per_page":100,"current_page":1,"last_page":1,"has_more":false}}}`
	f, _, _, _ := prodFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pg/orders" {
			_, _ = w.Write([]byte(list))
			return
		}
		_, _ = w.Write([]byte(prodOrderDetail))
	})
	prompts := prompttest.Use(t, f, prompttest.Step{Label: "Select an order", Answer: "INV-1 · CAPTURED · 250.000 VND"})

	require.NoError(t, run(f, "order", "get"))

	assert.Equal(t, []string{"INV-1 · CAPTURED · 250.000 VND", "INV-2 · CAPTURED · 150.000 VND"}, prompts.Offered["Select an order"])
}

// A filter left unset is not sent on either env: the server refuses an empty order_status and page=0.
func TestOrderList_SendsNoUnsetFilter(t *testing.T) {
	for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
		t.Run(env, func(t *testing.T) {
			f, _, _, got := apiFactory(t, env, okJSON(prodOrderList))

			require.NoError(t, run(f, "order", "list"))

			assert.Equal(t, []string{"/pg/orders"}, got.paths)
			assert.Empty(t, got.query)
		})
	}
}

// A server failure fails the list on both envs instead of printing an empty result.
func TestOrderList_AServerFailureIsNotAnEmptyResult(t *testing.T) {
	for _, env := range []string{config.EnvProduction, config.EnvSandbox} {
		t.Run(env, func(t *testing.T) {
			f, out, errOut, _ := apiFactory(t, env, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			})

			err := run(f, "order", "list")

			assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
			assert.NotContains(t, out.String(), "ORDER CODE")
			assert.NotContains(t, errOut.String(), "No orders found.")
		})
	}
}

// The ORDER CODE column feeds get on both envs, and cancel and void only on sandbox.
func TestOrder_HelpTiesTheOrderCodeColumnToTheSandboxWrites(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(`{}`))
	root := NewCmdPG(f)
	long := func(path ...string) string {
		c, _, err := root.Find(path)
		require.NoError(t, err)
		return strings.Join(strings.Fields(c.Long), " ")
	}

	assert.Contains(t, long("order", "list"), `The ORDER CODE column is the <invoice-number> "pg order get" takes; on sandbox, "cancel" and "void" take it for an order of the sandbox merchant.`)
	for _, verb := range []string{"cancel", "void"} {
		assert.Contains(t, long("order", verb), `<invoice-number> is the ORDER CODE column of "sepay pg order list" for an order of the sandbox merchant.`, verb)
	}
}
