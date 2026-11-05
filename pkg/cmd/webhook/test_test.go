package webhook

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/listen"
	"github.com/sepayvn/sepay-cli/pkg/cmd/sandbox"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// apiForTest routes mint, coverage, simulate and bank-account calls; simBody captures the simulate body.
func apiForTest(t *testing.T, hubURL string, simBody *[]byte, accountIDs ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/webhooks/listen":
			fmt.Fprintf(w, `{"status":"success","data":{"hub_url":%q,"listen_key":"lk_test","environment":"sandbox","heartbeat":15}}`, hubURL)
		case "/transactions/simulate":
			if simBody != nil {
				*simBody, _ = io.ReadAll(r.Body)
			}
			_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx_1"}}`))
		case "/webhooks":
			_, _ = w.Write([]byte(`{"status":"success","data":[{"id":"wh_1","name":"","event_type":"All",` +
				`"authen_type":"No_Authen","webhook_url":"https://x.test/h","is_verify_payment":false,` +
				`"skip_if_no_code":false,"only_va":false,"bank_mode":"all","bank_account_xid":null,` +
				`"bank_sub_account_xids":[],"prefix_filters":[],"alert_threshold":null,"active":true,` +
				`"created_at":""}],"meta":{"pagination":{"total":1,"per_page":100,"current_page":1,` +
				`"last_page":1,"has_more":false}}}`))
		case "/bank-accounts":
			rows := make([]string, 0, len(accountIDs))
			for _, id := range accountIDs {
				rows = append(rows, fmt.Sprintf(`{"id":%q,"account_number":"0000000001","bank_short_name":"ACB","active":1}`, id))
			}
			fmt.Fprintf(w, `{"status":"success","data":[%s],"meta":{"pagination":{"total":%d,"per_page":100,"current_page":1,"last_page":1,"has_more":false}}}`,
				strings.Join(rows, ","), len(accountIDs))
		default:
			assert.Failf(t, "unexpected request", "unexpected API call: %s %s", r.Method, r.URL.Path)
		}
	}
}

var paymentInEvent = []string{
	"event: ready\ndata: {}\n\n",
	"event: webhook\ndata: {\"id\":\"e1\",\"type\":\"payment_in\",\"payload\":{\"transferAmount\":100000}}\n\n",
}

func TestTest_Success(t *testing.T) {
	var gotBody []byte
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()

	hub := httptest.NewServer(sseHub(t, paymentInEvent...))
	defer hub.Close()

	var simBody []byte
	apiSrv := httptest.NewServer(apiForTest(t, hub.URL, &simBody))
	defer apiSrv.Close()

	f, _, errOut := factoryForAPI(apiSrv.URL)
	err := run(NewCmdWebhook(f), "test", "--forward", handler.URL, "--bank-account", "ba_1", "--amount", "100000")
	require.NoError(t, err)

	assert.Contains(t, errOut.String(), "handler responded 200")
	assert.Contains(t, string(gotBody), "transferAmount", "the real event payload is forwarded")
	assert.Contains(t, string(simBody), `"bank_account_xid":"ba_1"`)
	assert.Contains(t, string(simBody), `"type":"payment_in"`)
}

func TestTest_AutoSelectsSingleBankAccount(t *testing.T) {
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()
	hub := httptest.NewServer(sseHub(t, paymentInEvent...))
	defer hub.Close()

	var simBody []byte
	apiSrv := httptest.NewServer(apiForTest(t, hub.URL, &simBody, "ba_only"))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	err := run(NewCmdWebhook(f), "test", "--forward", handler.URL, "--amount", "100000")
	require.NoError(t, err)
	assert.Contains(t, string(simBody), `"bank_account_xid":"ba_only"`)
}

func TestTest_MultipleBankAccountsNonInteractive(t *testing.T) {
	hub := httptest.NewServer(sseHub(t, paymentInEvent...))
	defer hub.Close()
	apiSrv := httptest.NewServer(apiForTest(t, hub.URL, nil, "ba_1", "ba_2"))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := run(cmd, "test", "--forward", "localhost:3000", "--amount", "100000")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestTest_HandlerNon2xxExit6(t *testing.T) {
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer handler.Close()
	hub := httptest.NewServer(sseHub(t, paymentInEvent...))
	defer hub.Close()
	apiSrv := httptest.NewServer(apiForTest(t, hub.URL, nil))
	defer apiSrv.Close()

	f, _, errOut := factoryForAPI(apiSrv.URL)
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := run(cmd, "test", "--forward", handler.URL, "--bank-account", "ba_1", "--amount", "100000")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitForward, cmdutil.ExitCodeForError(err))
	assert.Contains(t, errOut.String(), "want 2xx")
}

