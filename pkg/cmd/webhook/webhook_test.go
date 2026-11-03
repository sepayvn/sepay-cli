package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testFactory(t *testing.T, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(srv.URL, "test")
		c.MaxRetries = 0
		return c, nil
	}
	return f, out, errOut
}

func run(cmd *cobra.Command, args ...string) error {
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

const oneWebhookList = `{"status":"success","data":[{"id":"wh_1","name":"Shop","event_type":"All","authen_type":"No_Authen","webhook_url":"https://x.test/hook","is_verify_payment":false,"skip_if_no_code":true,"only_va":false,"bank_mode":"single","bank_account_xid":"ba_1","bank_sub_account_xids":[],"prefix_filters":[],"alert_threshold":null,"active":true,"created_at":"2026-07-15 10:00:00"}],"meta":{"pagination":{"total":1,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`

func TestList_Table(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(oneWebhookList))
	})
	require.NoError(t, run(NewCmdWebhook(f), "list"))
	s := out.String()
	assert.Contains(t, s, "wh_1")
	assert.Contains(t, s, "https://x.test/hook")
	assert.Contains(t, s, "single")
	assert.Contains(t, s, "yes") // active
}

func TestList_JSONPreservesTypes(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(oneWebhookList))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdWebhook(f), "list"))
	s := out.String()
	assert.Contains(t, s, `"active": true`)          // real bool
	assert.Contains(t, s, `"alert_threshold": null`) // nullable int
	assert.Contains(t, s, `"bank_account_xid": "ba_1"`)
}

func TestGet_MasksSecretsInJSON(t *testing.T) {
	body := `{"status":"success","data":{"id":"wh_1","name":"Shop","event_type":"All","authen_type":"Api_Key","webhook_url":"https://x.test","is_verify_payment":false,"skip_if_no_code":false,"only_va":false,"bank_mode":"all","bank_account_xid":null,"bank_sub_account_xids":[],"prefix_filters":[],"alert_threshold":null,"active":true,"created_at":"2026-07-15 10:00:00","api_key":"secret123","secret_key":"hmac456","oauth2_client_secret":"cs789","request_content_type":"Json"}}`
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdWebhook(f), "get", "wh_1"))
	s := out.String()
	assert.NotContains(t, s, "secret123")
	assert.NotContains(t, s, "hmac456")
	assert.NotContains(t, s, "cs789")
	assert.Contains(t, s, `"api_key": "***"`)
	assert.Contains(t, s, `"secret_key": "***"`)
	assert.Contains(t, s, `"oauth2_client_secret": "***"`)
}

const logRow = `{"transaction_id":"aaaaaaaa-0000-4000-8000-000000000001","webhook_xid":"cccccccc-0000-4000-8000-000000000001","replayable":true,"status":"failed","type":"transaction","is_replay":false,"request_method":"POST","request_url":"https://x.test/hook","connect_success":true,"response_status_code":500,"response_time_ms":123,"error_message":null,"error_code":null,"created_at":"2026-07-15 10:00:00"}`

func logsPage(rows ...string) string {
	return `{"status":"success","data":[` + strings.Join(rows, ",") + `],"meta":{"pagination":{"total":` +
		strconv.Itoa(len(rows)) + `,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`
}

func TestLogs_Table(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(logsPage(logRow)))
	})
	require.NoError(t, run(NewCmdWebhook(f), "logs"))
	s := out.String()
	assert.Contains(t, s, "TRANSACTION")
	assert.Contains(t, s, "aaaaaaaa-0000-4000-8000-000000000001")
	assert.Contains(t, s, "cccccccc-0000-4000-8000-000000000001", "the webhook xid is whole, so it pastes into webhook get")
	assert.Contains(t, s, "failed")
	assert.Contains(t, s, "500")
}

// --json is a public contract: no log row id, the replay pair and replayable on every row.
func TestLogs_JSONShape(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(logsPage(logRow, `{"transaction_id":null,"webhook_xid":null,"replayable":false,"status":"success","type":"transaction","is_replay":false,"request_method":"POST","request_url":"https://shop.test/sapo","connect_success":true,"response_status_code":200,"response_time_ms":9,"error_message":null,"error_code":null,"created_at":"2026-07-15 09:00:00"}`)))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdWebhook(f), "logs"))
	assert.JSONEq(t, `[
	  {"transaction_id":"aaaaaaaa-0000-4000-8000-000000000001","webhook_xid":"cccccccc-0000-4000-8000-000000000001","replayable":true,
	   "status":"failed","type":"transaction","is_replay":false,"request_method":"POST","request_url":"https://x.test/hook",
	   "connect_success":true,"response_status_code":500,"response_time_ms":123,"error_message":null,"error_code":null,
	   "created_at":"2026-07-15 10:00:00"},
	  {"transaction_id":null,"webhook_xid":null,"replayable":false,
	   "status":"success","type":"transaction","is_replay":false,"request_method":"POST","request_url":"https://shop.test/sapo",
	   "connect_success":true,"response_status_code":200,"response_time_ms":9,"error_message":null,"error_code":null,
	   "created_at":"2026-07-15 09:00:00"}
	]`, out.String())
}

