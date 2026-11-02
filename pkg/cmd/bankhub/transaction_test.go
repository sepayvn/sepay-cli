package bankhub

import (
	"net/http"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const transactionList = `{"data":[{"transaction_id":"TX1","transaction_date":"2026-07-23 10:00:00","bank_account_xid":"ba_1","company_xid":"co_1","brand_name":"ACB","account_number":"9990001111","va":"9990001111001","va_xid":"va_1","reference_number":"FT123","transfer_type":"credit","amount":"100000.00","transaction_content":"test payment","payment_code":null,"webhook_success":1}],"meta":{"total":1,"per_page":20,"current_page":1,"page_count":1,"has_more":false}}`

const transactionDetail = `{"data":{"transaction_id":"TX1","transaction_date":"2026-07-23 10:00:00","bank_account_xid":"ba_1","company_xid":"co_1","brand_name":"ACB","account_number":"9990001111","va":"9990001111001","va_xid":"va_1","reference_number":"FT123","transfer_type":"credit","amount":"100000.00","transaction_content":"test payment","payment_code":null,"webhook_success":1}}`

func TestTransactionList_Table(t *testing.T) {
	var gotQuery string
	f, out, errOut := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(transactionList))
	})
	require.NoError(t, run(f, "transaction", "list", "--type", "credit", "--bank-account", "ba_1"))
	s := out.String()
	assert.Contains(t, s, "TX1")
	assert.Contains(t, s, "credit")
	assert.Contains(t, s, "100.000") // decimal amount → VND
	assert.Contains(t, s, "test payment")
	assert.Contains(t, errOut.String(), "1 of 1")
	assert.Contains(t, gotQuery, "transfer_type=credit")
	assert.Contains(t, gotQuery, "bank_account_xid=ba_1")
}

func TestTransactionList_JSONEmptyIsArray(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{"data":[],"meta":{"total":0}}`))
	f.Global.Output = "json"
	require.NoError(t, run(f, "transaction", "list"))
	assert.Contains(t, out.String(), "[]")
}

func TestTransactionGet_ShowsDetail(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(transactionDetail))
	require.NoError(t, run(f, "transaction", "get", "TX1"))
	s := out.String()
	assert.Contains(t, s, "TX1")
	assert.Contains(t, s, "100.000")
	assert.Contains(t, s, "9990001111")
	assert.Contains(t, s, "ACB")
}

func TestTransactionGet_SanitizesTerminalEscapes(t *testing.T) {
	body := `{"data":{"transaction_id":"TX2","transaction_date":"2026-07-23 10:00:00","bank_account_xid":"ba_1","company_xid":"co_1","brand_name":"ACB","account_number":"9990001111","va":null,"va_xid":null,"reference_number":"R1","transfer_type":"credit","amount":"1000.00","transaction_content":"\u001b]0;PWNED\u0007\u001b[2Jhello","payment_code":null,"webhook_success":1}}`
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(body))
	require.NoError(t, run(f, "transaction", "get", "TX2"))
	s := out.String()
	assert.Contains(t, s, "hello")      // safe text survives
	assert.NotContains(t, s, "\x1b]0;") // OSC title-set stripped
	assert.NotContains(t, s, "\x1b[2J") // clear-screen stripped
}

func TestTransactionGet_NotFoundMapsToError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":404,"message":"Giao dịch không tồn tại hoặc không thuộc merchant này"}`))
	})
	err := run(f, "transaction", "get", "NOPE")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
}

// An empty id would route to the list endpoint.
func TestTransactionGet_EmptyIDIsAUsageError(t *testing.T) {
	var called bool
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(transactionList))
	})

	err := run(f, "transaction", "get", "")
	require.Error(t, err)
	assert.EqualError(t, err, "a transaction id is required")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.False(t, called, "no request may leave the machine")
}
