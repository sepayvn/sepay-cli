package pg

import (
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const orderList = `{"data":[{"id":"u1","customer_id":"2427","order_id":"SEPAY-1","order_invoice_number":"DH1","order_status":"CAPTURED","order_amount":"300000.00","order_currency":"VND","created_at":"2025-09-05 13:31:42","updated_at":"2025-09-05 13:31:42"}],"meta":{"per_page":20,"total":1,"has_more":false,"current_page":1,"page_count":1}}`

const orderDetail = `{"data":{"id":"u1","customer_id":null,"order_id":"SEPAY-1","order_invoice_number":"DH1","order_status":"CAPTURED","order_amount":"300000.00","order_currency":"VND","authentication_status":"AUTHENTICATION_SUCCESSFUL","created_at":"2025-08-28 15:43:48","updated_at":"2025-08-28 15:43:48","transactions":[{"id":"t1","payment_method":"CARD","transaction_type":"PAYMENT","transaction_amount":"300000","transaction_currency":"VND","transaction_status":"APPROVED","card_number":"512345xxxxxx0008","card_brand":"MASTERCARD","transaction_date":"2025-08-28 15:43:41"}]}}`

func TestOrderList_Table(t *testing.T) {
	var gotQuery string
	f, out, errOut := testFactory(t, okJSON(brokerPGBasic), func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(orderList))
	})
	require.NoError(t, run(f, "order", "list", "--status", "CAPTURED", "--from", "2025-09-01"))
	s := out.String()
	assert.Contains(t, s, "DH1")
	assert.Contains(t, s, "CAPTURED")
	assert.Contains(t, s, "300000.00")
	assert.Contains(t, errOut.String(), "1 of 1")
	assert.Contains(t, gotQuery, "order_status=CAPTURED")
	assert.Contains(t, gotQuery, "from_created_at=2025-09-01")
}

func TestOrderList_JSONEmptyIsArray(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(`{"data":[],"meta":{"total":0}}`))
	f.Global.Output = "json"
	require.NoError(t, run(f, "order", "list"))
	assert.Contains(t, out.String(), "[]")
}

func TestOrderGet_ShowsTransactions(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(orderDetail))
	require.NoError(t, run(f, "order", "get", "DH1"))
	s := out.String()
	assert.Contains(t, s, "DH1")
	assert.Contains(t, s, "CARD")
	assert.Contains(t, s, "512345xxxxxx0008")
	assert.Contains(t, s, "APPROVED")
}

func TestOrderGet_JSONIncludesTransactions(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(orderDetail))
	f.Global.Output = "json"
	require.NoError(t, run(f, "order", "get", "DH1"))
	assert.Contains(t, out.String(), `"transactions"`)
	assert.Contains(t, out.String(), `"MASTERCARD"`)
}

func TestOrderGet_NotFoundMapsToError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerPGBasic), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"error":404,"messages":{"error":"Không tìm thấy đơn hàng"}}`))
	})
	err := run(f, "order", "get", "NOPE")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
}

func TestOrderGet_NoArgNonInteractiveIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(`{"data":[]}`))
	err := run(f, "order", "get")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestOrderCancel_ConfirmGuard(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(`{"message":"ok"}`))
	err := run(f, "order", "cancel", "DH1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestOrderCancel_NoArgNonInteractiveIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(`{"message":"ok"}`))
	f.Global.Yes = true // even with --yes, a non-TTY has no invoice to pick
	err := run(f, "order", "cancel")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestOrderCancel_WithYes(t *testing.T) {
	var hit bool
	var body []byte
	f, out, _ := testFactory(t, okJSON(brokerPGBasic), func(w http.ResponseWriter, r *http.Request) {
		hit = true
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"message":"Đã hủy đơn hàng thành công"}`))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "order", "cancel", "DH1"))
	assert.True(t, hit)
	assert.Contains(t, string(body), `"order_invoice_number":"DH1"`)
	assert.Contains(t, out.String(), "Order cancelled.")
	assert.NotContains(t, out.String(), "Đã hủy")
}

func TestOrderCancel_BusinessErrorExit(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerPGBasic), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":4001,"message":"Đơn hàng đã bị hủy trước đó"}`))
	})
	f.Global.Yes = true
	err := run(f, "order", "cancel", "DH1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "đã bị hủy")
}

func TestOrderVoid_ConfirmGuard(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(`{"message":"ok"}`))
	err := run(f, "order", "void", "DH1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestOrderVoid_PartialSuccess207(t *testing.T) {
	f, out, errOut := testFactory(t, okJSON(brokerPGBasic), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMultiStatus) // 207
		_, _ = w.Write([]byte(`{"code":2070,"message":"Void OK; cancel the agreement","agreement_id":"AG9"}`))
	})
	f.Global.Yes = true
	require.NoError(t, run(f, "order", "void", "DH1"))
	assert.Contains(t, out.String(), "Transaction voided.")
	assert.Contains(t, errOut.String(), "AG9")
}

func TestOrderList_StatusValidation(t *testing.T) {
	cases := []struct {
		name  string
		value string
		query string
		ok    bool
	}{
		{"common value", "CAPTURED", "CAPTURED", true},
		{"less common but accepted", "PARTIALLY_REFUNDED", "PARTIALLY_REFUNDED", true},
		{"wrong case", "captured", "", false},
		{"bogus", "BOGUS", "", false},
		{"empty is unset", "", "", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var got url.Values
			f, _, _ := testFactory(t, okJSON(brokerPGBasic), func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query()
				_, _ = w.Write([]byte(`{"data":[],"meta":{"total":0}}`))
			})

			err := run(f, "order", "list", "--status", tt.value)

			if !tt.ok {
				require.Error(t, err)
				assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
				assert.Contains(t, err.Error(), "CAPTURED")
				assert.Nil(t, got, "no request may be made")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.query, got.Get("order_status"))
		})
	}
}