func TestLogs_TransactionAndTimeFilters(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want url.Values
	}{
		{"transaction", []string{"--transaction", "aaaa-1"}, url.Values{"transaction_id": {"aaaa-1"}}},
		{"until date", []string{"--until", "2026-11-01"}, url.Values{"until": {"2026-11-01"}}},
		{"until datetime", []string{"--until", "2026-11-01 18:30"}, url.Values{"until": {"2026-11-01 18:30"}}},
		{"since date", []string{"--since", "2026-10-30"}, url.Values{"since": {"2026-10-30"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got url.Values
			f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query()
				_, _ = w.Write([]byte(logsPage()))
			})
			require.NoError(t, run(NewCmdWebhook(f), append([]string{"logs"}, tc.args...)...))
			for k, v := range tc.want {
				assert.Equal(t, v, got[k], k)
			}
		})
	}
}

func TestLogs_RejectsMalformedTimesBeforeAnyRequest(t *testing.T) {
	for _, args := range [][]string{
		{"--until", "yesterday"},
		{"--until", "24h"},
		{"--since", "2026-13-40"},
		{"--since", "-1h"},
		{"--transaction", " "},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) { t.Error("no request should be made") })
			err := run(NewCmdWebhook(f), append([]string{"logs"}, args...)...)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

// A skipped row carries no status code and is not a failure.
func TestLogs_SkippedStatusIsAccepted(t *testing.T) {
	var q string
	f, out, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"status":"success","data":[{"id":"log_3","webhook_xid":"wh_1","status":"skipped","type":"payment_in","is_replay":false,"request_method":null,"request_url":null,"connect_success":false,"response_status_code":null,"response_time_ms":null,"error_message":null,"error_code":null,"created_at":"2026-07-15 10:00:00"}],"meta":{"pagination":{"total":1,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "logs", "--status", "skipped"))
	assert.Contains(t, q, "status=skipped")
	assert.Contains(t, out.String(), "skipped")
}

func TestLogs_JSONCarriesErrorCode(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":[{"id":"log_1","webhook_xid":"wh_1","status":"failed","type":"payment_in","is_replay":false,"request_method":"POST","request_url":"https://x.test","connect_success":false,"response_status_code":null,"response_time_ms":null,"error_message":"connect timeout","error_code":28,"created_at":"2026-07-15 10:00:00"}],"meta":{"pagination":{"total":1,"per_page":20,"current_page":1,"last_page":1,"has_more":false}}}`))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdWebhook(f), "logs"))
	assert.Contains(t, out.String(), `"error_code": 28`)
}

func TestLogs_QueryParams(t *testing.T) {
	var q string
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"status":"success","data":[],"meta":{"pagination":{"total":0,"per_page":20,"current_page":1,"last_page":0,"has_more":false}}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "logs", "--webhook", "wh_1", "--status", "failed", "--per-page", "5"))
	assert.Contains(t, q, "webhook_xid=wh_1")
	assert.Contains(t, q, "status=failed")
	assert.Contains(t, q, "per_page=5")
}

// An unfiltered page would read as "nothing delivered in the window".
func TestLogs_NonPositiveSinceIsRejected(t *testing.T) {
	for _, arg := range []string{"-24h", "0"} {
		t.Run(arg, func(t *testing.T) {
			called := false
			f, _, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
				called = true
				_, _ = w.Write([]byte(`{"status":"success","data":[],"meta":{"pagination":{"total":0,"per_page":20,"current_page":1,"last_page":0,"has_more":false}}}`))
			})
			err := run(NewCmdWebhook(f), "logs", "--since", arg)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "--since must be a positive duration")
			assert.False(t, called, "no request may leave the machine")
		})
	}
}

func TestLogs_PositiveSinceStillFilters(t *testing.T) {
	var q string
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"status":"success","data":[],"meta":{"pagination":{"total":0,"per_page":20,"current_page":1,"last_page":0,"has_more":false}}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "logs", "--since", "24h"))
	assert.Contains(t, q, "since=")
}

func TestLogs_OmittedSinceSendsNoWindow(t *testing.T) {
	var q string
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"status":"success","data":[],"meta":{"pagination":{"total":0,"per_page":20,"current_page":1,"last_page":0,"has_more":false}}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "logs"))
	assert.NotContains(t, q, "since=")
}