func TestTest_ExpectStatusMismatchExit6(t *testing.T) {
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // 200, but we require 201
	}))
	defer handler.Close()
	hub := httptest.NewServer(sseHub(t, paymentInEvent...))
	defer hub.Close()
	apiSrv := httptest.NewServer(apiForTest(t, hub.URL, nil))
	defer apiSrv.Close()

	f, _, errOut := factoryForAPI(apiSrv.URL)
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := run(cmd, "test", "--forward", handler.URL, "--bank-account", "ba_1", "--amount", "100000", "--expect-status", "201")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitForward, cmdutil.ExitCodeForError(err))
	assert.Contains(t, errOut.String(), "returned 200, want 201")
}

func TestTest_HandlerDownExit6(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := down.URL
	down.Close() // connection refused

	hub := httptest.NewServer(sseHub(t, paymentInEvent...))
	defer hub.Close()
	apiSrv := httptest.NewServer(apiForTest(t, hub.URL, nil))
	defer apiSrv.Close()

	f, _, errOut := factoryForAPI(apiSrv.URL)
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := run(cmd, "test", "--forward", addr, "--bank-account", "ba_1", "--amount", "100000")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitForward, cmdutil.ExitCodeForError(err))
	assert.Contains(t, errOut.String(), "handler at "+addr+"/ not responding (is your handler running?)")
	assert.NotContains(t, errOut.String(), "dial tcp", "the Go transport error is not for the user")
}

func TestTest_PinHintOnlyWithoutWebhookFlag(t *testing.T) {
	event := "event: webhook\ndata: {\"id\":\"e1\",\"type\":\"payment_in\",\"webhook_xid\":\"wh_1\",\"payload\":{\"transferAmount\":100000}}\n\n"
	for _, tc := range []struct {
		name     string
		args     []string
		wantHint bool
	}{
		{"not pinned", nil, true},
		{"pinned", []string{"--webhook", "wh_1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer handler.Close()
			hub := httptest.NewServer(sseHub(t, "event: ready\ndata: {}\n\n", event))
			defer hub.Close()
			apiSrv := httptest.NewServer(apiForTest(t, hub.URL, nil))
			defer apiSrv.Close()

			f, _, errOut := factoryForAPI(apiSrv.URL)
			args := append([]string{"test", "--forward", handler.URL, "--bank-account", "ba_1", "--amount", "100000"}, tc.args...)
			err := run(NewCmdWebhook(f), args...)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitForward, cmdutil.ExitCodeForError(err))
			if tc.wantHint {
				assert.Contains(t, errOut.String(), "pin it with --webhook")
			} else {
				assert.NotContains(t, errOut.String(), "pin it with --webhook")
			}
		})
	}
}

func TestTest_JSONResult(t *testing.T) {
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()
	hub := httptest.NewServer(sseHub(t, paymentInEvent...))
	defer hub.Close()
	apiSrv := httptest.NewServer(apiForTest(t, hub.URL, nil))
	defer apiSrv.Close()

	f, out, _ := factoryForAPI(apiSrv.URL)
	f.Global.Output = "json"
	err := run(NewCmdWebhook(f), "test", "--forward", handler.URL, "--bank-account", "ba_1", "--amount", "100000")
	require.NoError(t, err)

	s := out.String()
	assert.Contains(t, s, `"ok": true`)
	assert.Contains(t, s, `"status_code": 200`)
	assert.Contains(t, s, `"event": "payment_in"`)
}

func TestTest_VACreditIsMatchedBySubAccount(t *testing.T) {
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()

	hub := httptest.NewServer(sseHub(t,
		"event: ready\ndata: {}\n\n",
		// A plain transfer on the same account arrives first and must be skipped.
		"event: webhook\ndata: {\"id\":\"e1\",\"type\":\"payment_in\",\"payload\":{\"subAccount\":null,\"transferAmount\":15000}}\n\n",
		"event: webhook\ndata: {\"id\":\"e2\",\"type\":\"payment_in\",\"payload\":{\"subAccount\":\"SBVA1\",\"transferAmount\":15000}}\n\n",
	))
	defer hub.Close()

	var simBody []byte
	apiSrv := httptest.NewServer(apiForTest(t, hub.URL, &simBody))
	defer apiSrv.Close()

	f, out, _ := factoryForAPI(apiSrv.URL)
	f.Global.Output = "json"
	err := run(NewCmdWebhook(f), "test", "--forward", handler.URL, "--bank-account", "ba_1",
		"--event", "va_credit", "--va", "SBVA1", "--amount", "15000")
	require.NoError(t, err)

	assert.Contains(t, string(simBody), `"type":"va_credit"`)
	assert.Contains(t, string(simBody), `"va_number":"SBVA1"`)
	assert.Contains(t, out.String(), `"ok": true`)
}

