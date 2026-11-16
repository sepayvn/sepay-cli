package webhook

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const dryXID = "11111111-2222-4333-8444-555555555555"

// Stored webhooks as the server's get returns them, one per auth type: each shows only its own
// credential column, and oauth2 no content type.
var storedByAuth = map[string]string{
	"none":   `"authen_type":"No_Authen","request_content_type":"Json"`,
	"apikey": `"authen_type":"Api_Key","api_key":"********","request_content_type":"application_x-www-form-urlencoded"`,
	"hmac":   `"authen_type":"HMAC_SHA256","secret_key":"********","request_content_type":"Json"`,
	"oauth2": `"authen_type":"OAuth2.0","oauth2_client_id":"cid","oauth2_client_secret":"********","oauth2_access_token_url":"https://auth.test/token"`,
}

func storedWebhook(auth, targeting string) string {
	if targeting == "" {
		targeting = `"bank_mode":"all","bank_account_xid":null,"bank_sub_account_xids":[]`
	}
	return `{"status":"success","data":{"id":"` + dryXID + `","name":"Shop","event_type":"All",` + storedByAuth[auth] +
		`,"webhook_url":"https://a.test/hook","delivery_mode":"http","is_verify_payment":false,"skip_if_no_code":false,"only_va":false,` +
		targeting + `,"prefix_filters":["DH"],"alert_threshold":null,"active":true,"created_at":"2026-07-15 10:00:00"}}`
}

// dryRunServer answers GET /webhooks/<xid> with stored and fails the test on any write.
func dryRunServer(t *testing.T, stored string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("dry run sent %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch {
		case r.URL.Path == "/webhooks/"+dryXID:
			_, _ = w.Write([]byte(stored))
		case strings.HasPrefix(r.URL.Path, "/transactions/"):
			_, _ = w.Write([]byte(replayTx))
		case r.URL.Path == "/webhook-logs":
			_, _ = w.Write([]byte(logsPage(logRow)))
		case strings.HasSuffix(r.URL.Path, "/va"):
			xid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/bank-accounts/"), "/va")
			_, _ = w.Write([]byte(`{"status":"success","data":` + reachVAs[xid] + `}`))
		case strings.HasPrefix(r.URL.Path, "/bank-accounts/"):
			_, _ = w.Write([]byte(`{"status":"success","data":` + reachAccounts[strings.TrimPrefix(r.URL.Path, "/bank-accounts/")] + `}`))
		default:
			http.NotFound(w, r)
		}
	}
}

type dryRunJSON struct {
	DryRun  bool `json:"dry_run"`
	Request struct {
		Method string         `json:"method"`
		Path   string         `json:"path"`
		Body   map[string]any `json:"body"`
	} `json:"request"`
	Before      map[string]any `json:"before"`
	After       map[string]any `json:"after"`
	NotChecked  []string       `json:"not_checked"`
	WouldRefuse []string       `json:"would_refuse"`
}

func runDryJSON(t *testing.T, stored string, args ...string) dryRunJSON {
	t.Helper()
	f, out, errOut := testFactory(t, dryRunServer(t, stored))
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdWebhook(f), append(args, "--dry-run")...))
	for _, s := range []string{cmdutil.DryRunNotice, "! not checked", "! live"} {
		assert.NotContains(t, errOut.String(), s, "only the real run's own warnings beside the object")
	}
	var got dryRunJSON
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
	return got
}

// changed lists the get keys whose value differs between before and after.
func changed(d dryRunJSON) map[string][2]any {
	out := map[string][2]any{}
	for k, b := range d.Before {
		bj, _ := json.Marshal(b)
		aj, _ := json.Marshal(d.After[k])
		if string(bj) != string(aj) {
			out[k] = [2]any{b, d.After[k]}
		}
	}
	for k, a := range d.After {
		if _, ok := d.Before[k]; !ok {
			out[k] = [2]any{nil, a}
		}
	}
	return out
}