const replayOK = `{"status":"success","data":{"delivered":true,"message":"Đã gửi lại","log":` + `{"transaction_id":"tx-1","webhook_xid":"wh-1","replayable":true,"status":"success","type":"transaction","is_replay":true,"request_method":"POST","request_url":"https://x.test/hook","connect_success":true,"response_status_code":200,"response_time_ms":88,"error_message":null,"error_code":null,"created_at":"2026-11-01 12:30:00"}` + `}}`

func TestReplay_PostsThePairWithIdempotencyKey(t *testing.T) {
	var method, path, key string
	var body map[string]any
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, key = r.Method, r.URL.Path, r.Header.Get("X-Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(replayOK))
	})
	f.Global.Yes = true
	require.NoError(t, run(NewCmdWebhook(f), "replay", "--transaction", "tx-1", "--webhook", "wh-1"))
	assert.Equal(t, http.MethodPost, method)
	assert.Equal(t, "/webhook-logs/replay", path)
	assert.NotEmpty(t, key)
	assert.Equal(t, map[string]any{"transaction_id": "tx-1", "webhook_xid": "wh-1"}, body)
	assert.Contains(t, errOut.String(), "handler answered 200 in 88ms")
	assert.NotContains(t, errOut.String(), "Đã gửi lại", "a server confirmation is not relayed as the success line")
}

func TestReplay_TransactionAloneResolvesItsWebhook(t *testing.T) {
	var logsQuery url.Values
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			logsQuery = r.URL.Query()
			row := strings.NewReplacer("aaaaaaaa-0000-4000-8000-000000000001", "tx-1").Replace(logRow)
			_, _ = w.Write([]byte(logsPage(row, row))) // a retry of the same delivery
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(replayOK))
	})
	f.Global.Yes = true
	require.NoError(t, run(NewCmdWebhook(f), "replay", "tx-1"))
	assert.Equal(t, "tx-1", logsQuery.Get("transaction_id"))
	assert.Equal(t, "cccccccc-0000-4000-8000-000000000001", body["webhook_xid"])
}

func TestReplay_SeveralWebhooksNonInteractiveIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("nothing may be replayed without a webhook")
		}
		a := strings.Replace(logRow, "cccccccc-0000-4000-8000-000000000001", "wh-a", 1)
		b := strings.Replace(logRow, "cccccccc-0000-4000-8000-000000000001", "wh-b", 1)
		_, _ = w.Write([]byte(logsPage(a, b)))
	})
	f.Global.Yes = true
	err := run(NewCmdWebhook(f), "replay", "aaaaaaaa-0000-4000-8000-000000000001")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "wh-a, wh-b")
}

func TestReplay_NoTransactionNonInteractiveIsUsageError(t *testing.T) {
	for _, args := range [][]string{{"replay"}, {"replay", "--webhook", "wh-1"}, {"replay", "--transaction", " "}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("no request should be made")
			})
			f.Global.Yes = true // even with --yes, a non-TTY has no delivery to pick
			err := run(NewCmdWebhook(f), args...)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

func TestReplay_PickerSkipsUnreplayableRows(t *testing.T) {
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/webhook-logs", r.URL.Path)
		skipped := strings.Replace(logRow, `"replayable":true`, `"replayable":false`, 1)
		other := strings.Replace(logRow, `"webhook_xid":"cccccccc-0000-4000-8000-000000000001"`, `"webhook_xid":null`, 1)
		_, _ = w.Write([]byte(logsPage(skipped, other)))
	})
	f.IOStreams.SetStdinTTY(true)
	f.IOStreams.SetStdoutTTY(true)
	err := run(NewCmdWebhook(f), "replay")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "none of the 2 most recent logs can be replayed")
}

