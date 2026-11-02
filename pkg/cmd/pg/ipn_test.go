package pg

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
	"github.com/sepayvn/sepay-cli/pkg/listen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Any HTTP response completes the delivery; only transport failures are retried.
func TestIpnListenSource_RetriesTransportFailuresOnly(t *testing.T) {
	src := ipnListenSource("")

	require.Nil(t, src.RetryNon2xx,
		"a non-2xx must not retry here: the platform treats any response as delivered")
	assert.True(t, src.FirstRetryImmediate,
		"PgIpnWorker queues with no last_retry_time, so retry 1 is due at once")
}

func TestIpnListenSource_OrderFilterDoesNotChangeRetry(t *testing.T) {
	src := ipnListenSource("INV-1")

	require.NotNil(t, src.Accept)
	assert.Nil(t, src.RetryNon2xx)
	assert.True(t, src.FirstRetryImmediate)
}

func TestIpnListenSource_MintsTheGatewayChannel(t *testing.T) {
	src := ipnListenSource("")
	assert.Equal(t, "/pg/ipn/listen", src.MintPath)
	assert.Equal(t, src.MintPath, src.RevokePath, "the key is revoked where it was minted")
	assert.Equal(t, "gateway IPN", src.Noun)
}

func TestOrderTag_NamesTheInvoiceNumber(t *testing.T) {
	ios, _, _, _ := iostreams.Test()

	full, err := listen.Decode([]byte(`{"payload":{"order":{"order_id":"ord_9","order_invoice_number":"DHSEPAY001"},` +
		`"transaction":{"id":"7c1e2b3a-4d5f-6a7b-8c9d-0e1f2a3b4c5d"}}}`))
	require.NoError(t, err)
	got := orderTag(ios, full)
	assert.Equal(t, "   order=DHSEPAY001", got, "the invoice number, not order_id")
	assert.NotContains(t, got, "7c1e2b3a", "the transaction uuid is not a handle any command accepts")

	legacy, err := listen.Decode([]byte(`{"payload":{"order":{"order_id":"ord_9"}}}`))
	require.NoError(t, err)
	assert.Equal(t, "   order=ord_9", orderTag(ios, legacy))
}

// apiTestFactory wires a Factory to a my.sepay.vn api/v2 server, which `pg ipn` talks to.
func apiTestFactory(t *testing.T, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table", Env: "sandbox"}}
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(srv.URL, "test")
		c.MaxRetries = 0
		return c, nil
	}
	return f, out, errOut
}

const pgIpnConfigured = `{"status":"success","data":{"configured":true,"ipn_url":"https://example.com/pg-ipn","auth_type":"SECRET_KEY","request_content_type":"application/json","secret_key":"****9999","active":true}}`

func TestPgIpnSet_SecretFromStdinReachesTheBody(t *testing.T) {
	var body []byte
	f, _, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(pgIpnConfigured))
			return
		}
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(pgIpnConfigured))
	})
	f.Global.Yes = true
	in, ok := f.IOStreams.In.(*bytes.Buffer)
	require.True(t, ok)
	in.WriteString("pg-piped-secret\n")

	require.NoError(t, run(f, "ipn", "set", "https://example.com/pg-ipn", "--secret", "-"))

	assert.Contains(t, string(body), `"secret_key":"pg-piped-secret"`)
	assert.NotContains(t, string(body), `"secret_key":"-"`)
	assert.Contains(t, string(body), `"auth_type":"SECRET_KEY"`, "a secret turns auth on, as the flag help says")
}

func TestPgIpnSet_RejectsMalformedURL(t *testing.T) {
	posted := false
	f, _, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			posted = true
		}
		_, _ = w.Write([]byte(pgIpnConfigured))
	})
	f.Global.Yes = true

	err := run(f, "ipn", "set", "example.com/pg-ipn") // no scheme, so no host either
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.False(t, posted, "a malformed URL must not reach the API")
}

func TestPgIpnSet_EmptyStdinSendsNothing(t *testing.T) {
	posted := false
	f, _, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			posted = true
		}
		_, _ = w.Write([]byte(pgIpnConfigured))
	})
	f.Global.Yes = true

	err := run(f, "ipn", "set", "https://example.com/pg-ipn", "--secret", "-")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.False(t, posted, "no write may be issued when the secret could not be read")
}

// The server answers a bad enum with a retryable 422.
func TestPgIpnSet_RejectsUnknownEnumsBeforeAnyRequest(t *testing.T) {
	for _, tt := range []struct {
		flag, value, want string
	}{
		{"--auth-type", "BOGUS", "--auth-type must be one of: SECRET_KEY"},
		{"--content-type", "text/plain", "--content-type must be one of: application/json"},
	} {
		t.Run(tt.flag, func(t *testing.T) {
			called := false
			f, _, _ := apiTestFactory(t, func(w http.ResponseWriter, _ *http.Request) {
				called = true
				_, _ = w.Write([]byte(pgIpnConfigured))
			})
			f.Global.Yes = true

			err := run(f, "ipn", "set", "https://example.com/pg-ipn", tt.flag, tt.value)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.False(t, called, "a bad enum must not reach the API at all")
		})
	}
}

func TestPgIpnSet_EmptyAuthTypeStillTurnsAuthOff(t *testing.T) {
	var body []byte
	f, _, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(pgIpnConfigured))
			return
		}
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(pgIpnConfigured))
	})
	f.Global.Yes = true

	require.NoError(t, run(f, "ipn", "set", "https://example.com/pg-ipn", "--auth-type", ""))
	assert.Contains(t, string(body), `"auth_type":""`)
}

func TestPgIpnSet_MasksASecretEchoedRawByThePost(t *testing.T) {
	f, out, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(pgIpnConfigured))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"configured":true,"ipn_url":"https://example.com/pg-ipn","auth_type":"SECRET_KEY","request_content_type":"application/json","secret_key":"SUPERSECRETRAWKEYcdef","active":true}}`))
	})
	f.Global.Yes = true
	f.Global.Output = "json"

	require.NoError(t, run(f, "ipn", "set", "https://example.com/pg-ipn"))
	assert.NotContains(t, out.String(), "SUPERSECRETRAWKEYcdef")
	assert.Contains(t, out.String(), `"secret_key": "****cdef"`)
}
