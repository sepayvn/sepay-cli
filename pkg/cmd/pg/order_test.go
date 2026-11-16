package pg

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orderList and orderDetail are API v2 bodies, which order list and get read on sandbox.
const orderList = `{"status":"success","data":[{"id":"u1","customer_id":"2427","order_id":"SEPAY-1","order_invoice_number":"DH1","order_status":"CAPTURED","order_amount":300000,"order_currency":"VND","created_at":"2025-09-05 13:31:42","updated_at":"2025-09-05 13:31:42"}],"meta":{"pagination":{"total":1,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`

const orderDetail = `{"status":"success","data":{"id":"u1","customer_id":null,"order_id":"SEPAY-1","order_invoice_number":"DH1","order_status":"CAPTURED","order_amount":300000,"order_currency":"VND","authentication_status":"AUTHENTICATION_SUCCESSFUL","created_at":"2025-08-28 15:43:48","updated_at":"2025-08-28 15:43:48","transactions":[{"id":"t1","payment_method":"CARD","transaction_type":"PAYMENT","transaction_amount":300000,"transaction_currency":"VND","transaction_status":"APPROVED","card":{"brand":"MASTERCARD","last4":"0008"},"transaction_date":"2025-08-28 15:43:41"}]}}`

const emptyOrderList = `{"status":"success","data":[],"meta":{"pagination":{"total":0,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`

func TestOrderList_Table(t *testing.T) {
	f, out, errOut, got := sandboxFactory(t, okJSON(orderList))
	require.NoError(t, run(f, "order", "list", "--status", "CAPTURED", "--from", "2025-09-01"))
	s := out.String()
	assert.Contains(t, s, "DH1")
	assert.Contains(t, s, "CAPTURED")
	assert.Contains(t, s, "300.000")
	assert.Contains(t, errOut.String(), "1 of 1")
	assert.Equal(t, "CAPTURED", got.query.Get("order_status"))
	assert.Equal(t, "2025-09-01", got.query.Get("from_created_at"))
}

func TestOrderList_JSONEmptyIsArray(t *testing.T) {
	f, out, _, _ := sandboxFactory(t, okJSON(emptyOrderList))
	f.Global.Output = "json"
	require.NoError(t, run(f, "order", "list"))
	assert.Contains(t, out.String(), "[]")
}

func TestOrderGet_ShowsTransactions(t *testing.T) {
	f, out, _, _ := sandboxFactory(t, okJSON(orderDetail))
	require.NoError(t, run(f, "order", "get", "DH1"))
	s := out.String()
	assert.Contains(t, s, "DH1")
	assert.Contains(t, s, "CARD")
	assert.Contains(t, s, "MASTERCARD ****0008")
	assert.NotContains(t, s, "512345xxxxxx0008")
	assert.Contains(t, s, "APPROVED")
}

func TestOrderGet_JSONIncludesTransactions(t *testing.T) {
	f, out, _, _ := sandboxFactory(t, okJSON(orderDetail))
	f.Global.Output = "json"
	require.NoError(t, run(f, "order", "get", "DH1"))
	assert.Contains(t, out.String(), `"transactions"`)
	assert.Contains(t, out.String(), `"MASTERCARD"`)
}

func TestOrderGet_NotFoundMapsToError(t *testing.T) {
	f, _, _, _ := sandboxFactory(t, apiError(http.StatusNotFound, "not_found", "Order not found"))
	err := run(f, "order", "get", "NOPE")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
}

func TestOrderGet_NoArgNonInteractiveIsUsageError(t *testing.T) {
	f, _, _, got := sandboxFactory(t, okJSON(emptyOrderList))
	err := run(f, "order", "get")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Empty(t, got.paths, "no list is read without a terminal to pick on")
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
			f, _, _, got := sandboxFactory(t, okJSON(emptyOrderList))

			err := run(f, "order", "list", "--status", tt.value)

			if !tt.ok {
				require.Error(t, err)
				assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
				assert.Contains(t, err.Error(), "CAPTURED")
				assert.Empty(t, got.paths, "no request may be made")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.query, got.query.Get("order_status"))
		})
	}
}