func TestReplay_NotDispatchedRelaysTheReasonOnce(t *testing.T) {
	var posts int
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"replay_not_dispatched","message":"Webhook đang tắt"}`))
	})
	f.Global.Yes = true
	err := run(NewCmdWebhook(f), "replay", "--transaction", "tx-1", "--webhook", "wh-1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
	assert.Equal(t, 1, posts)
	ios, _, _, errOut := iostreams.Test()
	cmdutil.HandleError(&cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}}, err)
	assert.Contains(t, errOut.String(), "the replay was not sent (Reason: Webhook đang tắt)")
}

func TestReplay_HandlerFailureIsReportedNotAnError(t *testing.T) {
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.NewReplacer(`"delivered":true`, `"delivered":false`, `"response_status_code":200`, `"response_status_code":500`).Replace(replayOK)))
	})
	f.Global.Yes = true
	require.NoError(t, run(NewCmdWebhook(f), "replay", "--transaction", "tx-1", "--webhook", "wh-1"))
	assert.Contains(t, errOut.String(), "Replayed, but the handler answered 500")
}

func TestUpdate_NoFlagsNonInteractiveIsUsageError(t *testing.T) {
	var hits int
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) { hits++ })
	err := run(NewCmdWebhook(f), "update", "wh_1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "provide at least one field to update")
	assert.Zero(t, hits, "no request should be made before the editor is ruled out")
}

func TestUpdate_WithFlagSkipsTheEditorGET(t *testing.T) {
	var paths []string
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_1","name":"Shop","event_type":"All","authen_type":"No_Authen","webhook_url":"https://x.test/hook","bank_mode":"single","bank_account_xid":"ba_1","bank_sub_account_xids":[],"prefix_filters":[],"active":true,"created_at":"2026-07-15 10:00:00"}}`))
	})
	f.Global.Yes = true
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1", "--name", "Renamed"))
	// The trailing GET resolves a bank-account label.
	assert.Equal(t, "PUT /webhooks/wh_1", paths[0])
	assert.NotContains(t, paths, "GET /webhooks/wh_1", "the editor's read must not run")
}

// Update never passes --listen-only; the editor reads the stored webhook.
func TestAuthChoices_OAuth2WithheldFromListenOnly(t *testing.T) {
	values := func(cs []cmdutil.PromptChoice) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = c.Value
		}
		return out
	}
	assert.Equal(t, []string{"none", "apikey", "hmac", "oauth2"}, values(authChoices(false)))
	assert.Equal(t, []string{"none", "apikey", "hmac"}, values(authChoices(true)))

	assert.True(t, Webhook{DeliveryMode: deliveryModeListen}.listenOnly())
	assert.False(t, Webhook{WebhookURL: "https://x.test"}.listenOnly())
}

// The API would store the webhook without the secret and say nothing.
func TestAuthFieldsUnused_CoverEverySchemeAndField(t *testing.T) {
	all := []string{"secret", "oauth2_client_id", "oauth2_client_secret", "oauth2_access_token_url"}
	for _, scheme := range []string{"none", "apikey", "hmac", "oauth2"} {
		unused, ok := authFieldsUnused[scheme]
		require.True(t, ok, "no unused-field list for %q", scheme)
		for _, f := range unused {
			assert.Contains(t, all, f, "%q lists an unknown body key %q", scheme, f)
		}
	}
	assert.ElementsMatch(t, all, authFieldsUnused["none"], "none uses nothing, so it clears everything")
	assert.NotContains(t, authFieldsUnused["hmac"], "secret", "hmac keeps a secret passed by flag")
	assert.NotContains(t, authFieldsUnused["apikey"], "secret")
	assert.Contains(t, authFieldsUnused["oauth2"], "secret", "oauth2 has its own secret field")
	assert.NotContains(t, authFieldsUnused["oauth2"], "oauth2_client_secret")

	for _, c := range authChoices(false) {
		assert.Contains(t, authFieldsUnused, c.Value, "picker offers %q with no unused-field list", c.Value)
	}
}

func TestClearUnusedAuthFields_ClosesTheSilentDrop(t *testing.T) {
	fromFlags := func() map[string]any {
		return map[string]any{
			"url":                     "https://example.com/x",
			"bank_mode":               "all",
			"secret":                  "s3cret",
			"oauth2_client_id":        "cid",
			"oauth2_client_secret":    "csec",
			"oauth2_access_token_url": "https://t.test",
		}
	}

	none := fromFlags()
	clearUnusedAuthFields(none, "none")
	assert.NotContains(t, none, "secret", "a secret none cannot use must not be sent")
	assert.NotContains(t, none, "oauth2_client_secret")
	assert.Equal(t, "https://example.com/x", none["url"], "non-auth fields untouched")
	assert.Equal(t, "all", none["bank_mode"])

	// Enter on the no-echo prompt means "keep the one I passed".
	hmac := fromFlags()
	clearUnusedAuthFields(hmac, "hmac")
	assert.Equal(t, "s3cret", hmac["secret"])
	assert.NotContains(t, hmac, "oauth2_client_id")

	oauth := fromFlags()
	clearUnusedAuthFields(oauth, "oauth2")
	assert.NotContains(t, oauth, "secret")
	assert.Equal(t, "csec", oauth["oauth2_client_secret"])
	assert.Equal(t, "cid", oauth["oauth2_client_id"])
}

