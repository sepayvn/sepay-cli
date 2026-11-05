package webhook

import (
	"net/http"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/golden"

	"github.com/stretchr/testify/require"
)

const goldenWebhook = `{"id":"wh_1","name":"Shop","event_type":"All","authen_type":"Api_Key","webhook_url":"https://x.test/hook",` +
	`"is_verify_payment":true,"skip_if_no_code":true,"only_va":false,"bank_mode":"multi","bank_account_xid":null,` +
	`"bank_account_xids":["ba_1","ba_2"],"bank_config":{"ba_1":{"receive_primary":true,"va_mode":"list"},"ba_2":{"receive_primary":false,"va_mode":"all"}},` +
	`"bank_sub_account_xids":["va_1"],"prefix_filters":["DH"],"alert_threshold":5,"active":true,"created_at":"2026-07-15 10:00:00",` +
	`"api_key":"secret123","request_content_type":"Json"}`

func TestList_JSON_Golden(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":[` + goldenWebhook + `],"meta":{"pagination":{"total":1,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdWebhook(f), "list"))
	golden.Assert(t, "list_json", out.Bytes())
}

func TestGet_JSON_Golden(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":` + goldenWebhook + `}`))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdWebhook(f), "get", "wh_1"))
	golden.Assert(t, "get_json", out.Bytes())
}
