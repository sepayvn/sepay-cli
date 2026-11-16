package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const sandboxQuota = `{"status":"success","data":{"available":true,"quotas":{"transaction":{"used":12,"limit":500,"remaining":488,"period":"daily","enforcement":"blocked"},"bank_account":{"used":2,"limit":50,"remaining":48,"period":"total","enforcement":"blocked"},"webhook":{"used":1,"limit":50,"remaining":49,"period":"total","enforcement":"blocked"},"api_key":{"used":0,"limit":50,"remaining":50,"period":"total","enforcement":"blocked"}},"va_per_bank_account_limit":100}}`

const productionQuota = `{"status":"success","data":{"available":true,"cycle":{"begin":"2026-07-01","end":"2026-09-30","billing_cycle":"quarterly"},"quotas":{"transaction":{"used":18432,"limit":30000,"remaining":11568,"period":"cycle","enforcement":"capped"}}}}`

// A plan with no allowance omits limit and remaining.
const productionUncappedQuota = `{"status":"success","data":{"available":true,"cycle":{"begin":"2026-08-01","end":"2026-08-31","billing_cycle":"free"},"quotas":{"transaction":{"used":41,"period":"cycle","enforcement":"unlimited"}}}}`

const noPlanQuota = `{"status":"success","data":{"available":false,"message":"Cong ty chua co goi dich vu nao"}}`

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

func run(cmd *cobra.Command) error {
	cmd.SetArgs([]string{})
	return cmd.ExecuteContext(context.Background())
}

func okJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestQuota_SandboxTable(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(sandboxQuota))
	require.NoError(t, run(NewCmdQuota(f)))

	s := out.String()
	assert.Contains(t, s, "transaction")
	assert.Contains(t, s, "488")
	assert.Contains(t, s, "daily")
	assert.Contains(t, s, "api_key")
	assert.Contains(t, s, "VA per bank account: 100")
}

func TestQuota_SandboxRowOrder(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(sandboxQuota))
	require.NoError(t, run(NewCmdQuota(f)))

	s := out.String()
	assert.Less(t, indexOf(s, "transaction"), indexOf(s, "bank_account"))
	assert.Less(t, indexOf(s, "bank_account"), indexOf(s, "webhook"))
	assert.Less(t, indexOf(s, "webhook"), indexOf(s, "api_key"))
}

func TestQuota_ProductionTable(t *testing.T) {
	f, out, errOut := testFactory(t, okJSON(productionQuota))
	require.NoError(t, run(NewCmdQuota(f)))

	s := out.String()
	assert.Contains(t, s, "transaction")
	assert.Contains(t, s, "11568")
	assert.Contains(t, s, "cycle")
	assert.Contains(t, s, "capped")
	assert.Contains(t, s, "Cycle: 2026-07-01 → 2026-09-30 (quarterly)")
	// The 100-VA cap is sandbox-only.
	assert.NotContains(t, s, "VA per bank account")
	assert.NotContains(t, s, "bank_account")
	assert.NotContains(t, s, "api_key")
	// The CLI reports enforcement and claims nothing about billing.
	assert.NotContains(t, errOut.String(), "overage")
}

func TestQuota_ProductionUncapped(t *testing.T) {
	f, out, errOut := testFactory(t, okJSON(productionUncappedQuota))
	require.NoError(t, run(NewCmdQuota(f)))

	s := out.String()
	assert.Contains(t, s, "unlimited")
	assert.NotContains(t, s, " 0 ")
	assert.NotContains(t, errOut.String(), "overage")
}

// An exempt plan (a Free plan allowed to exceed its allowance) still sends limit and remaining.
const productionExemptQuota = `{"status":"success","data":{"available":true,"cycle":{"begin":"2026-11-01","end":"2026-11-30","billing_cycle":"free"},"quotas":{"transaction":{"used":41,"limit":50,"remaining":9,"period":"cycle","enforcement":"exempt"}}}}`

func TestQuota_ProductionExempt(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(productionExemptQuota))
	require.NoError(t, run(NewCmdQuota(f)))

	fields := strings.Fields(out.String())
	assert.Equal(t, []string{"transaction", "41", "50", "9", "cycle", "exempt"}, fields[6:12])
}

func TestQuota_ProductionExemptJSON(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(productionExemptQuota))
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdQuota(f)))

	s := out.String()
	assert.Contains(t, s, `"limit": 50`)
	assert.Contains(t, s, `"remaining": 9`)
	assert.Contains(t, s, `"enforcement": "exempt"`)
}

// Enforcement "unknown" sends a cycle with null dates.
func TestQuota_UnknownWindowOmitsCycleLine(t *testing.T) {
	body := `{"status":"success","data":{"available":true,"cycle":{"begin":null,"end":null,"billing_cycle":"monthly"},"quotas":{"transaction":{"used":0,"period":"cycle","enforcement":"unknown"}}}}`
	f, out, _ := testFactory(t, okJSON(body))
	require.NoError(t, run(NewCmdQuota(f)))

	s := out.String()
	assert.Contains(t, s, "unknown")
	assert.NotContains(t, s, "Cycle:")
}