func TestNormalizePrefixes(t *testing.T) {
	for _, tt := range []struct {
		name, in, want string
		wantErr        bool
	}{
		{name: "trims around the comma", in: "SEVQR, ORDER", want: "SEVQR,ORDER"},
		{name: "drops empty entries", in: "SEVQR,,  ,DH", want: "SEVQR,DH"},
		{name: "blank stays blank", in: "   ", want: ""},
		{name: "a quote is refused", in: `SEVQR,OR"DER`, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizePrefixes(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEditableFields_BlankClearsOnlyWhereMeant(t *testing.T) {
	clears := map[string]bool{}
	for _, fd := range editableFields() {
		if fd.flag != "" {
			clears[fd.flag] = fd.blankClears
		}
	}
	assert.True(t, clears["name"])
	assert.True(t, clears["prefix-filter"])
	assert.False(t, clears["url"], "a blank URL would strip the webhook's destination")
	assert.False(t, clears["event-type"])
}

func TestEditableFields_PromptLabelsAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, fd := range editableFields() {
		if fd.ask == nil {
			continue
		}
		label := fd.promptLabel()
		assert.NotContains(t, seen, label, "duplicate prompt label %q", label)
		seen[label] = fd.flag
	}
	assert.Equal(t, "Name (blank to clear)", promptLabelOf(t, "name"))
	assert.Equal(t, "Destination URL", promptLabelOf(t, "url"))
}

func promptLabelOf(t *testing.T, flag string) string {
	t.Helper()
	for _, fd := range editableFields() {
		if fd.flag == flag {
			return fd.promptLabel()
		}
	}
	t.Fatalf("no editable field for --%s", flag)
	return ""
}

func TestTargetingSummary(t *testing.T) {
	ba := "ba_1"
	for _, tt := range []struct {
		name string
		in   Webhook
		want string
	}{
		{"single names the account", Webhook{BankMode: "single", BankAccountXID: &ba}, "single · ba_1"},
		{"single with no account", Webhook{BankMode: "single"}, "single"},
		{"multi counts them", Webhook{BankMode: "multi", BankAccountXIDs: []string{"a", "b"}}, "multi · 2 accounts"},
		{"all needs no detail", Webhook{BankMode: "all"}, "all"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, targetingSummary(tt.in))
		})
	}
}

func TestDelete_ConfirmedWithYes(t *testing.T) {
	var method, path string
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	f.Global.Yes = true
	require.NoError(t, run(NewCmdWebhook(f), "delete", "wh_1"))
	assert.Equal(t, http.MethodDelete, method)
	assert.Equal(t, "/webhooks/wh_1", path)
	assert.Contains(t, errOut.String(), "Deleted webhook wh_1")
}

func TestDelete_NeedsConfirmNonInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not delete without confirmation")
	})
	err := run(NewCmdWebhook(f), "delete", "wh_1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestCreate_SingleTargeting(t *testing.T) {
	var method, path, key string
	var body map[string]any
	f, out, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bank-accounts/ba_1" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"id":"ba_1","account_number":"0000000001","bank_short_name":"ACB","account_holder_name":"NGUYEN XUAN L","label":"sandbox","active":1}}`))
			return
		}
		method, path, key = r.Method, r.URL.Path, r.Header.Get("X-Idempotency-Key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9","name":"n","event_type":"All","authen_type":"No_Authen","webhook_url":"https://x.test","is_verify_payment":false,"skip_if_no_code":false,"only_va":false,"bank_mode":"single","bank_account_xid":"ba_1","bank_sub_account_xids":[],"prefix_filters":[],"alert_threshold":null,"active":true,"created_at":"2026-07-15 10:00:00"}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-account", "ba_1"))
	assert.Equal(t, http.MethodPost, method)
	assert.Equal(t, "/webhooks", path)
	assert.NotEmpty(t, key)
	assert.Equal(t, "https://x.test", body["url"])
	assert.Equal(t, "single", body["bank_mode"])
	assert.Equal(t, "ba_1", body["bank_account_xid"])
	assert.Contains(t, errOut.String(), "Created webhook wh_9")
	assert.Contains(t, out.String(), "ACB") // detail resolves the xid to a friendly label
}

func TestCreate_NameFlagSent(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9","name":"Shop","bank_mode":"all","prefix_filters":[],"active":true,"created_at":"x"}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-mode", "all", "--name", "Shop"))
	assert.Equal(t, "Shop", body["name"])
}

func TestCreate_NoNameNonInteractiveOmitsName(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9","bank_mode":"all","prefix_filters":[],"active":true,"created_at":"x"}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-mode", "all"))
	_, hasName := body["name"]
	assert.False(t, hasName, "name must not be sent when unset and non-interactive")
}