func TestTest_AmountOutOfRangeIsAUsageError(t *testing.T) {
	for _, amount := range []string{"999", "500000000"} {
		apiSrv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			assert.Failf(t, "unexpected request", "must not call the API with an out-of-range amount: %s", r.URL.Path)
		}))
		f, _, _ := factoryForAPI(apiSrv.URL)
		cmd := NewCmdWebhook(f)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := run(cmd, "test", "--forward", "localhost:3000", "--bank-account", "ba_1", "--amount", amount)
		apiSrv.Close()

		require.Error(t, err, amount)
		assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), amount)
		assert.Contains(t, err.Error(), "--amount must be between 1000 and 499000000")
	}
}

func TestTest_UnknownEventIsAUsageError(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		assert.Failf(t, "unexpected request", "must not call the API with an unknown event: %s", r.URL.Path)
	}))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := run(cmd, "test", "--forward", "localhost:3000", "--bank-account", "ba_1",
		"--amount", "100000", "--event", "teleport")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "--event must be one of: payment_in, payment_out, va_credit")
}

func TestTest_EventFlagHelpMatchesTheAcceptedSet(t *testing.T) {
	f, _, _ := factoryForAPI("http://127.0.0.1:1")
	for _, c := range NewCmdWebhook(f).Commands() {
		if c.Name() != "test" {
			continue
		}
		fl := c.Flags().Lookup("event")
		require.NotNil(t, fl)
		for _, ev := range sandbox.EventTypes() {
			assert.Contains(t, fl.Usage, ev)
		}
		return
	}
	require.Fail(t, "webhook test command not found")
}

func TestTest_MissingForwardNonInteractive(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		assert.Failf(t, "unexpected request", "must not call the API without a forward address: %s", r.URL.Path)
	}))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := run(cmd, "test", "--bank-account", "ba_1", "--amount", "100000")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestTest_RefusesProductionEnv(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		assert.Failf(t, "unexpected request", "must not call the API on a production env: %s", r.URL.Path)
	}))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	f.Global.Env = "production"
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := run(cmd, "test", "--forward", "localhost:3000", "--bank-account", "ba_1", "--amount", "100000")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestTest_RefusesWhenNoWebhookCoversTheAccount(t *testing.T) {
	hub := httptest.NewServer(sseHub(t))
	defer hub.Close()

	apiHandler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/webhooks/listen":
			fmt.Fprintf(w, `{"status":"success","data":{"hub_url":%q,"listen_key":"lk","environment":"sandbox","heartbeat":15}}`, hub.URL)
		case "/webhooks":
			_, _ = w.Write([]byte(`{"status":"success","data":[{"id":"wh_1","name":"","event_type":"All",` +
				`"authen_type":"No_Authen","webhook_url":"https://x.test/h","is_verify_payment":false,` +
				`"skip_if_no_code":false,"only_va":false,"bank_mode":"single","bank_account_xid":"ba_other",` +
				`"bank_sub_account_xids":[],"prefix_filters":[],"alert_threshold":null,"active":true,` +
				`"created_at":""}],"meta":{"pagination":{"total":1,"per_page":100,"current_page":1,` +
				`"last_page":1,"has_more":false}}}`))
		case "/transactions/simulate":
			assert.Fail(t, "must refuse before creating the transaction")
		}
	}

	f, _, _ := testFactory(t, apiHandler)
	err := run(NewCmdWebhook(f), "test", "--forward", "localhost:1", "--bank-account", "ba_mine", "--amount", "50000")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "no active webhook targets this bank account")
}

// Matching on direction or VA alone would judge the wrong transaction.
func TestTestOptions_Matches(t *testing.T) {
	vaCredit := testOptions{event: eventVACredit, va: "SBVA1"}

	cases := []struct {
		name  string
		opts  testOptions
		frame string
		want  bool
	}{
		{
			"a fired payment_in matches its frame",
			testOptions{event: eventPaymentIn},
			`{"type":"payment_in"}`, true,
		},
		{
			"a fired payment_in ignores an outgoing frame",
			testOptions{event: eventPaymentIn},
			`{"type":"payment_out"}`, false,
		},
		{
			"a fired va_credit matches the incoming transfer on its VA", vaCredit,
			`{"type":"payment_in","payload":{"subAccount":"SBVA1"}}`, true,
		},
		{
			"a fired va_credit ignores an outgoing transfer on the same VA", vaCredit,
			`{"type":"payment_out","payload":{"subAccount":"SBVA1"}}`, false,
		},
		{
			"a fired va_credit ignores an incoming transfer on another VA", vaCredit,
			`{"type":"payment_in","payload":{"subAccount":"SBVA2"}}`, false,
		},
		{
			"a fired va_credit ignores a transfer to the account number", vaCredit,
			`{"type":"payment_in","payload":{}}`, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr, err := listen.Decode([]byte(tc.frame))
			require.NoError(t, err)
			assert.Equal(t, tc.want, tc.opts.matches(fr))
		})
	}
}