func TestTransactionCard(t *testing.T) {
	for _, tc := range []struct {
		name string
		tx   wireTransaction
		want *card
	}{
		{"bare sandbox pan keeps brand and last 4", wireTransaction{CardNumber: "5123450000000008", CardBrand: "MASTERCARD"}, &card{"MASTERCARD", "0008"}},
		{"gateway-masked sandbox number keeps last 4", wireTransaction{CardNumber: "512345xxxxxx0008", CardBrand: "MASTERCARD"}, &card{"MASTERCARD", "0008"}},
		{"already-masked sandbox number keeps last 4", wireTransaction{CardNumber: "****0008"}, &card{"", "0008"}},
		{"number not ending in 4 digits has no card", wireTransaction{CardNumber: "5123450000xxxxxx", CardBrand: "VISA"}, nil},
		{"empty number has no card", wireTransaction{CardBrand: "VISA"}, nil},
		{"production card object is kept", wireTransaction{Card: &card{"VISA", "1234"}}, &card{"VISA", "1234"}},
		{"production card with a bad last4 is dropped", wireTransaction{Card: &card{"VISA", "12a4"}}, nil},
		{"production last4 longer than 4 is dropped", wireTransaction{Card: &card{"VISA", "12345"}}, nil},
		{"production empty last4 is dropped", wireTransaction{Card: &card{"VISA", ""}}, nil},
		{"sandbox number shorter than 4 has no card", wireTransaction{CardNumber: "123", CardBrand: "VISA"}, nil},
		{"sandbox number padded with spaces keeps last 4", wireTransaction{CardNumber: " 411111xxxxxx1111 ", CardBrand: "VISA"}, &card{"VISA", "1111"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.tx.card())
		})
	}
}

func TestCardString(t *testing.T) {
	assert.Equal(t, "VISA ****1234", (&card{"VISA", "1234"}).String())
	assert.Equal(t, "****0008", (&card{Last4: "0008"}).String(), "no brand leaves no leading space")
	assert.Equal(t, "-", (*card)(nil).String(), "a transaction that is not a card payment")
}

func TestVNDAmount(t *testing.T) {
	whole := func(n int64) *int64 { return &n }
	for _, tc := range []struct {
		in   string
		want *int64
	}{
		{`150000`, whole(150000)},
		{`"150000.00"`, whole(150000)},
		{`"150000.50"`, whole(150001)},
		{`"300000"`, whole(300000)},
		{`0`, whole(0)},
		{`null`, nil},
		{`""`, nil},
		{`"99999999999.00"`, whole(99999999999)},
		{`9007199254740991`, whole(9007199254740991)},
		{`-9007199254740991`, whole(-9007199254740991)},
		{`9007199254740993`, whole(9007199254740993)},
		{`"999999999999999999.99"`, whole(1000000000000000000)},
		{`"999999999999999999.49"`, whole(999999999999999999)},
		{`1000000000000000000`, whole(1000000000000000000)},
		{`"9223372036854775807"`, whole(9223372036854775807)},
		{`"-9223372036854775807"`, whole(-9223372036854775807)},
		{`"-150000.50"`, whole(-150001)},
		{`"-0.40"`, whole(0)},
		{`"1.5e3"`, whole(1500)},
		{`1E2`, whole(100)},
	} {
		var got vnd
		require.NoError(t, json.Unmarshal([]byte(tc.in), &got), tc.in)
		assert.Equal(t, tc.want, got.ptr(), tc.in)
	}
	for _, bad := range []string{
		`"abc"`, `"NaN"`, `"Inf"`, `"-Inf"`, `"1e30"`, `1e30`,
		`"9223372036854775808"`, `"9223372036854775807.5"`, `"-9223372036854775808"`,
		`"0x10"`, `"1/2"`, `"+5"`, `".5"`, `"5."`, `" 5"`, `"1e1000"`,
		`"0000000000000000000001"`, `"1.000000000000000000000"`,
	} {
		var got vnd
		assert.Error(t, json.Unmarshal([]byte(bad), &got), bad)
	}
}

// A transaction whose order has no payment method keeps null, as the server sends it.
func TestOrderGet_MissingPaymentMethodStaysNull(t *testing.T) {
	const detail = `{"status":"success","data":{"id":"u1","order_id":"SEPAY-1","order_invoice_number":"DH1","order_status":"CAPTURED","order_amount":1,"order_currency":"VND","transactions":[{"id":"t1","payment_method":null,"transaction_type":"PAYMENT","transaction_status":"APPROVED","transaction_amount":1}]}}`

	f, out, _, _ := sandboxFactory(t, okJSON(detail))
	f.Global.Output = "json"
	require.NoError(t, run(f, "order", "get", "DH1"))
	assert.Contains(t, out.String(), `"payment_method": null`)

	f, out, _, _ = sandboxFactory(t, okJSON(detail))
	require.NoError(t, run(f, "order", "get", "DH1"))
	assert.Regexp(t, `(?m)^ +PAYMENT `, out.String(), "a missing method leaves the METHOD cell empty")
}

