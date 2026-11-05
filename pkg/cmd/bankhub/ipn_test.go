package bankhub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/listen"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// apiTestFactory wires a Factory to a my.sepay.vn api/v2 server, which `bankhub ipn` talks to.
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

const ipnConfigured = `{"status":"success","data":{"configured":true,"ipn_url":"https://example.com/bh-ipn","api_key":"****9999"}}`

func TestIPNGet_ShowsConfig(t *testing.T) {
	f, out, _ := apiTestFactory(t, okJSON(ipnConfigured))
	require.NoError(t, run(f, "ipn", "get"))
	s := out.String()
	assert.Contains(t, s, "https://example.com/bh-ipn")
	assert.Contains(t, s, "****9999") // server-masked hint, safe to show
}

func TestIPNGet_NotConfigured(t *testing.T) {
	f, out, _ := apiTestFactory(t, okJSON(`{"status":"success","data":{"configured":false,"ipn_url":null,"api_key":null}}`))
	require.NoError(t, run(f, "ipn", "get"))
	assert.Contains(t, out.String(), "No IPN configured")
}

func TestIPNSet_OmitsApiKeyWhenFlagAbsent(t *testing.T) {
	var body []byte
	f, _, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(ipnConfigured))
			return
		}
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(ipnConfigured))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "ipn", "set", "https://example.com/bh-ipn"))

	// Absent means "leave as it is": setting a URL must not strip the stored key.
	var sent map[string]any
	require.NoError(t, json.Unmarshal(body, &sent))
	assert.Equal(t, "https://example.com/bh-ipn", sent["ipn_url"])
	_, hasKey := sent["api_key"]
	assert.False(t, hasKey, "api_key must not be sent when --api-key was not passed")
}

func TestIPNSet_SendsApiKeyAndNeverPrintsIt(t *testing.T) {
	var body []byte
	f, out, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(ipnConfigured))
			return
		}
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"status":"success","data":{"configured":true,"ipn_url":"https://example.com/bh-ipn","api_key":"****RAW9","accounts_updated":3,"accounts_total":3}}`))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "ipn", "set", "https://example.com/bh-ipn", "--api-key", "keyRAW9"))

	assert.Contains(t, string(body), `"api_key":"keyRAW9"`)
	assert.NotContains(t, out.String(), "keyRAW9", "the plaintext key must never print")
	assert.Contains(t, out.String(), "3 of 3 updated")
}

func TestIPNSet_WarnsOnPartialFanOut(t *testing.T) {
	f, out, errOut := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(ipnConfigured))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"configured":true,"ipn_url":"https://example.com/bh-ipn","api_key":"****9999","accounts_updated":2,"accounts_total":5}}`))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "ipn", "set", "https://example.com/bh-ipn"))

	assert.Contains(t, out.String(), "2 of 5 updated")
	assert.Contains(t, errOut.String(), "3 linked account(s) still deliver to the previous endpoint")
}

