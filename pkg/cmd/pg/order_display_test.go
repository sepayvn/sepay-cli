package pg

import (
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	green  = "32"
	yellow = "33"
	red    = "31"
	dim    = "2"
	plain  = ""
)

func painted(code, s string) string {
	if code == plain {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func colorStreams() *iostreams.IOStreams {
	ios, _, _, _ := iostreams.Test()
	ios.SetColorEnabled(true)
	return ios
}

func TestOrderStatusColor(t *testing.T) {
	want := map[string]string{
		"CAPTURED": green, "AUTHORIZED": green, "PARTIALLY_CAPTURED": green, "DISBURSED": green, "AUTHENTICATED": green,
		"AUTHENTICATION_NOT_NEEDED": yellow, "INITIATED": yellow, "AUTHENTICATION_INITIATED": yellow, "FUNDING": yellow, "REFUND_REQUESTED": yellow,
		"FAILED": red, "CANCELLED": red, "AUTHENTICATION_UNSUCCESSFUL": red, "DISPUTED": red, "CHARGEBACK_PROCESSED": red,
		"REFUNDED": dim, "PARTIALLY_REFUNDED": dim, "EXCESSIVELY_REFUNDED": dim, "VERIFIED": dim,
		"SOMETHING_NEW": plain, "": plain,
	}
	for _, status := range orderStatuses {
		_, ok := want[status]
		assert.True(t, ok, "%s has no color group", status)
	}
	ios := colorStreams()
	for status, code := range want {
		assert.Equal(t, painted(code, status), orderStatusColor(ios, status), status)
	}

	ios.SetColorEnabled(false)
	assert.Equal(t, "CAPTURED", orderStatusColor(ios, "CAPTURED"), "no color off a terminal")
}

// Every value of App\Enums\PgTransactionStatus, grouped.
func TestTransactionStatusColor(t *testing.T) {
	want := map[string]string{
		"APPROVED": green, "APPROVED_AUTO": green, "APPROVED_PENDING_SETTLEMENT": green, "PARTIALLY_APPROVED": green,
		"PENDING": yellow, "SUBMITTED": yellow, "AUTHENTICATION_IN_PROGRESS": yellow, "DEFERRED_TRANSACTION_RECEIVED": yellow, "REFERRED": yellow,
		"ABORTED": red, "ACQUIRER_SYSTEM_ERROR": red, "AUTHENTICATION_FAILED": red, "BLOCKED": red, "CANCELLED": red,
		"DECLINED": red, "DECLINED_AVS": red, "DECLINED_AVS_CSC": red, "DECLINED_CSC": red, "DECLINED_DO_NOT_CONTACT": red,
		"DECLINED_INVALID_PIN": red, "DECLINED_PAYMENT_PLAN": red, "DECLINED_PIN_REQUIRED": red, "DUPLICATE_BATCH": red,
		"EXCEEDED_RETRY_LIMIT": red, "EXPIRED_CARD": red, "INSUFFICIENT_FUNDS": red, "INVALID_CSC": red, "LOCK_FAILURE": red,
		"NOT_SUPPORTED": red, "NO_BALANCE": red, "SYSTEM_ERROR": red, "TIMED_OUT": red, "UNSPECIFIED_FAILURE": red,
		"BALANCE_AVAILABLE": plain, "BALANCE_UNKNOWN": plain, "UNKNOWN": plain, "": plain,
		"SOMETHING_NEW": plain, "NOT_DECLINED": plain,
	}
	ios := colorStreams()
	for status, code := range want {
		assert.Equal(t, painted(code, status), transactionStatusColor(ios, status), status)
	}
}

// Color reaches the terminal table and lines only; JSON stays plain even when color is on.
func TestOrderRead_ColorsOnlyTheTerminal(t *testing.T) {
	f, out, _, _ := prodFactory(t, okJSON(prodOrderList))
	f.IOStreams.SetColorEnabled(true)
	require.NoError(t, run(f, "order", "list"))
	assert.Contains(t, out.String(), painted(green, "CAPTURED"))
	assert.Contains(t, out.String(), painted(red, "CANCELLED"))

	body := strings.NewReplacer(`"order_status":"CANCELLED"`, `"order_status":"AUTHENTICATION_NOT_NEEDED"`, `"order_status":"CAPTURED"`, `"order_status":"REFUNDED"`).Replace(prodOrderList)
	f, out, _, _ = prodFactory(t, okJSON(body))
	f.IOStreams.SetColorEnabled(true)
	require.NoError(t, run(f, "order", "list"))
	assert.Contains(t, out.String(), painted(yellow, "AUTHENTICATION_NOT_NEEDED"), "yellow survives the table's sanitiser")
	assert.Contains(t, out.String(), painted(dim, "REFUNDED"))

	f, out, _, _ = prodFactory(t, okJSON(prodOrderDetail))
	f.IOStreams.SetColorEnabled(true)
	require.NoError(t, run(f, "order", "get", "INV-A"))
	assert.Contains(t, out.String(), "Status:       "+painted(green, "CAPTURED"))
	assert.Contains(t, out.String(), painted(green, "APPROVED"))

	for _, args := range [][]string{{"order", "list"}, {"order", "get", "INV-A"}} {
		body := prodOrderList
		if args[1] == "get" {
			body = prodOrderDetail
		}
		f, out, _, _ = prodFactory(t, okJSON(body))
		f.IOStreams.SetColorEnabled(true)
		f.Global.Output = "json"
		require.NoError(t, run(f, args...))
		assert.NotContains(t, out.String(), "\x1b", "machine output carries no color")
	}
}

// At 80 columns the list leaves out CUSTOMER, then CREATED, and keeps ids, status and amount.
func TestOrderList_DropsColumnsOnAnEightyColumnTerminal(t *testing.T) {
	body := strings.NewReplacer(`"order_status":"CANCELLED"`, `"order_status":"AUTHENTICATION_NOT_NEEDED"`,
		`"order_id":"ORD-1"`, `"order_id":"SEPAY-20261110-000123"`).Replace(prodOrderList)
	f, out, _, _ := prodFactory(t, okJSON(body))
	f.IOStreams.SetTerminalWidth(80)

	require.NoError(t, run(f, "order", "list"))

	assert.Equal(t, "ORDER CODE  PAY CODE               STATUS                     AMOUNT\n"+
		"INV-B       ORD-2                  AUTHENTICATION_NOT_NEEDED  200.000\n"+
		"INV-A       SEPAY-20261110-000123  CAPTURED                   150.000\n", out.String())

	f, out, _, _ = prodFactory(t, okJSON(body))
	f.IOStreams.SetTerminalWidth(90)
	require.NoError(t, run(f, "order", "list"))
	assert.Contains(t, out.String(), "CREATED", "at 90 columns only CUSTOMER goes")
	assert.NotContains(t, out.String(), "CUSTOMER")

	f, out, _, _ = prodFactory(t, okJSON(body))
	f.IOStreams.SetTerminalWidth(40)
	require.NoError(t, run(f, "order", "list"))
	assert.Contains(t, out.String(), "ORDER CODE  PAY CODE               STATUS                     AMOUNT\n",
		"narrower still, no id, status or amount column goes and the line overflows")
	assert.Contains(t, out.String(), "SEPAY-20261110-000123")
}