func TestTest_VACreditTimeoutNamesADebitOnTheSameVA(t *testing.T) {
	handler := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "an outgoing transfer must not be forwarded as the fired VA credit")
	}))
	defer handler.Close()

	hub := httptest.NewServer(sseHub(t,
		"event: ready\ndata: {}\n\n",
		"event: webhook\ndata: {\"id\":\"e1\",\"type\":\"payment_out\",\"payload\":{\"subAccount\":\"SBVA1\"}}\n\n",
	))
	defer hub.Close()

	apiSrv := httptest.NewServer(apiForTest(t, hub.URL, nil))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := run(cmd, "test", "--forward", handler.URL, "--bank-account", "ba_1",
		"--event", "va_credit", "--va", "SBVA1", "--amount", "15000", "--timeout", "300ms")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "SBVA1")
	assert.Contains(t, err.Error(), "payment_out")
}

// A sibling's delivery is signed with a secret the handler does not hold.
func TestTest_WebhookFlagPinsTheJudgedDelivery(t *testing.T) {
	var gotBody []byte
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()

	hub := httptest.NewServer(sseHub(t,
		"event: ready\ndata: {}\n\n",
		"event: webhook\ndata: {\"id\":\"e1\",\"type\":\"payment_in\",\"webhook_xid\":\"wh_other\",\"payload\":{\"transferAmount\":100000,\"code\":\"SIBLING\"}}\n\n",
		"event: webhook\ndata: {\"id\":\"e1\",\"type\":\"payment_in\",\"webhook_xid\":\"wh_mine\",\"payload\":{\"transferAmount\":100000,\"code\":\"MINE\"}}\n\n",
	))
	defer hub.Close()

	apiSrv := httptest.NewServer(apiForTest(t, hub.URL, nil))
	defer apiSrv.Close()

	f, _, errOut := factoryForAPI(apiSrv.URL)
	err := run(NewCmdWebhook(f), "test", "--forward", handler.URL, "--bank-account", "ba_1",
		"--amount", "100000", "--webhook", "wh_mine", "--expect-status", "200")
	require.NoError(t, err)

	assert.Contains(t, string(gotBody), "MINE")
	assert.NotContains(t, string(gotBody), "SIBLING")
	assert.Contains(t, errOut.String(), "handler responded 200")
}

func TestTest_WebhookFlagTimeoutNamesSkippedSiblings(t *testing.T) {
	handler := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "a sibling webhook's delivery must not be forwarded")
	}))
	defer handler.Close()

	hub := httptest.NewServer(sseHub(t,
		"event: ready\ndata: {}\n\n",
		"event: webhook\ndata: {\"id\":\"e1\",\"type\":\"payment_in\",\"webhook_xid\":\"wh_other\",\"payload\":{\"transferAmount\":100000}}\n\n",
	))
	defer hub.Close()

	apiSrv := httptest.NewServer(apiForTest(t, hub.URL, nil))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := run(cmd, "test", "--forward", handler.URL, "--bank-account", "ba_1",
		"--amount", "100000", "--webhook", "wh_mine", "--timeout", "300ms")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wh_mine")
	assert.Contains(t, err.Error(), "1 delivery(ies) for other webhooks")
}

func TestTest_ForwardsTheSignedRawBody(t *testing.T) {
	var gotBody []byte
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()

	// Same data, different bytes, as the hub's two encodings give.
	hub := httptest.NewServer(sseHub(t,
		"event: ready\ndata: {}\n\n",
		"event: webhook\ndata: {\"id\":\"e1\",\"type\":\"payment_in\",\"webhook_xid\":\"wh_1\","+
			"\"payload\":{\"transferAmount\":100000,\"code\":\"A\"},"+
			"\"raw_body\":\"{\\\"code\\\":\\\"A\\\",\\\"transferAmount\\\":100000}\"}\n\n",
	))
	defer hub.Close()

	apiSrv := httptest.NewServer(apiForTest(t, hub.URL, nil))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	require.NoError(t, run(NewCmdWebhook(f), "test", "--forward", handler.URL,
		"--bank-account", "ba_1", "--amount", "100000"))

	assert.Equal(t, `{"code":"A","transferAmount":100000}`, string(gotBody)) //nolint:testifylint // raw_body is forwarded byte for byte, key order included
}
