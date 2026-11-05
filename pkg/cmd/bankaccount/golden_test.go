package bankaccount

import (
	"net/http"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/golden"

	"github.com/stretchr/testify/require"
)

const goldenAccounts = `{"status":"success","data":[` +
	`{"id":"ba_1","account_holder_name":"NGUYEN VAN A","account_number":"0000000001","accumulated":1500000,"last_transaction":"2026-07-15 10:00:00","label":"Shop","account_type":"individual","active":1,"created_at":"2026-07-01 09:00:00","bank_short_name":"MSB","bank_full_name":"MSB","bank_bin":"970426","bank_code":"MSB","connection_type":"api","official_va_supported":true,"va_only":true},` +
	`{"id":"ba_2","account_holder_name":"CONG TY B","account_number":"0000000002","accumulated":0,"last_transaction":null,"label":"","account_type":"business","active":1,"created_at":"2026-07-02 09:00:00","bank_short_name":"TPBank","bank_full_name":"TPBank","bank_bin":"970423","bank_code":"TPB","connection_type":"sms","official_va_supported":false,"va_only":false}]}`

func TestList_JSON_Golden(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(goldenAccounts)) })
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdBankAccount(f), "list"))
	golden.Assert(t, "list_json", out.Bytes())
}