// after is in the stored vocabulary the server answers with, so it can be compared with a get
// after the real run; before is the get itself.
func TestDryRunUpdate_AfterUsesTheStoredForms(t *testing.T) {
	cases := []struct {
		name  string
		auth  string
		args  []string
		want  map[string][2]any
		empty bool
	}{
		{"event type in", "none", []string{"--event-type", "in"}, map[string][2]any{"event_type": {"All", "In_only"}}, false},
		{"event type out", "hmac", []string{"--event-type", "out"}, map[string][2]any{"event_type": {"All", "Out_only"}}, false},
		{"content type form", "none", []string{"--content-type", "form"}, map[string][2]any{"request_content_type": {"Json", "application_x-www-form-urlencoded"}}, false},
		{"content type multipart on apikey", "apikey", []string{"--content-type", "multipart"}, map[string][2]any{"request_content_type": {"application_x-www-form-urlencoded", "multipart_form-data"}}, false},
		{"resending the stored event type changes nothing", "none", []string{"--event-type", "all"}, nil, true},
		{"resending the stored content type changes nothing", "hmac", []string{"--content-type", "json"}, nil, true},
		{"url", "oauth2", []string{"--url", "https://b.test/hook"}, map[string][2]any{"webhook_url": {"https://a.test/hook", "https://b.test/hook"}}, false},
		{"name and flags", "none", []string{"--name", "New", "--verify-payment", "--skip-if-no-code"}, map[string][2]any{
			"name": {"Shop", "New"}, "is_verify_payment": {false, true}, "skip_if_no_code": {false, true},
		}, false},
		{"prefix filter list", "none", []string{"--prefix-filter", "DH,HD"}, map[string][2]any{"prefix_filters": {[]any{"DH"}, []any{"DH", "HD"}}}, false},
		{"apikey to hmac sets the scheme and the secret it sends", "apikey", []string{"--auth-type", "hmac", "--secret", "s3cr3t"}, map[string][2]any{
			"authen_type": {"Api_Key", "HMAC_SHA256"},
			"secret_key":  {nil, "***"},
		}, false},
		{"auth change with a content type", "apikey", []string{"--auth-type", "hmac", "--secret", "s", "--content-type", "json"}, map[string][2]any{
			"authen_type":          {"Api_Key", "HMAC_SHA256"},
			"secret_key":           {nil, "***"},
			"request_content_type": {"application_x-www-form-urlencoded", "Json"},
		}, false},
		{"hmac to oauth2 sets only the fields it sends", "hmac", []string{"--auth-type", "oauth2", "--oauth2-client-id", "c", "--oauth2-client-secret", "x", "--oauth2-token-url", "https://auth.test/t"}, map[string][2]any{
			"authen_type":             {"HMAC_SHA256", "OAuth2.0"},
			"oauth2_client_id":        {nil, "c"},
			"oauth2_client_secret":    {nil, "***"},
			"oauth2_access_token_url": {nil, "https://auth.test/t"},
		}, false},
		{"apikey to oauth2 keeps the content type it sends", "apikey", []string{
			"--auth-type", "oauth2", "--oauth2-client-id", "c", "--oauth2-client-secret", "x", "--oauth2-token-url", "https://auth.test/t", "--content-type", "multipart",
		}, map[string][2]any{
			"authen_type":             {"Api_Key", "OAuth2.0"},
			"oauth2_client_id":        {nil, "c"},
			"oauth2_client_secret":    {nil, "***"},
			"oauth2_access_token_url": {nil, "https://auth.test/t"},
			"request_content_type":    {"application_x-www-form-urlencoded", "multipart_form-data"},
		}, false},
		{"oauth2 to none sets only the scheme", "oauth2", []string{"--auth-type", "none"}, map[string][2]any{
			"authen_type": {"OAuth2.0", "No_Authen"},
		}, false},
		{"content type on a stored oauth2 as sent", "oauth2", []string{"--content-type", "form"}, map[string][2]any{
			"request_content_type": {nil, "application_x-www-form-urlencoded"},
		}, false},
		{"oauth2 client id without --auth-type", "oauth2", []string{"--oauth2-client-id", "c2"}, map[string][2]any{
			"oauth2_client_id": {"cid", "c2"},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runDryJSON(t, storedWebhook(tc.auth, ""), append([]string{"update", dryXID}, tc.args...)...)
			assert.True(t, got.DryRun)
			assert.Equal(t, http.MethodPut, got.Request.Method)
			assert.Equal(t, "/webhooks/"+dryXID, got.Request.Path)
			if tc.empty {
				assert.Empty(t, changed(got))
				return
			}
			assert.Equal(t, tc.want, changed(got))
		})
	}
}

