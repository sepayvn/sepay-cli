package transaction

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const oneTxList = `{"status":"success","data":[{"id":"tx_1","transaction_date":"2026-02-18 09:35:12","account_number":"0123456789","va":null,"transfer_type":"in","amount_in":100000,"amount_out":0,"accumulated":5000000,"transaction_content":"thanh toan","reference_number":"FT001","code":null,"bank_brand_name":"ACB","bank_account_id":"ba_1","va_id":null,"webhook_success":1}],"meta":{"pagination":{"total":57,"per_page":20,"current_page":1,"last_page":3,"has_more":true}}}`

const oneTx = `{"status":"success","data":{"id":"tx_1","transaction_date":"2026-02-18 09:35:12","account_number":"0123456789","va":null,"transfer_type":"out","amount_in":0,"amount_out":250000,"accumulated":5000000,"transaction_content":"rut tien","reference_number":"FT002","code":null,"bank_brand_name":"ACB","bank_account_id":"ba_1","va_id":null,"webhook_success":null}}`

func testFactory(t *testing.T, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(srv.URL, "test")
		c.MaxRetries = 0 // no backoff sleeps in tests
		return c, nil
	}
	return f, out, errOut
}

func run(cmd *cobra.Command, args ...string) error {
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func okJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestList_Table(t *testing.T) {
	f, out, errOut := testFactory(t, okJSON(oneTxList))
	require.NoError(t, run(NewCmdTransaction(f), "list"))

	assert.Contains(t, out.String(), "tx_1")
	assert.Contains(t, out.String(), "+100.000")
	assert.Contains(t, out.String(), "ok")
	assert.Contains(t, errOut.String(), "page 1/3 · 1 of 57")
}

func TestList_JSON_PreservesNull(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(oneTxList))
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdTransaction(f), "list"))

	s := out.String()
	assert.Contains(t, s, `"code": null`)
	assert.Contains(t, s, `"va_id": null`)
	assert.Contains(t, s, `"bank_account_id": "ba_1"`)
	assert.Contains(t, s, `"amount_in": 100000`)
}

func TestList_Empty(t *testing.T) {
	body := `{"status":"success","data":[],"meta":{"pagination":{"total":0,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`
	f, out, errOut := testFactory(t, okJSON(body))
	require.NoError(t, run(NewCmdTransaction(f), "list"))

	assert.Contains(t, errOut.String(), "No transactions found.")
	assert.Empty(t, out.String())
}

func TestList_FlagToQueryMapping(t *testing.T) {
	var got url.Values
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(oneTxList))
	})

	require.NoError(t, run(NewCmdTransaction(f), "list",
		"--q", "abc", "--transfer-type", "out", "--amount-out-min", "1000",
		"--bank-account-id", "ba_9", "--page", "2", "--per-page", "50"))

	assert.Equal(t, "abc", got.Get("q"))
	assert.Equal(t, "out", got.Get("transfer_type"))
	assert.Equal(t, "1000", got.Get("amount_out_min"))
	assert.Equal(t, "ba_9", got.Get("bank_account_id"))
	assert.Equal(t, "2", got.Get("page"))
	assert.Equal(t, "50", got.Get("per_page"))
	assert.Empty(t, got.Get("amount_in_min"))
	assert.Empty(t, got.Get("code"))
}

func TestGet_Table(t *testing.T) {
	var gotPath string
	f, out, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(oneTx))
	})

	require.NoError(t, run(NewCmdTransaction(f), "get", "tx_1"))
	assert.Equal(t, "/transactions/tx_1", gotPath)
	assert.Contains(t, out.String(), "-250.000") // money-out shows a minus sign
	assert.Contains(t, out.String(), "FT002")
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   cmdutil.ExitCode
	}{
		{"unauthorized", 401, `{"status":"error","error_code":"unauthorized","message":"x"}`, cmdutil.ExitAuth},
		{"insufficient_scope", 403, `{"status":"error","error_code":"insufficient_scope","message":"x"}`, cmdutil.ExitScope},
		{"not_found", 404, `{"status":"error","error_code":"not_found","message":"x"}`, cmdutil.ExitError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			err := run(NewCmdTransaction(f), "list")
			require.Error(t, err)
			assert.Equal(t, tc.want, cmdutil.ExitCodeForError(err))
		})
	}
}

