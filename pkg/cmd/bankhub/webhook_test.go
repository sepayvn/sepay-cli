package bankhub

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const webhookConfig = `{"data":{"webhook_url":"https://example.com/hook","auth_type":"SECRET_KEY","secret_key":"***1234","request_content_type":"application/json","active":1,"allow_events":["*"]}}`

func TestWebhookList_ShowsConfig(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(webhookConfig))
	require.NoError(t, run(f, "webhook", "list"))
	s := out.String()
	assert.Contains(t, s, "https://example.com/hook")
	assert.Contains(t, s, "SECRET_KEY")
	assert.Contains(t, s, "***1234") // server-masked secret, safe to show
	assert.Contains(t, s, "yes")     // active
}

func TestWebhook_EventsLineSaysNoneWhenNothingIsDelivered(t *testing.T) {
	for _, c := range []struct{ events, want string }{
		{`null`, "Events:   none\n"},
		{`[]`, "Events:   none\n"},
		{`["*"]`, "Events:   *\n"},
		{`["LINK_TOKEN_CREATED","LINK_TOKEN_REVOKED"]`, "Events:   LINK_TOKEN_CREATED, LINK_TOKEN_REVOKED\n"},
	} {
		resp := `{"data":{"webhook_url":"https://example.com/hook","auth_type":null,"secret_key":null,"request_content_type":"application/json","active":1,"allow_events":` + c.events + `}}`
		t.Run("list "+c.events, func(t *testing.T) {
			f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(resp))
			require.NoError(t, run(f, "webhook", "list"))
			assert.Contains(t, out.String(), c.want)
		})
		t.Run("set "+c.events, func(t *testing.T) {
			f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(resp))
			f.Global.Yes = true
			require.NoError(t, run(f, "webhook", "set", "https://example.com/hook"))
			assert.Contains(t, out.String(), c.want)
		})
	}
}

func TestWebhookList_NotConfigured(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{"data":null}`))
	require.NoError(t, run(f, "webhook", "list"))
	assert.Contains(t, out.String(), "No webhook configured.")
}

func TestWebhookList_JSONNullWhenUnconfigured(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{"data":null}`))
	f.Global.Output = "json"
	require.NoError(t, run(f, "webhook", "list"))
	assert.Contains(t, out.String(), "null")
}

func TestWebhookSet_PostsBodyAndMasksRawSecret(t *testing.T) {
	var body []byte
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		// POST echoes secret_key raw, unlike the masked GET.
		_, _ = w.Write([]byte(`{"data":{"webhook_url":"https://example.com/hook","auth_type":"SECRET_KEY","secret_key":"supersecretRAW9999","request_content_type":"application/json","active":1,"allow_events":["*"]}}`))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "webhook", "set", "https://example.com/hook", "--events", "*", "--secret", "supersecretRAW9999"))
	assert.Contains(t, string(body), `"webhook_url":"https://example.com/hook"`)
	assert.Contains(t, string(body), `"secret_key":"supersecretRAW9999"`)
	// Bank Hub requires allow_events as a JSON-encoded string.
	assert.Contains(t, string(body), `"allow_events":"[\"*\"]"`)
	assert.NotContains(t, out.String(), "supersecretRAW9999")
	assert.Contains(t, out.String(), "webhook set")
}

func TestWebhookSet_RejectsMalformedURL(t *testing.T) {
	for _, dest := range []string{"test", "example.com/hook", "ftp://example.com/hook"} {
		t.Run(dest, func(t *testing.T) {
			f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not reach Bank Hub with a malformed URL")
			})
			f.Global.Yes = true
			err := run(f, "webhook", "set", dest)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

func TestWebhookSet_JSONHasNoRawSecret(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"webhook_url":"https://example.com/hook","auth_type":"SECRET_KEY","secret_key":"supersecretRAW9999","request_content_type":"application/json","active":1,"allow_events":["*"]}}`))
	})
	f.Global.Yes = true
	f.Global.Output = "json"
	require.NoError(t, run(f, "webhook", "set", "https://example.com/hook"))
	assert.NotContains(t, out.String(), "supersecretRAW9999")
	assert.Contains(t, out.String(), "****9999") // masked hint only
}

func TestWebhookSet_SecretFromStdinReachesTheBody(t *testing.T) {
	var body []byte
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"data":{"webhook_url":"https://example.com/hook","auth_type":"SECRET_KEY","secret_key":"x","request_content_type":"application/json","active":1}}`))
	})
	f.Global.Yes = true
	in, ok := f.IOStreams.In.(*bytes.Buffer)
	require.True(t, ok)
	in.WriteString("piped-secret-9999\n")

	require.NoError(t, run(f, "webhook", "set", "https://example.com/hook", "--secret", "-"))
	assert.Contains(t, string(body), `"secret_key":"piped-secret-9999"`)
	assert.Contains(t, string(body), `"auth_type":"SECRET_KEY"`)
	assert.NotContains(t, string(body), `"secret_key":"-"`)
}

func TestWebhookSet_SecretFromEmptyStdinIsAUsageError(t *testing.T) {
	reached := false
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	f.Global.Yes = true

	err := run(f, "webhook", "set", "https://example.com/hook", "--secret", "-")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.False(t, reached, "no request may be sent when the secret could not be read")
}