func TestQuota_CycleStripsTerminalEscapes(t *testing.T) {
	body := `{"status":"success","data":{"available":true,"cycle":{"begin":"2026-07-01","end":"2026-09-30","billing_cycle":"quarterly\u001b[2J\u001b]0;pwned\u0007"},"quotas":{"transaction":{"used":1,"limit":10,"remaining":9,"period":"cycle","enforcement":"capped"}}}}`
	f, out, _ := testFactory(t, okJSON(body))
	require.NoError(t, run(NewCmdQuota(f)))

	s := out.String()
	assert.Contains(t, s, "quarterly")
	assert.NotContains(t, s, "\u001b[2J")
	assert.NotContains(t, s, "\u001b]0;")
}

func TestQuota_NoPlan(t *testing.T) {
	f, out, errOut := testFactory(t, okJSON(noPlanQuota))
	require.NoError(t, run(NewCmdQuota(f)))

	assert.Empty(t, out.String())
	assert.Contains(t, errOut.String(), "Quota is not available in this environment.")
	assert.NotContains(t, errOut.String(), "Cong ty chua co goi")
}

// With no plan, -o json carries only available and the server's message.
func TestQuota_NoPlanJSON(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(noPlanQuota))
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdQuota(f)))

	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, map[string]any{"available": false, "message": "Cong ty chua co goi dich vu nao"}, got)
}

func TestQuota_JSON(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(sandboxQuota))
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdQuota(f)))

	s := out.String()
	assert.Contains(t, s, `"available": true`)
	assert.Contains(t, s, `"remaining": 488`)
	assert.Contains(t, s, `"va_per_bank_account_limit": 100`)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

const productionEinvoiceQuota = `{"status":"success","data":{"available":true,"cycle":{"begin":"2026-11-01","end":"2026-11-30","billing_cycle":"monthly"},"quotas":{"transaction":{"used":120,"limit":1000,"remaining":880,"period":"cycle","enforcement":"capped"},"einvoice":{"used":12,"remaining":88}}}}`

func TestQuota_ProductionEinvoiceRow(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(productionEinvoiceQuota))
	require.NoError(t, run(NewCmdQuota(f)))

	fields := strings.Fields(out.String())
	assert.Equal(t, []string{"transaction", "120", "1000", "880", "cycle", "capped"}, fields[6:12])
	assert.Equal(t, []string{"einvoice", "12", "-", "88", "-", "-"}, fields[12:18])
}

// A server that still sends "unlimited": true without remaining gets "-", the no-figure mark.
const productionEinvoiceNoRemainingQuota = `{"status":"success","data":{"available":true,"cycle":{"begin":"2026-11-01","end":"2026-11-30","billing_cycle":"monthly"},"quotas":{"transaction":{"used":120,"limit":1000,"remaining":880,"period":"cycle","enforcement":"capped"},"einvoice":{"used":12,"unlimited":true}}}}`

func TestQuota_EinvoiceWithoutRemainingIsDash(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(productionEinvoiceNoRemainingQuota))
	require.NoError(t, run(NewCmdQuota(f)))

	fields := strings.Fields(out.String())
	assert.Equal(t, []string{"einvoice", "12", "-", "-", "-", "-"}, fields[12:18])
}

func TestQuota_ProductionEinvoiceJSON(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(productionEinvoiceQuota))
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdQuota(f)))

	var got struct {
		Quotas map[string]map[string]any `json:"quotas"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, map[string]any{"used": float64(12), "remaining": float64(88)}, got.Quotas["einvoice"])
	assert.Equal(t, "cycle", got.Quotas["transaction"]["period"])
}

func TestQuota_ProductionEinvoiceYAML(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(productionEinvoiceQuota))
	f.Global.Output = "yaml"
	require.NoError(t, run(NewCmdQuota(f)))

	var got struct {
		Quotas map[string]map[string]any `yaml:"quotas"`
	}
	require.NoError(t, yaml.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, map[string]any{"used": 12, "remaining": 88}, got.Quotas["einvoice"])
	assert.Equal(t, "cycle", got.Quotas["transaction"]["period"])
}

// An active plan without an e-invoice plan carries no einvoice key.
func TestQuota_NoEinvoicePlanHasNoEinvoiceRow(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(productionQuota))
	require.NoError(t, run(NewCmdQuota(f)))
	assert.NotContains(t, out.String(), "einvoice")
	assert.Contains(t, out.String(), "transaction")

	for output, unmarshal := range map[string]func([]byte, any) error{"json": json.Unmarshal, "yaml": yaml.Unmarshal} {
		t.Run(output, func(t *testing.T) {
			f, out, _ := testFactory(t, okJSON(productionQuota))
			f.Global.Output = output
			require.NoError(t, run(NewCmdQuota(f)))

			var got struct {
				Quotas map[string]any `json:"quotas" yaml:"quotas"`
			}
			require.NoError(t, unmarshal(out.Bytes(), &got))
			assert.NotContains(t, got.Quotas, "einvoice")
			assert.Contains(t, got.Quotas, "transaction")
		})
	}
}

func TestQuota_HelpDescribesTheEinvoiceRow(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(productionQuota))
	long := NewCmdQuota(f).Long

	assert.Contains(t, long, "e-invoice quota")
	assert.Contains(t, long, `the einvoice row shows the e-invoices issued to date (USED)`)
	assert.Contains(t, long, `the number your plan still lets you issue (REMAINING);`)
	assert.Contains(t, long, "There is no einvoice row if your company has not bought an\ne-invoice plan.")
	// "unlimited" stays an ENFORCEMENT value of the transaction row, never an einvoice state.
	_, einvoice, ok := strings.Cut(long, "the einvoice row")
	require.True(t, ok)
	assert.NotContains(t, einvoice, "unlimited")
}
