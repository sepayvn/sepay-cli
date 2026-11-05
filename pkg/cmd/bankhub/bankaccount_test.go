package bankhub

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const bankAccountList = `{"data":[{"xid":"ba_1","company_xid":"co_1","brand_name":"ACB","account_holder_name":"NGUYEN VAN A","account_number":"123456","accumulated":5000000,"account_type":"enterprise","bank_api_connected":1,"active":1,"last_transaction":null,"created_at":"2026-01-02 10:00:00","updated_at":"2026-01-02 10:00:00"}],"meta":{"total":1,"per_page":20,"current_page":1,"page_count":1,"has_more":false}}`

const bankAccountDetail = `{"data":{"xid":"ba_1","company_xid":"co_1","brand_name":"ACB","account_holder_name":"NGUYEN VAN A","account_number":"123456","accumulated":"5000000.00","account_type":"enterprise","bank_api_connected":1,"active":1,"last_transaction":null,"created_at":"2026-01-02 10:00:00","updated_at":"2026-01-02 10:00:00","va_list":[{"xid":"va_1","sub_account":"123456001","sub_holder_name":"SHOP A","active":1,"created_at":"2026-01-03 10:00:00","updated_at":"2026-01-03 10:00:00"}]}}`

func TestBankAccountList_Table(t *testing.T) {
	var gotQuery string
	f, out, errOut := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(bankAccountList))
	})
	require.NoError(t, run(f, "bank-account", "list", "--company", "co_1", "--per-page", "20"))
	s := out.String()
	assert.Contains(t, s, "ba_1")
	assert.Contains(t, s, "ACB")
	assert.Contains(t, s, "123456")
	assert.Contains(t, errOut.String(), "1 of 1")
	assert.Contains(t, gotQuery, "company_xid=co_1")
	assert.Contains(t, gotQuery, "per_page=20")
}

func TestBankAccountList_JSONEmptyIsArray(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(`{"data":[],"meta":{"total":0}}`))
	f.Global.Output = "json"
	require.NoError(t, run(f, "bank-account", "list"))
	assert.Contains(t, out.String(), "[]")
}

func TestBankAccountGet_ShowsVAList(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(bankAccountDetail))
	require.NoError(t, run(f, "bank-account", "get", "ba_1"))
	s := out.String()
	assert.Contains(t, s, "ba_1")
	assert.Contains(t, s, "5.000.000") // accumulated formatted as VND
	assert.Contains(t, s, "va_1")
	assert.Contains(t, s, "123456001")
}

func TestBankAccountGet_JSONHasVAList(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(bankAccountDetail))
	f.Global.Output = "json"
	require.NoError(t, run(f, "bank-account", "get", "ba_1"))
	assert.Contains(t, out.String(), `"va_list"`)
	assert.Contains(t, out.String(), `"va_1"`)
}

func TestBankAccountGet_NotFoundMapsToError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":404,"message":"Tài khoản ngân hàng không tồn tại hoặc không thuộc merchant này"}`))
	})
	err := run(f, "bank-account", "get", "NOPE")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
}

// Bank Hub sends money as decimal strings; a real fraction must be rejected, not rounded.
func TestBankAccount_MoneyDecodingRejectsFractions(t *testing.T) {
	var a bankAccount
	require.NoError(t, json.Unmarshal([]byte(`{"xid":"ba_1","accumulated":"5000000.00"}`), &a))
	assert.EqualValues(t, 5000000, int64(a.Accumulated))

	require.NoError(t, json.Unmarshal([]byte(`{"xid":"ba_1","accumulated":5000000}`), &a))
	assert.EqualValues(t, 5000000, int64(a.Accumulated))

	for _, bad := range []string{`"5000000.50"`, `"Inf"`, `"NaN"`} {
		err := json.Unmarshal([]byte(`{"xid":"ba_1","accumulated":`+bad+`}`), &a)
		assert.Error(t, err, "%s must be rejected, not silently narrowed", bad)
	}
}

func TestBankAccountList_BankIsAnID(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not list with a bank code as the id")
	})
	err := run(f, "bank-account", "list", "--bank", "ACB")
	require.EqualError(t, err, `--bank takes a Bank Hub bank id (a number), got "ACB"; it is not a bank code`)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}