// After is before with each field the body sends, as sent: the targeting the server derives from
// it is not guessed, and the not_checked lines say so, the payment receiving setup included.
func TestDryRunUpdate_AfterIsWhatTheBodySends(t *testing.T) {
	const legacySingle = `"bank_mode":"single","bank_account_xid":"ba_both","bank_sub_account_xids":[]`
	const multi = `"bank_mode":"multi","bank_account_xid":null,"bank_account_xids":["ba_both","ba_sms"],` +
		`"bank_config":{"ba_both":{"receive_primary":true,"va_mode":"all"},"ba_sms":{"receive_primary":true,"va_mode":"all"}},"bank_sub_account_xids":[]`
	cases := []struct {
		name   string
		stored string
		args   []string
		want   map[string][2]any
	}{
		{"bank_config keeps the accounts it leaves out", storedWebhook("none", multi), []string{"--bank-config", "ba_both=off:all"}, map[string][2]any{"bank_config": {
			map[string]any{"ba_both": map[string]any{"receive_primary": true, "va_mode": "all"}, "ba_sms": map[string]any{"receive_primary": true, "va_mode": "all"}},
			map[string]any{"ba_both": map[string]any{"receive_primary": false, "va_mode": "all"}, "ba_sms": map[string]any{"receive_primary": true, "va_mode": "all"}},
		}}},
		{"bank_config on a webhook storing none", storedWebhook("none", legacySingle), []string{"--bank-config", "ba_both=off:all"}, map[string][2]any{"bank_config": {
			nil, map[string]any{"ba_both": map[string]any{"receive_primary": false, "va_mode": "all"}},
		}}},
		{"bank_mode all leaves the other fields", storedWebhook("none", multi), []string{"--bank-mode", "all"}, map[string][2]any{"bank_mode": {"multi", "all"}}},
		{"single", storedWebhook("none", multi), []string{"--bank-mode", "single", "--bank-account", "ba_both"}, map[string][2]any{
			"bank_mode": {"multi", "single"}, "bank_account_xid": {nil, "ba_both"},
		}},
		{"only_va as sent", storedWebhook("none", single), []string{"--only-va"}, map[string][2]any{"only_va": {false, true}}},
		{"name as sent", storedWebhook("none", ""), []string{"--name", " Shop #1 "}, map[string][2]any{"name": {"Shop", " Shop #1 "}}},
		{"listen-only keeps the stored url", storedWebhook("none", ""), []string{"--listen-only"}, map[string][2]any{"delivery_mode": {"http", "listen"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runDryJSON(t, tc.stored, append([]string{"update", dryXID}, tc.args...)...)
			assert.Equal(t, tc.want, changed(got))
			assert.Equal(t, []string{cmdutil.DryRunNotChecked, paymentSetupNotChecked}, got.NotChecked)
			assert.Empty(t, got.WouldRefuse, "only the CLI's own gates refuse")
		})
	}
}

func TestDryRunUpdate_TableListsOnlyChanges(t *testing.T) {
	f, out, errOut := testFactory(t, dryRunServer(t, storedWebhook("none", "")))
	require.NoError(t, run(NewCmdWebhook(f), "update", dryXID, "--name", "New", "--event-type", "all", "--dry-run"))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 2, out.String())
	assert.Equal(t, []string{"name", "Shop", "New"}, strings.Fields(lines[1]))
	assert.Equal(t, cmdutil.DryRunNotice+"\n! not checked: "+cmdutil.DryRunNotChecked+"\n! not checked: "+paymentSetupNotChecked+"\n", errOut.String())
}

const (
	single        = `"bank_mode":"single","bank_account_xid":"ba_both","bank_config":{"ba_both":{"receive_primary":true,"va_mode":"all"}},"bank_sub_account_xids":[]`
	targetingNote = "the account number and VA settings the server keeps or gives each account --bank-config does not set"
)