func TestCreate_SecretFlagWarns(t *testing.T) {
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9","bank_mode":"all","prefix_filters":[],"active":true,"created_at":"x"}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-mode", "all", "--auth-type", "apikey", "--secret", "s3cret"))
	assert.Contains(t, errOut.String(), "--secret passed inline is visible in shell history")
}

func TestCreate_AllMode(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9","bank_mode":"all","bank_account_xid":null,"bank_sub_account_xids":[],"prefix_filters":[],"active":true,"created_at":"x"}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-mode", "all"))
	assert.Equal(t, "all", body["bank_mode"])
	_, hasSingle := body["bank_account_xid"]
	assert.False(t, hasSingle)
}

func TestCreate_MultiTargeting(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9","bank_mode":"multi","bank_sub_account_xids":[],"prefix_filters":[],"active":true,"created_at":"x"}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-account", "ba_1", "--bank-account", "ba_2"))
	assert.Equal(t, "multi", body["bank_mode"])
	xids, ok := body["bank_account_xids"].([]any)
	require.True(t, ok)
	assert.Len(t, xids, 2)
}

func TestCreate_MultiNoAccountsIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request should be sent: %s %s", r.Method, r.URL.Path)
	})
	err := run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-mode", "multi")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "at least one")
}

func TestCreate_TargetingConflictRejected(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"single with two banks", []string{"create", "--url", "https://x.test", "--bank-mode", "single", "--bank-account", "ba_1", "--bank-account", "ba_2"}},
		{"all with a bank", []string{"create", "--url", "https://x.test", "--bank-mode", "all", "--bank-account", "ba_1"}},
		{"all with only-va", []string{"create", "--url", "https://x.test", "--bank-mode", "all", "--only-va"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not call the server on a targeting conflict")
			})
			err := run(NewCmdWebhook(f), tt.args...)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

func TestCreate_RejectsBadEnum(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server on a bad enum value")
	})
	err := run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-mode", "all", "--auth-type", "bogus")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestCreate_RejectsMalformedURL(t *testing.T) {
	for _, dest := range []string{"test", "example.com/hook", "ftp://example.com/hook"} {
		t.Run(dest, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not call the server with a URL the API is certain to refuse")
			})
			err := run(NewCmdWebhook(f), "create", "--url", dest, "--bank-mode", "all")
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

func TestCreate_RejectsMalformedOAuth2TokenURL(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server with a malformed token url")
	})
	err := run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-mode", "all",
		"--auth-type", "oauth2", "--oauth2-client-id", "id", "--oauth2-client-secret", "s", "--oauth2-token-url", "token")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestCreate_MissingURLNonInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server without a url")
	})
	err := run(NewCmdWebhook(f), "create", "--bank-mode", "all")
	require.Error(t, err)
	// The transport picker is TTY-only.
	assert.Contains(t, err.Error(), "--url is required")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestUpdate_SendsOnlyChanged(t *testing.T) {
	var method string
	var body map[string]any
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_1","name":"new","bank_mode":"all","bank_sub_account_xids":[],"prefix_filters":[],"active":true,"created_at":"x"}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1", "--name", "new"))
	assert.Equal(t, http.MethodPut, method)
	assert.Equal(t, "new", body["name"])
	_, hasURL := body["url"]
	assert.False(t, hasURL, "unchanged url must not be sent")
	_, hasMode := body["bank_mode"]
	assert.False(t, hasMode, "untouched targeting must not be sent")
	assert.Contains(t, errOut.String(), "Updated webhook wh_1")
}

func TestUpdate_NoFieldsIsUsageError(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server with no fields")
	})
	err := run(NewCmdWebhook(f), "update", "wh_1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestCreate_BankConfigPerAccount(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9","bank_mode":"multi","bank_sub_account_xids":[],"prefix_filters":[],"active":true,"created_at":"x"}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test",
		"--bank-account", "ba_A", "--bank-account", "ba_B",
		"--bank-config", "ba_A=on:all", "--bank-config", "ba_B=off:list"))

	cfg, ok := body["bank_config"].(map[string]any)
	require.True(t, ok)
	a := cfg["ba_A"].(map[string]any)
	assert.Equal(t, true, a["receive_primary"])
	assert.Equal(t, "all", a["va_mode"])
	b := cfg["ba_B"].(map[string]any)
	assert.Equal(t, false, b["receive_primary"])
	assert.Equal(t, "list", b["va_mode"])
}