// A null masked field must not render as a real zero.
func TestTransaction_MaskedFieldsAreNotZero(t *testing.T) {
	const masked = `{
		"id":"tx_1","transaction_date":"2026-08-05 10:00:00","account_number":"111",
		"transfer_type":"out","amount_in":0,
		"amount_out":null,"accumulated":null,
		"transaction_content":null,"reference_number":null,
		"bank_brand_name":"ACB","bank_account_id":"ba_1"
	}`

	var tx Transaction
	require.NoError(t, json.Unmarshal([]byte(masked), &tx))

	assert.Nil(t, tx.AmountOut, "a withheld amount must not decode to 0")
	assert.Nil(t, tx.Accumulated)
	assert.Nil(t, tx.TransactionContent)
	assert.Nil(t, tx.ReferenceNumber)

	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}

	amount := signedAmount(f, tx)
	assert.Contains(t, amount, "hidden", "a withheld amount must not read as a number")
	assert.NotContains(t, amount, "+0", "direction comes from transfer_type, not the amount")
	assert.Equal(t, "(hidden)", dashHidden(tx.TransactionContent))
	assert.Equal(t, "(hidden)", amountOrHidden(tx.Accumulated))

	out := int64(5000000)
	empty := ""
	visible := Transaction{TransferType: "out", AmountOut: &out, TransactionContent: &empty}
	assert.Contains(t, signedAmount(f, visible), "5")
	assert.Equal(t, "-", dashHidden(visible.TransactionContent))
}

func TestList_FieldsRejectsUnknownColumn(t *testing.T) {
	var called bool
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(oneTxList))
	})

	err := run(NewCmdTransaction(f), "list", "--fields", "id,amount_inn")
	require.Error(t, err)
	require.EqualError(t, err, `--fields has no column "amount_inn"`)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.False(t, called, "no request may be sent")
	assert.Empty(t, out.String())
}

func TestList_FieldsAllowsEveryServedColumn(t *testing.T) {
	var got url.Values
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(oneTxList))
	})

	names := make([]string, 0, 15)
	for name := range transactionFields() {
		names = append(names, name)
	}
	list := strings.Join(names, ",")
	require.NoError(t, run(NewCmdTransaction(f), "list", "--fields", list))
	assert.Equal(t, list, got.Get("fields"))
}

func TestList_FieldsPassThrough(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"valid subset", []string{"--fields", "id,amount_in"}, "id,amount_in"},
		{"spaced list", []string{"--fields", "id, amount_in"}, "id, amount_in"},
		{"repeated name", []string{"--fields", "id,id,id"}, "id,id,id"},
		{"empty is the documented no-op", []string{"--fields", ""}, ""},
		{"flag absent", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got url.Values
			f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query()
				_, _ = w.Write([]byte(oneTxList))
			})
			require.NoError(t, run(NewCmdTransaction(f), append([]string{"list"}, tc.argv...)...))
			assert.Equal(t, tc.want, got.Get("fields"))
		})
	}
}

func TestGet_EmptyXIDIsAUsageError(t *testing.T) {
	var called bool
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(oneTxList))
	})

	err := run(NewCmdTransaction(f), "get", "")
	require.Error(t, err)
	require.EqualError(t, err, "a transaction xid is required")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.False(t, called, "no request may leave the machine")
	assert.Empty(t, out.String())
}

func TestGet_NonEmptyXIDStillFetches(t *testing.T) {
	var path string
	f, out, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(oneTx))
	})

	require.NoError(t, run(NewCmdTransaction(f), "get", "tx_1"))
	assert.Equal(t, "/transactions/tx_1", path)
	assert.Contains(t, out.String(), "tx_1")
}

