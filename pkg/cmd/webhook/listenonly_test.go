package webhook

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const listenOnlyWebhookList = `{"status":"success","data":[{"id":"wh_2","name":"Local","event_type":"All","authen_type":"HMAC_SHA256","webhook_url":"","delivery_mode":"listen","is_verify_payment":false,"skip_if_no_code":false,"only_va":false,"bank_mode":"all","bank_account_xid":null,"bank_sub_account_xids":[],"prefix_filters":[],"alert_threshold":null,"active":true,"created_at":"2026-08-03 10:00:00"}],"meta":{"pagination":{"total":1,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`

func TestCreate_ListenOnlySendsModeAndNoURL(t *testing.T) {
	var body []byte
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_2","name":"Local","event_type":"All","authen_type":"No_Authen","webhook_url":"","delivery_mode":"listen","is_verify_payment":false,"skip_if_no_code":false,"only_va":false,"bank_mode":"all","bank_account_xid":null,"bank_sub_account_xids":[],"prefix_filters":[],"alert_threshold":null,"active":true,"created_at":""}}`))
	})

	require.NoError(t, run(NewCmdWebhook(f), "create", "--listen-only", "--bank-mode", "all", "--name", "Local"))

	var sent map[string]any
	require.NoError(t, json.Unmarshal(body, &sent))
	assert.Equal(t, "listen", sent["delivery_mode"])
	assert.NotContains(t, sent, "url", "a listen-only webhook must not carry a destination")
}

func TestCreate_ListenOnlyWithURLIsAUsageError(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("the API must not be called for a usage error")
	})

	err := run(NewCmdWebhook(f), "create", "--listen-only", "--url", "https://x.test/h", "--bank-mode", "all")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

// The picker can also turn listen-only on; this pins the flag path.
func TestCreate_ListenOnlyWithOAuth2IsAUsageError(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("the API must not be called for a usage error")
	})

	err := run(NewCmdWebhook(f), "create", "--listen-only", "--auth-type", "oauth2", "--bank-mode", "all")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestCreate_ListenOnlyRefusesProductionEnv(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("the API must not be called when the env is refused")
	})
	f.Global.Env = config.EnvProduction

	err := run(NewCmdWebhook(f), "create", "--listen-only", "--bank-mode", "all")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox-only")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestUpdate_ListenOnlyRefusesProductionEnv(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("the API must not be called when the env is refused")
	})
	f.Global.Env = config.EnvProduction

	err := run(NewCmdWebhook(f), "update", "wh_1", "--listen-only")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox-only")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestUpdate_URLSendsHTTPModeInSandbox(t *testing.T) {
	var body []byte
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			body, _ = io.ReadAll(r.Body)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_2","name":"","event_type":"All","authen_type":"No_Authen","webhook_url":"https://x.test/h","delivery_mode":"http","is_verify_payment":false,"skip_if_no_code":false,"only_va":false,"bank_mode":"all","bank_account_xid":null,"bank_sub_account_xids":[],"prefix_filters":[],"alert_threshold":null,"active":true,"created_at":""}}`))
	})

	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_2", "--url", "https://x.test/h"))

	var sent map[string]any
	require.NoError(t, json.Unmarshal(body, &sent))
	assert.Equal(t, "https://x.test/h", sent["url"])
	assert.Equal(t, "http", sent["delivery_mode"])
}

func TestUpdate_URLOmitsModeInProduction(t *testing.T) {
	var body []byte
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			body, _ = io.ReadAll(r.Body)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_2","name":"","event_type":"All","authen_type":"No_Authen","webhook_url":"https://x.test/h","is_verify_payment":false,"skip_if_no_code":false,"only_va":false,"bank_mode":"all","bank_account_xid":null,"bank_sub_account_xids":[],"prefix_filters":[],"alert_threshold":null,"active":true,"created_at":""}}`))
	})
	f.Global.Env = config.EnvProduction
	f.Global.Yes = true

	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_2", "--url", "https://x.test/h"))

	var sent map[string]any
	require.NoError(t, json.Unmarshal(body, &sent))
	assert.NotContains(t, sent, "delivery_mode")
}

func TestList_ListenOnlyLabelsTheURLColumn(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(listenOnlyWebhookList))
	})
	require.NoError(t, run(NewCmdWebhook(f), "list"))
	assert.Contains(t, out.String(), "(listen-only)")
}

func TestDeliverable_ListenOnlyCoversTheAccount(t *testing.T) {
	hooks := []Webhook{{
		ID: "wh_2", Active: true, BankMode: "all", EventType: "All",
		DeliveryMode: deliveryModeListen,
	}}
	ok, why := deliverable(hooks, delivery{account: "ba_1", event: eventPaymentIn, hasCode: true})
	assert.True(t, ok, why)
}