// An amount the server does not have stays null, never a zero-VND order.
func TestOrderGet_MissingAmountStaysNull(t *testing.T) {
	const detail = `{"status":"success","data":{"id":"u1","order_id":"SEPAY-1","order_invoice_number":"DH1","order_status":"CAPTURED","order_amount":null,"order_currency":"VND","transactions":[{"id":"t1","payment_method":"CARD","transaction_status":"APPROVED","transaction_amount":null}]}}`

	f, out, _, _ := sandboxFactory(t, okJSON(detail))
	f.Global.Output = "json"
	require.NoError(t, run(f, "order", "get", "DH1"))
	assert.Contains(t, out.String(), `"order_amount": null`)
	assert.Contains(t, out.String(), `"transaction_amount": null`)

	f, out, _, _ = sandboxFactory(t, okJSON(detail))
	require.NoError(t, run(f, "order", "get", "DH1"))
	assert.Contains(t, out.String(), "Amount:       - VND")
	assert.Regexp(t, `(?m)^CARD +- +APPROVED `, out.String(), "a null transaction amount prints -")
	assert.NotContains(t, out.String(), " 0 VND")
}

// The decoder keeps only the brand and last 4 of a card number, the gateway's transaction field, in
// whatever body carries one: no other card detail reaches output.
func TestOrderGet_CardNumberNeverReachesOutput(t *testing.T) {
	const detail = `{"status":"success","data":{"id":"u1","order_id":"SEPAY-1","order_invoice_number":"DH1","order_status":"CAPTURED","order_amount":300000,"order_currency":"VND","transactions":[{"id":"t1","payment_method":"CARD","transaction_type":"PAYMENT","transaction_amount":300000,"transaction_status":"APPROVED","card_number":"5123450000000008","card_brand":"MASTERCARD","card_holder_name":"NGUYEN VAN A","card_expiry":"12/30","card_funding_method":"CREDIT","transaction_date":"2025-08-28 15:43:41"}]}}`

	for _, output := range []string{"json", "table"} {
		t.Run(output, func(t *testing.T) {
			f, out, _, _ := sandboxFactory(t, okJSON(detail))
			f.Global.Output = output
			require.NoError(t, run(f, "order", "get", "DH1"))
			s := out.String()
			for _, leaked := range []string{"5123450000000008", "card_number", "NGUYEN VAN A", "12/30", "CREDIT", "card_holder_name", "card_expiry", "card_funding_method"} {
				assert.NotContains(t, s, leaked)
			}
			assert.Contains(t, s, "0008")
		})
	}
}

// A list row's transactions print the card shape {brand,last4}, never a card number.
func TestOrderList_TransactionsCarryTheCardShape(t *testing.T) {
	const list = `{"status":"success","data":[{"id":"u1","order_id":"SEPAY-1","order_invoice_number":"DH1","order_status":"CAPTURED","order_amount":300000,"order_currency":"VND","created_at":"2025-09-05 13:31:42","transactions":[{"id":"t1","payment_method":"CARD","transaction_status":"APPROVED","card_number":"4111111111111111"}]}],"meta":{"pagination":{"total":1,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`

	f, out, _, _ := sandboxFactory(t, okJSON(list))
	f.Global.Output = "json"
	require.NoError(t, run(f, "order", "list"))
	assert.NotContains(t, out.String(), "4111111111111111")
	assert.NotContains(t, out.String(), "card_number")
	assert.Contains(t, out.String(), `"last4": "1111"`)
}

func TestOrderGet_SanitizesTerminalEscapes(t *testing.T) {
	body := `{"status":"success","data":{"id":"u1","order_id":"SEPAY-1","order_invoice_number":"DH1\u001b]0;PWNED\u0007","order_status":"CAPTURED\u001b[2J","order_amount":300000,"order_currency":"VND","created_at":"2025-08-28 15:43:48","updated_at":"2025-08-28 15:43:48","transactions":[]}}`
	f, out, _, _ := sandboxFactory(t, okJSON(body))
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
			f, _, _, got := sandboxFactory(t, okJSON(orderList))
			require.NoError(t, run(f, "order", "list", name, "DH1"))
			assert.Equal(t, "DH1", got.query.Get("q"))
		})
	}
	f, _, _, _ := sandboxFactory(t, func(http.ResponseWriter, *http.Request) { t.Error("no request expected") })
	err := run(f, "order", "list", "--search", "a", "--q", "a")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.ErrorContains(t, err, "--search and --q are the same flag; pass one")
}

func TestOrderList_ReversedRangeFailsBeforeAnyRequest(t *testing.T) {
	f, _, _, got := sandboxFactory(t, okJSON(emptyOrderList))
	err := run(f, "order", "list", "--from", "2026-07-10", "--to", "2026-07-01")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Empty(t, got.paths, "a reversed range would return an empty list, not an error")
}