// No secret reaches either stream, whichever way it entered: inline, stdin, or the prompt.
func TestDryRun_SecretsNeverPrinted(t *testing.T) {
	const secret = "TOPSECRET-value"
	cases := []struct {
		name  string
		args  []string
		stdin bool
		steps []prompttest.Step
	}{
		{"inline --secret", []string{"update", dryXID, "--auth-type", "hmac", "--secret", secret}, false, nil},
		{"--secret from stdin", []string{"update", dryXID, "--auth-type", "apikey", "--secret", "-"}, true, nil},
		{"inline oauth2 client secret", []string{
			"update", dryXID, "--auth-type", "oauth2", "--oauth2-client-id", "c",
			"--oauth2-client-secret", secret, "--oauth2-token-url", "https://auth.test/t",
		}, false, nil},
		{"create with an inline secret", []string{"create", "--url", "https://b.test/hook", "--bank-mode", "all", "--auth-type", "hmac", "--secret", secret}, false, nil},
	}
	for _, tc := range cases {
		for _, output := range []string{"table", "json", "yaml"} {
			t.Run(tc.name+"/"+output, func(t *testing.T) {
				f, out, errOut := testFactory(t, dryRunServer(t, storedWebhook("hmac", "")))
				f.Global.Output = output
				if tc.stdin {
					f.IOStreams.In = strings.NewReader(secret)
				}
				require.NoError(t, run(NewCmdWebhook(f), append(tc.args, "--dry-run")...))
				assert.NotContains(t, out.String(), secret)
				assert.NotContains(t, errOut.String(), secret)
				if output != "table" || tc.args[0] == "update" {
					assert.Contains(t, out.String(), "***", "the preview says a secret is set")
				}
			})
		}
	}
}

func TestDryRun_SecretTypedAtThePromptNeverPrinted(t *testing.T) {
	const secret = "TOPSECRET-typed"
	f, out, errOut := testFactory(t, dryRunServer(t, storedWebhook("none", "")))
	prompttest.Use(t, f,
		prompttest.Step{Label: "Webhook name (optional)", Answer: ""},
		prompttest.Step{Label: "Authentication", Answer: "HMAC signature"},
		prompttest.Step{Label: "Shared secret", Answer: secret},
	)
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://b.test/hook", "--bank-mode", "all", "--dry-run"))
	assert.NotContains(t, out.String()+errOut.String(), secret)
}

// The table shows a secret the update sets even though both sides read "***".
func TestDryRunUpdate_TableShowsAReplacedSecret(t *testing.T) {
	for _, tc := range []struct {
		auth  string
		args  []string
		field string
	}{
		{"hmac", []string{"--auth-type", "hmac", "--secret", "new"}, "secret_key"},
		{"apikey", []string{"--auth-type", "apikey", "--secret", "new"}, "api_key"},
		{"oauth2", []string{"--auth-type", "oauth2", "--oauth2-client-id", "cid", "--oauth2-token-url", "https://auth.test/token", "--oauth2-client-secret", "new"}, "oauth2_client_secret"},
	} {
		t.Run(tc.auth, func(t *testing.T) {
			f, out, _ := testFactory(t, dryRunServer(t, storedWebhook(tc.auth, "")))
			require.NoError(t, run(NewCmdWebhook(f), append([]string{"update", dryXID}, append(tc.args, "--dry-run")...)...))
			assert.Regexp(t, `(?m)^`+tc.field+` +\*\*\* +\*\*\* \(new\)$`, out.String())
		})
	}
}

// The account and VA lists in after are the lists the body sends, whatever the server derives.
func TestDryRunUpdate_AfterListsAreTheBodysLists(t *testing.T) {
	for _, tc := range []struct {
		key  string
		args []string
	}{
		{"bank_account_xids", []string{"--bank-mode", "multi", "--bank-account", "ba_both", "--bank-account", "ba_plain"}},
		{"bank_sub_account_xids", []string{"--va-xid", "va_b2"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			got := runDryJSON(t, storedWebhook("none", single), append([]string{"update", dryXID}, tc.args...)...)
			require.NotNil(t, got.Request.Body[tc.key])
			assert.Equal(t, got.Request.Body[tc.key], got.After[tc.key])
			assert.NotEqual(t, got.Before[tc.key], got.After[tc.key])
		})
	}
}