func TestList_AmountIsHiddenWhenItsColumnsAreNotRequested(t *testing.T) {
	const noAmount = `{"status":"success","data":[{"id":"tx_1","transaction_content":"thanh toan"}],"meta":{"pagination":{"total":1,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(noAmount))
	})

	require.NoError(t, run(NewCmdTransaction(f), "list", "--fields", "id,transaction_content"))

	assert.Contains(t, out.String(), hidden)
	assert.NotContains(t, out.String(), "+0", "an absent column must not read as a zero amount")
}

func TestSignedAmount_NeedsBothDirectionAndFigure(t *testing.T) {
	in, outAmt := int64(500000), int64(250000)
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}

	cases := []struct {
		name string
		tx   Transaction
		want string
	}{
		{"credit", Transaction{TransferType: "in", AmountIn: &in}, "+500.000"},
		{"debit", Transaction{TransferType: "out", AmountOut: &outAmt}, "-250.000"},
		{"credit with the amount withheld", Transaction{TransferType: "in"}, "+" + hidden},
		{"debit with the amount withheld", Transaction{TransferType: "out"}, "-" + hidden},
		{"no direction at all", Transaction{AmountIn: &in}, hidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, jsonfmt.Clean(signedAmount(f, tc.tx)))
		})
	}
}

func TestTransaction_AbsentAmountInIsNullNotZero(t *testing.T) {
	var tx Transaction
	require.NoError(t, json.Unmarshal([]byte(`{"id":"tx_1","transfer_type":"in"}`), &tx))
	assert.Nil(t, tx.AmountIn, "an absent amount must not decode to 0")

	var zero Transaction
	require.NoError(t, json.Unmarshal([]byte(`{"id":"tx_1","transfer_type":"in","amount_in":0}`), &zero))
	require.NotNil(t, zero.AmountIn, "a real zero must stay a real zero")
	assert.Equal(t, int64(0), *zero.AmountIn)
}

// The renamed flags must reach the API exactly as the old spellings did.
func TestList_RenamedFlagsSendTheSameParam(t *testing.T) {
	for _, tc := range []struct{ flag, old, param, value string }{
		{"--search", "--q", "q", "abc"},
		{"--bank-account", "--bank-account-id", "bank_account_id", "ba_9"},
		{"--va-xid", "--va-id", "va_id", "va_9"},
		{"--bank", "--bank-brand-name", "bank_brand_name", "ACB"},
		{"--from", "--transaction-date-from", "transaction_date_from", "2026-07-01"},
		{"--to", "--transaction-date-to", "transaction_date_to", "2026-07-31 18:00:00"},
	} {
		for _, name := range []string{tc.flag, tc.old} {
			t.Run(name, func(t *testing.T) {
				var got url.Values
				f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
					got = r.URL.Query()
					_, _ = w.Write([]byte(oneTxList))
				})
				require.NoError(t, run(NewCmdTransaction(f), "list", name, tc.value))
				assert.Equal(t, url.Values{tc.param: {tc.value}}, got)
			})
		}
		t.Run(tc.flag+" and "+tc.old, func(t *testing.T) {
			f, _, _ := testFactory(t, func(http.ResponseWriter, *http.Request) {
				t.Error("no request expected")
			})
			err := run(NewCmdTransaction(f), "list", tc.flag, tc.value, tc.old, tc.value)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), tc.flag+" and "+tc.old+" are the same flag; pass one")
		})
	}
}

func TestList_BareEndDateCoversTheWholeDay(t *testing.T) {
	cases := []struct{ flag, value, want string }{
		{"--to", "2026-10-20", "2026-10-20 23:59:59"},
		{"--transaction-date-to", "2026-10-20", "2026-10-20 23:59:59"},
		{"--to", "2026-10-20 12:00:00", "2026-10-20 12:00:00"},
		{"--to", "2026-10-20T12:00:00+07:00", "2026-10-20 12:00:00"},
	}
	for _, c := range cases {
		t.Run(c.flag+" "+c.value, func(t *testing.T) {
			var got url.Values
			f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query()
				_, _ = w.Write([]byte(oneTxList))
			})
			require.NoError(t, run(NewCmdTransaction(f), "list", "--from", "2026-10-20", c.flag, c.value))
			assert.Equal(t, "2026-10-20", got.Get("transaction_date_from"))
			assert.Equal(t, c.want, got.Get("transaction_date_to"))
		})
	}
}

func TestList_RenamedFlagsHideTheOldSpelling(t *testing.T) {
	f, _, _ := testFactory(t, okJSON(oneTxList))
	cmd := NewCmdTransaction(f)
	list, _, err := cmd.Find([]string{"list"})
	require.NoError(t, err)
	for _, old := range []string{"q", "bank-account-id", "va-id", "bank-brand-name", "transaction-date-from", "transaction-date-to"} {
		assert.True(t, list.Flags().Lookup(old).Hidden, old)
	}
}

func TestList_RefusesReversedRangesBeforeAsking(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--from", "2026-11-06", "--to", "2026-11-05"}, "--from must not be after --to"},
		{[]string{"--transaction-date-from", "2026-11-06", "--transaction-date-to", "2026-11-05"}, "--transaction-date-from must not be after --transaction-date-to"},
		{[]string{"--amount-in-min", "500", "--amount-in-max", "100"}, "--amount-in-min must not be greater than --amount-in-max"},
		{[]string{"--amount-out-min", "500", "--amount-out-max", "100"}, "--amount-out-min must not be greater than --amount-out-max"},
	}
	for _, c := range cases {
		f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
			t.Error("a reversed range must not reach the server")
		})
		err := run(NewCmdTransaction(f), append([]string{"list"}, c.args...)...)
		require.EqualError(t, err, c.want)
		assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	}
}