func TestMaskPAN(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"bare pan is masked", "5123450000000008", "****0008"},
		{"shortest pan is masked", "4111111111111", "****1111"},
		{"longest pan is masked", "4111111111111111111", "****1111"},
		{"gateway-masked value is untouched", "512345xxxxxx0008", "512345xxxxxx0008"},
		{"already-masked value is untouched", "****0008", "****0008"},
		{"too short to be a pan", "123456789012", "123456789012"},
		{"too long to be a pan", "41111111111111111111", "41111111111111111111"},
		{"empty stays empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, maskPAN(tc.in))
		})
	}
}

func TestOrderGet_MasksUnmaskedPAN(t *testing.T) {
	const detail = `{"data":{"id":"u1","order_id":"SEPAY-1","order_invoice_number":"DH1","order_status":"CAPTURED","order_amount":"300000.00","order_currency":"VND","transactions":[{"id":"t1","payment_method":"CARD","transaction_status":"APPROVED","card_number":"5123450000000008","card_brand":"MASTERCARD"}]}}`

	f, out, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(detail))
	f.Global.Output = "json"
	require.NoError(t, run(f, "order", "get", "DH1"))
	assert.NotContains(t, out.String(), "5123450000000008")
	assert.Contains(t, out.String(), `"card_number": "****0008"`)
}

func TestOrderGet_TableMasksUnmaskedPAN(t *testing.T) {
	const detail = `{"data":{"id":"u1","order_id":"SEPAY-1","order_invoice_number":"DH1","order_status":"CAPTURED","order_amount":"300000.00","order_currency":"VND","transactions":[{"id":"t1","payment_method":"CARD","transaction_type":"PAYMENT","transaction_status":"APPROVED","card_number":"5123450000000008","card_brand":"MASTERCARD","transaction_date":"2025-08-28 15:43:41"}]}}`

	f, out, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(detail))
	require.NoError(t, run(f, "order", "get", "DH1"))
	s := out.String()
	assert.NotContains(t, s, "5123450000000008")
	assert.Contains(t, s, "****0008")
	assert.Contains(t, s, "APPROVED", "the rest of the row still renders")
}

func TestOrderList_MasksUnmaskedPAN(t *testing.T) {
	const list = `{"data":[{"id":"u1","order_id":"SEPAY-1","order_invoice_number":"DH1","order_status":"CAPTURED","order_amount":"300000.00","order_currency":"VND","created_at":"2025-09-05 13:31:42","transactions":[{"id":"t1","payment_method":"CARD","transaction_status":"APPROVED","card_number":"4111111111111111"}]}],"meta":{"per_page":20,"total":1,"has_more":false,"current_page":1,"page_count":1}}`

	f, out, _ := testFactory(t, okJSON(brokerPGBasic), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(list))
	})
	f.Global.Output = "json"
	require.NoError(t, run(f, "order", "list"))
	assert.NotContains(t, out.String(), "4111111111111111")
	assert.Contains(t, out.String(), `"card_number": "****1111"`)
}

func TestOrderGet_GatewayMaskedValueIsPreserved(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(orderDetail))
	f.Global.Output = "json"
	require.NoError(t, run(f, "order", "get", "DH1"))
	assert.Contains(t, out.String(), `"card_number": "512345xxxxxx0008"`)
}

func TestOrderGet_SanitizesTerminalEscapes(t *testing.T) {
	body := `{"data":{"id":"u1","order_id":"SEPAY-1","order_invoice_number":"DH1\u001b]0;PWNED\u0007","order_status":"CAPTURED\u001b[2J","order_amount":"300000.00","order_currency":"VND","created_at":"2025-08-28 15:43:48","updated_at":"2025-08-28 15:43:48","transactions":[]}}`
	f, out, _ := testFactory(t, okJSON(brokerPGBasic), okJSON(body))
	require.NoError(t, run(f, "order", "get", "DH1"))
	s := out.String()
	assert.Contains(t, s, "DH1")
	assert.Contains(t, s, "CAPTURED")
	assert.NotContains(t, s, "\x1b]0;")
	assert.NotContains(t, s, "\x1b[2J")
}

func TestOrderList_SearchAndQSendTheSameParam(t *testing.T) {
	for _, name := range []string{"--search", "--q"} {
		t.Run(name, func(t *testing.T) {
			var got url.Values
			f, _, _ := testFactory(t, okJSON(brokerPGBasic), func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query()
				_, _ = w.Write([]byte(orderList))
			})
			require.NoError(t, run(f, "order", "list", name, "DH1"))
			assert.Equal(t, "DH1", got.Get("q"))
		})
	}
	f, _, _ := testFactory(t, okJSON(brokerPGBasic), func(http.ResponseWriter, *http.Request) { t.Error("no request expected") })
	err := run(f, "order", "list", "--search", "a", "--q", "a")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.ErrorContains(t, err, "--search and --q are the same flag; pass one")
}

func TestOrderList_ReversedRangeFailsBeforeAnyRequest(t *testing.T) {
	called := false
	record := func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"data":[],"meta":{"total":0}}`))
	}
	f, _, _ := testFactory(t, record, record)
	err := run(f, "order", "list", "--from", "2026-07-10", "--to", "2026-07-01")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.False(t, called, "a reversed range would return an empty list, not an error")
}