// A bare update with only --dry-run is not a field change: without a terminal it is refused like
// the real run, on a terminal it opens the editor.
func TestDryRunUpdate_DryRunAloneIsNoField(t *testing.T) {
	f, _, _ := testFactory(t, dryRunServer(t, storedWebhook("none", "")))
	err := run(NewCmdWebhook(f), "update", dryXID, "--dry-run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provide at least one field to update")

	f, out, _ := testFactory(t, dryRunServer(t, storedWebhook("none", "")))
	prompttest.Use(t, f,
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Name (Shop)"}},
		prompttest.Step{Label: "Name (blank to clear)", Answer: "Renamed"},
	)
	require.NoError(t, run(NewCmdWebhook(f), "update", dryXID, "--dry-run"))
	assert.Contains(t, out.String(), "Renamed")
}

func TestDryRunDelete_ShowsTheWebhookItRemoves(t *testing.T) {
	got := runDryJSON(t, storedWebhook("apikey", ""), "delete", dryXID)
	assert.Equal(t, http.MethodDelete, got.Request.Method)
	assert.Equal(t, "/webhooks/"+dryXID, got.Request.Path)
	assert.Nil(t, got.Request.Body)
	assert.Equal(t, dryXID, got.Before["id"])
	assert.Equal(t, "***", got.Before["api_key"], "redacted like get")
	assert.Nil(t, got.After)
	assert.Equal(t, []string{cmdutil.DryRunNotChecked}, got.NotChecked, "only create and update add the payment receiving setup")

	f, out, errOut := testFactory(t, dryRunServer(t, storedWebhook("none", "")))
	require.NoError(t, run(NewCmdWebhook(f), "delete", dryXID, "--dry-run"))
	assert.Contains(t, out.String(), "https://a.test/hook", "the table is the webhook as get prints it")
	assert.Contains(t, errOut.String(), cmdutil.DryRunNotice)
	assert.Contains(t, errOut.String(), "Would delete webhook "+dryXID+".")
	assert.Contains(t, errOut.String(), "! the real run would refuse: delete webhook "+dryXID+"? requires confirmation; pass --yes",
		"a sandbox delete without --yes and no terminal stops at its confirm")
}

// After is the fields the create sends, in get's names and stored values; what the server fills in
// (id, created_at, its defaults) is left out.
func TestDryRunCreate_AfterIsWhatTheBodySends(t *testing.T) {
	got := runDryJSON(t, "", "create", "--url", "https://b.test/hook", "--bank-mode", "all", "--event-type", "in",
		"--auth-type", "apikey", "--secret", "s")
	assert.Equal(t, http.MethodPost, got.Request.Method)
	assert.Equal(t, "/webhooks", got.Request.Path)
	assert.Nil(t, got.Before)
	assert.Equal(t, "in", got.Request.Body["event_type"], "the body as sent, in the CLI's vocabulary")
	assert.Equal(t, map[string]any{
		"webhook_url": "https://b.test/hook", "bank_mode": "all", "event_type": "In_only", "authen_type": "Api_Key", "api_key": "***",
	}, got.After)
	assert.Equal(t, []string{cmdutil.DryRunNotChecked, paymentSetupNotChecked}, got.NotChecked)

	f, out, _ := testFactory(t, dryRunServer(t, ""))
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://b.test/hook", "--bank-mode", "all", "--dry-run"))
	assert.Equal(t, []string{"FIELD", "VALUE"}, strings.Fields(strings.SplitN(out.String(), "\n", 2)[0]))
	assert.Contains(t, out.String(), "webhook_url")
	assert.NotContains(t, out.String(), "created_at")
}