// Last-wins would store "on:all" as "off:none" silently.
func TestCreate_BankConfigRejectsARepeatedAccount(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not create a webhook from a contradictory --bank-config")
	})

	err := run(NewCmdWebhook(f), "create", "--url", "https://x.test",
		"--bank-mode", "single", "--bank-account", "ba_A",
		"--bank-config", "ba_A=on:all", "--bank-config", "ba_A=off:none")

	require.Error(t, err)
	assert.EqualError(t, err, "--bank-config lists ba_A twice; give each bank account one config")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestCreate_BankConfigInvalid(t *testing.T) {
	for _, bad := range []string{"ba_A", "ba_A=maybe:all", "ba_A=on:everything", "=on:all"} {
		f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
			t.Error("must not call the server with a bad --bank-config")
		})
		err := run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-mode", "all", "--bank-config", bad)
		require.Error(t, err, bad)
		assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), bad)
	}
}

func TestWrite_OnlyVAIsRefusedOnlyWhereItIsANoOp(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		refused bool
	}{
		{
			name: "multi with no sub-account changes nothing",
			args: []string{"create", "--url", "https://x.test", "--bank-mode", "multi",
				"--bank-account", "ba_1", "--bank-account", "ba_2", "--only-va"},
			refused: true,
		},
		{
			name: "multi with a sub-account narrows delivery",
			args: []string{"create", "--url", "https://x.test", "--bank-mode", "multi",
				"--bank-account", "ba_1", "--only-va", "--sub-account", "va_1"},
		},
		{
			name: "single with no sub-account still turns the account number off",
			args: []string{"create", "--url", "https://x.test", "--bank-account", "ba_1", "--only-va"},
		},
		{
			name: "an update with no targeting flag leaves the mode to the server",
			args: []string{"update", "wh_1", "--only-va"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			f, _, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
				called = true
				_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_1","bank_mode":"single",` +
					`"bank_sub_account_xids":[],"prefix_filters":[],"only_va":true,"active":true,"created_at":"x"}}`))
			})
			err := run(NewCmdWebhook(f), tc.args...)
			if !tc.refused {
				require.NoError(t, err)
				assert.True(t, called, "the server decides what only_va means for this mode")
				return
			}
			require.Error(t, err)
			assert.False(t, called, "must not send a no-op only_va")
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "--bank-config")
		})
	}
}

func TestUpdate_OnlyVAWithSubAccountIsSent(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_1","bank_mode":"single","bank_sub_account_xids":["va_1"],"prefix_filters":[],"only_va":true,"active":true,"created_at":"x"}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1", "--only-va", "--sub-account", "va_1"))
	assert.Equal(t, true, body["only_va"])
	assert.Equal(t, []any{"va_1"}, body["bank_sub_account_xids"])
}

func TestList_RejectsAnUnaccceptedActiveFilter(t *testing.T) {
	for _, bad := range []string{"false", "true", "2", "ZZZ"} {
		f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
			t.Error("must not call the server with an unaccepted --active")
		})
		err := run(NewCmdWebhook(f), "list", "--active", bad)
		require.Error(t, err, bad)
		assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), bad)
	}
}

func TestList_ActiveFilterIsForwarded(t *testing.T) {
	var got string
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("active")
		_, _ = w.Write([]byte(oneWebhookList))
	})
	require.NoError(t, run(NewCmdWebhook(f), "list", "--active", "0"))
	assert.Equal(t, "0", got)
}

func TestWebhook_SandboxOnlyVerbsSaySoInShort(t *testing.T) {
	f, _, _ := testFactory(t, func(http.ResponseWriter, *http.Request) {})
	for _, c := range NewCmdWebhook(f).Commands() {
		if cmdutil.Annotation(c, cmdutil.AnnEnvs) != "sandbox" {
			continue
		}
		assert.Contains(t, c.Short, "(sandbox only)", c.Name())
	}
}

func TestUpdate_BankConfigOnly(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_1","bank_mode":"single","bank_sub_account_xids":[],"prefix_filters":[],"active":true,"created_at":"x"}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1", "--bank-config", "ba_A=off:none"))
	cfg, ok := body["bank_config"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "none", cfg["ba_A"].(map[string]any)["va_mode"])
}

func TestCreate_SecretFromStdinReachesTheBody(t *testing.T) {
	var body map[string]any
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9","bank_mode":"all","prefix_filters":[],"active":true,"created_at":"x"}}`))
	})
	in, ok := f.IOStreams.In.(*bytes.Buffer)
	require.True(t, ok)
	in.WriteString("piped-hmac-key\n")

	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test",
		"--bank-mode", "all", "--auth-type", "hmac", "--secret", "-"))
	assert.Equal(t, "piped-hmac-key", body["secret"])
	assert.NotContains(t, errOut.String(), "shell history")
}

