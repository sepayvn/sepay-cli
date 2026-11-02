package bankhub

import (
	"io"
	"net/http"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const simCreated = `{"code":201,"message":"Mock transaction created successfully","data":{"transaction_id":"TX9","reference_number":"REF9","transaction_date":"2026-07-23 10:30:00","amount":"100000.00","transfer_type":"credit","accumulated":"5100000.00"}}`

func TestSimulateTransaction_Creates(t *testing.T) {
	var gotBody string
	f, out, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(simCreated))
	})
	require.NoError(t, run(f, "sandbox", "simulate-transaction",
		"--bank-account", "ba_1", "--amount", "100000", "--content", "test payment", "--type", "credit"))
	s := out.String()
	assert.Contains(t, s, "TX9")
	assert.Contains(t, s, "100.000")   // amount VND
	assert.Contains(t, s, "5.100.000") // accumulated VND
	assert.Contains(t, gotBody, `"bank_account_xid":"ba_1"`)
	assert.Contains(t, gotBody, `"amount":100000`) // sent as a JSON integer
	assert.Contains(t, gotBody, `"transaction_content":"test payment"`)
}

func TestSimulateTransaction_RequiresBankAccount(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(simCreated))
	err := run(f, "sandbox", "simulate-transaction", "--amount", "1000", "--content", "x")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

// "--amount 0" is a value the caller typed, not a missing flag.
func TestSimulateTransaction_AmountZeroIsNotAMissingFlag(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"explicit zero", []string{"--amount", "0"}, "--amount must be greater than 0"},
		{"negative", []string{"--amount", "-5"}, "--amount must be greater than 0"},
		{"omitted", nil, "--amount is required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), okJSON(simCreated))
			args := append([]string{"sandbox", "simulate-transaction", "--bank-account", "ba_1", "--content", "x"}, tt.args...)
			err := run(f, args...)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.EqualError(t, err, tt.want)
		})
	}
}

func TestSimulateTransaction_DebitUnsupportedMapsToError(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":422,"message":"Không hợp lệ","errors":{"transfer_type":"Ngân hàng này chỉ hỗ trợ đồng bộ giao dịch tiền vào, không hỗ trợ tiền ra"}}`))
	})
	err := run(f, "sandbox", "simulate-transaction",
		"--bank-account", "ba_1", "--amount", "1000", "--content", "x", "--type", "debit")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
}

func TestSimulateTransaction_TypeValidation(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(brokerBankHubBearer), func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server on an invalid transfer type")
	})
	err := run(f, "sandbox", "simulate-transaction",
		"--bank-account", "ba_1", "--amount", "1000", "--content", "x", "--type", "CREDIT")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "credit, debit")
}