func TestDryRunReplay_Table(t *testing.T) {
	const notChecked = "! not checked: " + cmdutil.DryRunNotChecked
	cases := []struct {
		name       string
		env        string
		force, yes bool
		want       []string
		not        []string
	}{
		{"sandbox", config.EnvSandbox, false, false, []string{notChecked}, []string{"credit this order twice"}},
		{"production", config.EnvProduction, false, false, []string{notChecked, "credit this order twice"}, nil},
		{"production --force without a terminal", config.EnvProduction, true, false, []string{notChecked, "the real run would refuse: replay a webhook delivery on production needs confirmation"}, nil},
		{"production --force --yes without a terminal", config.EnvProduction, true, true, []string{notChecked, "the real run would refuse: --force on production"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, out, errOut := testFactory(t, dryRunServer(t, ""))
			f.Global.Env = tc.env
			f.Global.Yes = tc.yes
			args := []string{"replay", "tx-1", "--webhook", "wh-1", "--dry-run"}
			if tc.force {
				args = append(args, "--force")
			}
			require.NoError(t, run(NewCmdWebhook(f), args...))
			assert.Contains(t, out.String(), "Transaction  tx-1")
			assert.Contains(t, out.String(), "Amount       +2.500.000")
			for _, s := range tc.want {
				assert.Contains(t, errOut.String(), s)
			}
			for _, s := range tc.not {
				assert.NotContains(t, errOut.String(), s)
			}
			assert.NotContains(t, errOut.String(), "Type tx-1 to confirm")
		})
	}
}

func TestDryRunReplay_JSON(t *testing.T) {
	f, out, errOut := testFactory(t, dryRunServer(t, ""))
	f.Global.Env = config.EnvProduction
	f.Global.Output = "json"
	f.Global.Yes = true
	require.NoError(t, run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1", "--force", "--dry-run"))
	assert.Empty(t, errOut.String())
	var got dryRunJSON
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, "/webhook-logs/replay", got.Request.Path)
	assert.Equal(t, map[string]any{"transaction_id": "tx-1", "webhook_xid": "wh-1", "force": true}, got.Request.Body)
	assert.Equal(t, map[string]any{
		"transaction_id": "tx-1", "amount": "+2.500.000", "sent_to": "https://x.test/hook",
		"last_result": "failed · HTTP 500 · 2026-07-15 10:00:00",
	}, got.Before)
	assert.Nil(t, got.After)
	assert.NotContains(t, out.String(), "\x1b[", "no color in JSON")
	assert.Equal(t, []string{"--force on production must be confirmed on a terminal, and --yes does not confirm it; ask the user to run this command in their own terminal"}, got.WouldRefuse,
		"with --yes the real run passes the production-write rule and stops at the --force check")
}

// An empty secret beside a scheme with no credential sets nothing, so it is not shown as one about to be stored.
func TestDryRun_EmptySecretIsNotMasked(t *testing.T) {
	got := runDryJSON(t, storedWebhook("apikey", ""), "update", dryXID, "--auth-type", "none", "--secret", "")
	assert.Empty(t, got.Request.Body["secret"])
	assert.Contains(t, got.Request.Body, "secret")
	assert.Equal(t, got.Before["api_key"], got.After["api_key"], "the stored key is kept as get shows it")

	f, out, _ := testFactory(t, dryRunServer(t, storedWebhook("apikey", "")))
	require.NoError(t, run(NewCmdWebhook(f), "update", dryXID, "--auth-type", "none", "--secret", "", "--dry-run"))
	assert.NotContains(t, out.String(), "(new)")
}

// On a production terminal the real run's own preview is not printed beside the dry run's.
func TestDryRunReplay_ProductionTerminalPreviewsOnce(t *testing.T) {
	f, out, errOut := testFactory(t, dryRunServer(t, ""))
	f.Global.Env = config.EnvProduction
	prompttest.Use(t, f)
	require.NoError(t, run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1", "--force", "--dry-run"))
	assert.Equal(t, 1, strings.Count(out.String()+errOut.String(), "Transaction  tx-1"))
}

func TestDryRunReplay_YAML(t *testing.T) {
	f, out, errOut := testFactory(t, dryRunServer(t, ""))
	f.Global.Output = "yaml"
	require.NoError(t, run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1", "--dry-run"))
	assert.Empty(t, errOut.String())
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, []any{cmdutil.DryRunNotChecked}, got["not_checked"])
}