func TestIPNSet_KeepsStoredKeyWhenTheReadFails(t *testing.T) {
	var body []byte
	f, _, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"status":"error","message":"boom"}`))
			return
		}
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(ipnConfigured))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "ipn", "set", "https://example.com/bh-ipn"))

	// A failed read must not be mistaken for "no key stored".
	var sent map[string]any
	require.NoError(t, json.Unmarshal(body, &sent))
	_, hasKey := sent["api_key"]
	assert.False(t, hasKey)
}

func TestIPNSet_EmptyApiKeyTurnsAuthOff(t *testing.T) {
	var body []byte
	f, _, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(ipnConfigured))
			return
		}
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(ipnConfigured))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "ipn", "set", "https://example.com/bh-ipn", "--api-key", ""))
	assert.Contains(t, string(body), `"api_key":""`)
}

func TestIPNListen_RejectsUnknownEvent(t *testing.T) {
	f, _, _ := apiTestFactory(t, okJSON(ipnConfigured))
	err := run(f, "ipn", "listen", "--forward", "localhost:3000", "--events", "payment_in")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "transaction_credit")
}

func TestIPN_ProductionRejected(t *testing.T) {
	f, _, _ := apiTestFactory(t, okJSON(ipnConfigured))
	f.Global.Env = "production"
	err := run(f, "ipn", "get")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestBankAccountFilter_ReadsPayload(t *testing.T) {
	frame := listen.Frame{Payload: json.RawMessage(`{"bank_account_xid":"ba-1","amount":50000}`)}
	assert.Equal(t, "ba-1", bankAccountXID(frame))

	accept := acceptBankAccount("ba-1")
	assert.True(t, accept(frame))
	assert.False(t, accept(listen.Frame{Payload: json.RawMessage(`{"bank_account_xid":"ba-2"}`)}))
	assert.Nil(t, acceptBankAccount(""))
}

func TestBankAccountTag_EmptyWithoutXid(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	assert.Empty(t, bankAccountTag(ios, listen.Frame{Payload: json.RawMessage(`{}`)}))
	assert.Contains(t, bankAccountTag(ios, listen.Frame{Payload: json.RawMessage(`{"bank_account_xid":"ba-1"}`)}), "ba=ba-1")
}

func TestIPNListenSource_RetryPolicy(t *testing.T) {
	src := ipnListenSource("")

	require.Nil(t, src.RetryNon2xx,
		"nothing this company can read would enable a non-2xx retry here")
	assert.False(t, src.FirstRetryImmediate,
		"the webhook pipeline queues with last_retry_time set, so retry 1 waits a minute")
	assert.Equal(t, "/bankhub/ipn/listen", src.MintPath)
	assert.Equal(t, "Bank Hub IPN", src.Noun)
}

func TestBankAccountTag_NamesLookupKeys(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	fr, err := listen.Decode([]byte(`{"payload":{"id":92704,"transaction_id":"FT26012345678","bank_account_xid":"ba-1"}}`))
	require.NoError(t, err)

	got := bankAccountTag(ios, fr)
	assert.Contains(t, got, "txn=FT26012345678", "the id bankhub transaction get takes")
	assert.NotContains(t, got, "92704", "not the internal numeric id")
	assert.Contains(t, got, "ba=ba-1", "the xid must not be trimmed at its dash")
}

func TestIPNSet_EmptyAPIKeyStillTurnsAuthOff(t *testing.T) {
	var body []byte
	f, _, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(ipnConfigured))
			return
		}
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"status":"success","data":{"configured":true,"ipn_url":"https://example.com/bh-ipn","api_key":null}}`))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "ipn", "set", "https://example.com/bh-ipn", "--api-key", ""))

	assert.Contains(t, string(body), `"api_key":""`)
}

func TestIPNSet_APIKeyFromStdinReachesTheBody(t *testing.T) {
	var body []byte
	f, _, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(ipnConfigured))
			return
		}
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"status":"success","data":{"configured":true,"ipn_url":"https://example.com/bh-ipn","api_key":"****dKey"}}`))
	})
	f.Global.Yes = true
	in, ok := f.IOStreams.In.(*bytes.Buffer)
	require.True(t, ok)
	in.WriteString("pipedKey\n")

	require.NoError(t, run(f, "ipn", "set", "https://example.com/bh-ipn", "--api-key", "-"))

	assert.Contains(t, string(body), `"api_key":"pipedKey"`)
	assert.NotContains(t, string(body), `"api_key":"-"`)
}

func TestIPNSet_MasksAKeyEchoedRawByThePost(t *testing.T) {
	f, out, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(ipnConfigured))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"configured":true,"ipn_url":"https://example.com/bh-ipn","api_key":"SUPERSECRETRAWKEYcdef"}}`))
	})
	f.Global.Yes = true
	f.Global.Output = "json"

	require.NoError(t, run(f, "ipn", "set", "https://example.com/bh-ipn", "--api-key", "SUPERSECRETRAWKEYcdef"))
	assert.NotContains(t, out.String(), "SUPERSECRETRAWKEYcdef")
	assert.Contains(t, out.String(), `"api_key": "****cdef"`)
}

// On a terminal an unread config offers no auth choice: "keep" could not be offered, and turning
// auth off by mistake is what the choice would risk.
func TestIPNSet_TerminalAsksNoKeyWhenTheReadFails(t *testing.T) {
	var body []byte
	f, _, _ := apiTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"status":"error","message":"upstream"}`))
			return
		}
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(ipnConfigured))
	})
	f.Global.Yes = true
	prompttest.Use(t, f)
	require.NoError(t, run(f, "ipn", "set", "https://example.com/bh-ipn"))

	var sent map[string]any
	require.NoError(t, json.Unmarshal(body, &sent))
	assert.NotContains(t, sent, "api_key")
	assert.NotContains(t, sent, "secret_key")
	assert.Contains(t, f.IOStreams.ErrOut.(*bytes.Buffer).String(), "could not read the current config")
}
