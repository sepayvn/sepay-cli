package bankhub

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	ibh "github.com/sepayvn/sepay-cli/internal/bankhub"
	"github.com/sepayvn/sepay-cli/internal/product"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

const brokerBankHubBearer = `{"status":"success","data":{"auth_type":"bearer","access_token":"bhtok_SECRET","token_type":"Bearer","expires_at":"2999-01-01T00:00:00Z"}}`

// testFactory wires a fake Factory to a broker server and a Bank Hub host server.
func testFactory(t *testing.T, brokerH, bhH http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	keyring.MockInit()
	brokerSrv := httptest.NewServer(brokerH)
	t.Cleanup(brokerSrv.Close)
	bhSrv := httptest.NewServer(bhH)
	t.Cleanup(bhSrv.Close)

	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table", Env: "sandbox"}}

	mgr := product.NewManager(product.NewBroker(api.NewClient(brokerSrv.URL, "tok")), product.NewStore(t.TempDir()), "default", "sandbox")
	f.ProductManager = func() (*product.Manager, error) { return mgr, nil }
	f.NewBankHubClient = func() (*ibh.Client, error) {
		c := ibh.NewClient(bhSrv.URL, mgr)
		c.MaxRetries = 0
		return c, nil
	}
	return f, out, errOut
}

func run(f *cmdutil.Factory, args ...string) error {
	cmd := NewCmdBankHub(f)
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
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{}`))
	require.NoError(t, run(f, "auth"))
	s := out.String()
	assert.Contains(t, s, "bankhub")
	assert.Contains(t, s, "sandbox")
	assert.Contains(t, s, "bearer")
	assert.NotContains(t, s, "bhtok_SECRET", "token must never print")
}

func TestAuth_JSONHasNoSecret(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{}`))
	f.Global.Output = "json"
	require.NoError(t, run(f, "auth"))
	s := out.String()
	assert.Contains(t, s, `"auth_type": "bearer"`)
	assert.NotContains(t, s, "bhtok_SECRET")
	assert.NotContains(t, s, "access_token")
}

func TestAuth_RefreshReMints(t *testing.T) {
	var mints int
	broker := func(w http.ResponseWriter, _ *http.Request) {
		mints++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(brokerBankHubBearer))
	}
	f, _, _ := testFactory(t, broker, okJSON(`{}`))
	require.NoError(t, run(f, "auth"))              // mints once, caches
	require.NoError(t, run(f, "auth"))              // cache hit, no mint
	require.NoError(t, run(f, "auth", "--refresh")) // forces a mint
	assert.Equal(t, 2, mints)
}

func TestAuth_ProductionRejected(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{}`))
	f.Global.Env = "production"
	err := run(f, "auth")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}