func TestCreate_OAuth2ClientSecretFromStdinReachesTheBody(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9","bank_mode":"all","prefix_filters":[],"active":true,"created_at":"x"}}`))
	})
	in, ok := f.IOStreams.In.(*bytes.Buffer)
	require.True(t, ok)
	in.WriteString("piped-oauth-secret")

	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test",
		"--bank-mode", "all", "--auth-type", "oauth2",
		"--oauth2-client-id", "cid", "--oauth2-token-url", "https://x.test/token",
		"--oauth2-client-secret", "-"))
	assert.Equal(t, "piped-oauth-secret", body["oauth2_client_secret"])
	assert.Equal(t, "cid", body["oauth2_client_id"])
}

// --auth-type rides along: the API reads a secret only inside that branch.
func TestUpdate_SecretFromStdinReachesTheBody(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_1","bank_mode":"all","prefix_filters":[],"active":true,"created_at":"x"}}`))
	})
	f.Global.Yes = true
	in, ok := f.IOStreams.In.(*bytes.Buffer)
	require.True(t, ok)
	in.WriteString("rotated-key")

	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1", "--auth-type", "hmac", "--secret", "-"))
	assert.Equal(t, "rotated-key", body["secret"])
	assert.Equal(t, "hmac", body["auth_type"])
}

func TestWrite_SecretWithoutAuthTypeIsRefused(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"create", []string{"create", "--url", "https://x.test", "--bank-mode", "all", "--secret", "s3cret"}},
		{"update", []string{"update", "wh_1", "--secret", "s3cret"}},
		{"create oauth2 secret", []string{"create", "--url", "https://x.test", "--bank-mode", "all", "--oauth2-client-secret", "s3cret"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			f, _, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9","bank_mode":"all","prefix_filters":[],"active":true,"created_at":"x"}}`))
			})
			f.Global.Yes = true

			err := run(NewCmdWebhook(f), tc.args...)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "needs --auth-type")
			assert.False(t, reached, "nothing may be sent when the secret would be ignored")
		})
	}
}

func TestWrite_SecretWithAuthTypePasses(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9","bank_mode":"all","prefix_filters":[],"active":true,"created_at":"x"}}`))
	})
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test",
		"--bank-mode", "all", "--auth-type", "hmac", "--secret", "s3cret"))
	assert.Equal(t, "s3cret", body["secret"])
}

// A key that disappears when the answer is "no" is indistinguishable from an unsupported field.
func TestGet_JSONCarriesEveryServerField(t *testing.T) {
	const body = `{"status":"success","data":{` +
		`"id":"wh_1","name":"Shop","event_type":"All","authen_type":"No_Authen",` +
		`"webhook_url":"https://x.test/hook","delivery_mode":"http",` +
		`"is_verify_payment":false,"skip_if_no_code":true,"only_va":false,` +
		`"bank_mode":"all","bank_account_xid":null,"bank_sub_account_xids":[],` +
		`"prefix_filters":[],"retry_conditions":null,"alert_threshold":null,` +
		`"alert_event_types":null,"active":true,"managed_by_partner":false,` +
		`"created_at":"2026-07-15 10:00:00"}}`

	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdWebhook(f), "get", "wh_1"))

	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	for _, key := range []string{
		"id", "name", "event_type", "authen_type", "webhook_url", "delivery_mode",
		"is_verify_payment", "skip_if_no_code", "only_va", "bank_mode",
		"bank_account_xid", "bank_sub_account_xids", "prefix_filters",
		"retry_conditions", "alert_threshold", "alert_event_types", "active",
		"managed_by_partner", "created_at",
	} {
		assert.Contains(t, got, key, "the API sends %q, so --json must carry it", key)
	}
	assert.Nil(t, got["retry_conditions"])
	assert.Nil(t, got["alert_event_types"])
	assert.Equal(t, false, got["managed_by_partner"])
}

func TestGet_JSONReadsAlertAndRetryValues(t *testing.T) {
	const body = `{"status":"success","data":{"id":"wh_2","name":"Shop","event_type":"All",` +
		`"authen_type":"No_Authen","webhook_url":"https://x.test/hook","bank_mode":"all",` +
		`"bank_sub_account_xids":[],"prefix_filters":[],"active":1,` +
		`"retry_conditions":{"non_2xx_status_code":1},"alert_threshold":3,` +
		`"alert_event_types":["failure","recovery"],"managed_by_partner":true,` +
		`"created_at":"2026-07-15 10:00:00"}}`

	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdWebhook(f), "get", "wh_2"))

	s := out.String()
	assert.Contains(t, s, `"non_2xx_status_code": 1`)
	assert.Contains(t, s, `"failure"`)
	assert.Contains(t, s, `"recovery"`)
	assert.Contains(t, s, `"alert_threshold": 3`)
	assert.Contains(t, s, `"managed_by_partner": true`)
	assert.Contains(t, s, `"active": true`)
}
