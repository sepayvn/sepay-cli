package pg

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	ipg "github.com/sepayvn/sepay-cli/internal/pg"
	"github.com/sepayvn/sepay-cli/internal/product"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

const brokerPGBasic = `{"status":"success","data":{"auth_type":"basic","merchant_id":"SP-TEST-1","secret_key":"spsk_test_SECRET"}}`

// testFactory wires a fake Factory to a broker server and a PG host server.
func testFactory(t *testing.T, brokerH, pgH http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	keyring.MockInit()
	brokerSrv := httptest.NewServer(brokerH)
	t.Cleanup(brokerSrv.Close)
	pgSrv := httptest.NewServer(pgH)
	t.Cleanup(pgSrv.Close)

	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table", Env: "sandbox"}}

	mgr := product.NewManager(product.NewBroker(api.NewClient(brokerSrv.URL, "tok")), product.NewStore(t.TempDir()), "default", "sandbox")
	f.ProductManager = func() (*product.Manager, error) { return mgr, nil }
	f.NewPGClient = func() (*ipg.Client, error) {
		c := ipg.NewClient(pgSrv.URL, mgr)
		c.CheckoutURL = pgSrv.URL
		c.MaxRetries = 0
		return c, nil
	}
	return f, out, errOut
}

func run(f *cmdutil.Factory, args ...string) error {
	cmd := NewCmdPG(f)
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

func TestAuth_Table(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(`{}`))
	require.NoError(t, run(f, "auth"))
	s := out.String()
	assert.Contains(t, s, "pg")
	assert.Contains(t, s, "sandbox")
	assert.Contains(t, s, "basic")
	assert.Contains(t, s, "SP-TEST-1")
	assert.NotContains(t, s, "spsk_test_SECRET", "secret must never print")
}

func TestAuth_JSONHasNoSecret(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(`{}`))
	f.Global.Output = "json"
	require.NoError(t, run(f, "auth"))
	s := out.String()
	assert.Contains(t, s, `"merchant_id": "SP-TEST-1"`)
	assert.NotContains(t, s, "spsk_test_SECRET")
	assert.NotContains(t, s, "secret_key")
	assert.NotContains(t, s, "access_token")
}

func TestAuth_RefreshReMints(t *testing.T) {
	var mints int
	broker := func(w http.ResponseWriter, _ *http.Request) {
		mints++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(brokerPGBasic))
	}
	f, _, _ := testFactory(t, broker, okJSON(`{}`))
	require.NoError(t, run(f, "auth"))              // mints once, caches
	require.NoError(t, run(f, "auth"))              // cache hit, no mint
	require.NoError(t, run(f, "auth", "--refresh")) // forces a mint
	assert.Equal(t, 2, mints)
}

func TestAuth_ProductionRejected(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(`{}`))
	f.Global.Env = "production"
	err := run(f, "auth")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestMerchantShow_Table(t *testing.T) {
	merchant := `{"payment_methods":[{"payment_method":"BANK_TRANSFER","active":1},{"payment_method":"CARD","active":0}],"payment_method_applicable_order_prices":{}}`
	f, out, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(merchant))
	require.NoError(t, run(f, "merchant", "show"))
	s := out.String()
	assert.Contains(t, s, "BANK_TRANSFER")
	assert.Contains(t, s, "yes")
	assert.Contains(t, s, "CARD")
	assert.Contains(t, s, "no")
}

func TestMerchantShow_JSON(t *testing.T) {
	merchant := `{"payment_methods":[{"payment_method":"CARD","active":"1"}]}`
	f, out, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(merchant))
	f.Global.Output = "json"
	require.NoError(t, run(f, "merchant", "get"))
	assert.Contains(t, out.String(), `"active": 1`)
}
