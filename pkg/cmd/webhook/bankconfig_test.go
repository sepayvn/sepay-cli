package webhook

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Accounts of every kind the web form tells apart, by what the server reports. Only ba_both and
// ba_vaonly have official VAs; the SMS accounts and ba_hasva reach VAs some other way, and
// ba_novas is VA-only by va_only alone.
var reachAccounts = map[string]string{
	"ba_both":    `{"id":"ba_both","bank_short_name":"ACB","account_number":"111","connection_type":"api","official_va_supported":true,"va_only":false}`,
	"ba_vaonly":  `{"id":"ba_vaonly","bank_short_name":"MSB","account_number":"222","connection_type":"api","official_va_supported":true,"va_only":true}`,
	"ba_sms":     `{"id":"ba_sms","bank_short_name":"MSB","account_number":"333","connection_type":"sms","official_va_supported":false,"va_only":false}`,
	"ba_plain":   `{"id":"ba_plain","bank_short_name":"TPBank","account_number":"444","connection_type":"api","official_va_supported":false,"va_only":false}`,
	"ba_hasva":   `{"id":"ba_hasva","bank_short_name":"TPBank","account_number":"555","connection_type":"api","official_va_supported":false,"va_only":false}`,
	"ba_novas":   `{"id":"ba_novas","bank_short_name":"MSB","account_number":"666","connection_type":"api","official_va_supported":false,"va_only":true}`,
	"ba_smsnova": `{"id":"ba_smsnova","bank_short_name":"TPBank","account_number":"777","connection_type":"api+sms","official_va_supported":false,"va_only":false}`,
}

// reachVAs are each account's virtual accounts; an inactive one never counts.
var reachVAs = map[string]string{
	"ba_both":    `[{"id":"va_b1","va":"VAB1","active":1},{"id":"va_b2","va":"VAB2","active":1}]`,
	"ba_vaonly":  `[{"id":"va_v1","va":"VAV1","active":1}]`,
	"ba_sms":     `[{"id":"va_s1","va":"VAS1","active":1}]`,
	"ba_plain":   `[{"id":"va_p0","va":"VAP0","active":0}]`,
	"ba_hasva":   `[{"id":"va_h1","va":"VAH1","active":1}]`,
	"ba_novas":   `[]`,
	"ba_smsnova": `[]`,
}

// reachServer serves the accounts, their VAs and the account list, and records the webhook write.
type reachServer struct {
	mu    sync.Mutex
	body  map[string]any
	wrote bool
}

func newReachServer(t *testing.T, stored string) (*cmdutil.Factory, *reachServer) {
	t.Helper()
	s := &reachServer{}
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && path == "/bank-accounts":
			var rows []string
			for _, xid := range sortedKeys(reachAccounts) {
				rows = append(rows, reachAccounts[xid])
			}
			_, _ = w.Write([]byte(`{"status":"success","data":[` + strings.Join(rows, ",") + `]}`))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/va"):
			xid := strings.TrimSuffix(strings.TrimPrefix(path, "/bank-accounts/"), "/va")
			_, _ = w.Write([]byte(`{"status":"success","data":` + reachVAs[xid] + `}`))
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/bank-accounts/"):
			_, _ = w.Write([]byte(`{"status":"success","data":` + reachAccounts[strings.TrimPrefix(path, "/bank-accounts/")] + `}`))
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/webhooks/"):
			_, _ = w.Write([]byte(`{"status":"success","data":` + stored + `}`))
		default:
			s.mu.Lock()
			defer s.mu.Unlock()
			s.wrote = true
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &s.body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_9","bank_mode":"single","bank_sub_account_xids":[],"prefix_filters":[],"active":true,"created_at":"x"}}`))
		}
	})
	return f, s
}

func (s *reachServer) config(t *testing.T) (map[string]any, []any) {
	t.Helper()
	cfg, ok := s.body["bank_config"].(map[string]any)
	require.True(t, ok, "no bank_config in %v", s.body)
	subs, _ := s.body["bank_sub_account_xids"].([]any)
	return cfg, subs
}

func entry(primary bool, mode string) map[string]any {
	return map[string]any{"receive_primary": primary, "va_mode": mode}
}

var createSingle = []string{"create", "--url", "https://x.test", "--name", "n", "--auth-type", "none", "--bank-mode", "single", "--bank-account"}

const formLabel = "What each bank account notifies"

// shape is what the form offers for one account: whether the account number can be edited, its
// starting value, and the VA modes by label.
type shape struct {
	fixed   bool
	primary bool
	modes   []string
	note    string
}

func shapeOf(a cmdutil.FormAccount) shape {
	var modes []string
	for _, m := range a.Modes {
		modes = append(modes, m.Label)
	}
	return shape{fixed: a.PrimaryFixed, primary: a.Primary, modes: modes, note: a.Note}
}

func TestCreate_FormOffersWhatEachKindOfAccountCanNotify(t *testing.T) {
	cases := []struct {
		account  string
		want     shape
		answer   cmdutil.FormAnswer
		sent     map[string]any
		sentSubs []any
	}{
		{
			"ba_both",
			shape{false, true, []string{labelAll, labelList, labelNone}, "api"},
			cmdutil.FormAnswer{Primary: true, Mode: 2},
			entry(true, "none"),
			[]any{},
		},
		{
			"ba_vaonly",
			shape{true, false, []string{labelAll, labelList}, "virtual accounts only"},
			cmdutil.FormAnswer{Mode: 1, Picked: []int{0}},
			entry(false, "list"),
			[]any{"va_v1"},
		},
		{
			"ba_sms",
			shape{false, true, []string{labelAll, labelList, labelNone}, "sms"},
			cmdutil.FormAnswer{Primary: false, Mode: 0},
			entry(false, "all"),
			[]any{},
		},
		{
			"ba_plain",
			shape{true, true, []string{labelNone}, "no virtual accounts"},
			cmdutil.FormAnswer{Primary: true, Mode: 0},
			entry(true, "none"),
			[]any{},
		},
		{
			"ba_hasva",
			shape{false, true, []string{labelAll, labelList, labelNone}, "api"},
			cmdutil.FormAnswer{Primary: true, Mode: 1, Picked: []int{0}},
			entry(true, "list"),
			[]any{"va_h1"},
		},
		{
			"ba_smsnova",
			shape{false, true, []string{labelAll, labelNone}, "api+sms"},
			cmdutil.FormAnswer{Primary: false, Mode: 0},
			entry(false, "all"),
			[]any{},
		},
		{
			"ba_novas",
			shape{true, false, []string{labelAll}, "virtual accounts only · none issued yet, so nothing is notified until one is"},
			cmdutil.FormAnswer{Mode: 0},
			entry(false, "all"),
			[]any{},
		},
	}
	for _, c := range cases {
		t.Run(c.account, func(t *testing.T) {
			f, srv := newReachServer(t, "")
			prompts := prompttest.Use(t, f, prompttest.Step{Label: formLabel, Answer: []cmdutil.FormAnswer{c.answer}})
			require.NoError(t, run(NewCmdWebhook(f), append(createSingle, c.account)...))

			form := prompts.Forms[formLabel]
			require.Len(t, form, 1)
			assert.Equal(t, c.want, shapeOf(form[0]))
			cfg, subs := srv.config(t)
			assert.Equal(t, map[string]any{c.account: c.sent}, cfg)
			assert.Equal(t, c.sentSubs, subs)
		})
	}
}

func TestCreate_MultiShowsEveryAccountInOneForm(t *testing.T) {
	f, srv := newReachServer(t, "")
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "Bank accounts to target", Answer: []string{"ACB · 111", "MSB · 222"}},
		prompttest.Step{Label: formLabel, Answer: []cmdutil.FormAnswer{
			{Primary: true, Mode: 1, Picked: []int{0}},
			{Mode: 0},
		}},
	)
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--name", "n", "--auth-type", "none", "--bank-mode", "multi"))
	require.Len(t, prompts.Forms[formLabel], 2)
	assert.Equal(t, "ACB · 111", prompts.Forms[formLabel][0].Title)
	assert.Equal(t, []cmdutil.FormOption{{Label: "VAB1", Short: "VAB1"}, {Label: "VAB2", Short: "VAB2"}}, prompts.Forms[formLabel][0].VAs)
	cfg, subs := srv.config(t)
	assert.Equal(t, map[string]any{"ba_both": entry(true, "list"), "ba_vaonly": entry(false, "all")}, cfg)
	assert.Equal(t, []any{"va_b1"}, subs)
	assert.Equal(t, []any{"ba_both", "ba_vaonly"}, srv.body["bank_account_xids"])
}

const storedMulti = `{"id":"wh_1","bank_mode":"multi","bank_account_xids":["ba_both","ba_sms"],` +
	`"bank_config":{"ba_both":{"receive_primary":true,"va_mode":"list"},"ba_sms":{"receive_primary":false,"va_mode":"all"}},` +
	`"bank_sub_account_xids":["va_b2"],"prefix_filters":[],"active":true,"created_at":"x"}`

func TestUpdate_FormStartsFromTheStoredConfig(t *testing.T) {
	f, srv := newReachServer(t, storedMulti)
	f.Global.Yes = true
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Targeting (multi · 2 accounts)"}},
		prompttest.Step{Label: "Targeting mode", Answer: "multi"},
		prompttest.Step{Label: "Bank accounts to target", Answer: []string{"ACB · 111", "MSB · 333"}},
		prompttest.Step{Label: formLabel, Answer: []cmdutil.FormAnswer{
			{Primary: false, Mode: 1, Picked: []int{0}},
			{Primary: false, Mode: 0},
		}},
	)
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1"))

	assert.Equal(t, "multi", prompts.Defaults["Targeting mode"])
	assert.Equal(t, []string{"ACB · 111", "MSB · 333"}, prompts.Defaults["Bank accounts to target"])
	form := prompts.Forms[formLabel]
	require.Len(t, form, 2)
	assert.True(t, form[0].Primary)
	assert.Equal(t, labelList, form[0].Modes[form[0].Mode].Label, "the stored list mode")
	assert.Equal(t, []int{1}, form[0].Picked, "the stored VA va_b2 starts checked")
	assert.False(t, form[1].Primary)
	assert.Equal(t, labelAll, form[1].Modes[form[1].Mode].Label)

	cfg, subs := srv.config(t)
	assert.Equal(t, map[string]any{"ba_both": entry(false, "list")}, cfg, "ba_sms answered as stored is left for the server to keep")
	assert.Equal(t, []any{"va_b1"}, subs, "the VA list is replaced whole, so ba_both's new pick is sent")
	assert.Equal(t, "multi", srv.body["bank_mode"], "a multi targeting change resends the mode")
}

func TestUpdate_FormAnsweredAsStoredSendsNoBankConfig(t *testing.T) {
	f, srv := newReachServer(t, storedMulti)
	f.Global.Yes = true
	prompttest.Use(t, f,
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Targeting (multi · 2 accounts)"}},
		prompttest.Step{Label: "Targeting mode", Answer: "multi"},
		prompttest.Step{Label: "Bank accounts to target", Answer: []string{"ACB · 111", "MSB · 333"}},
		prompttest.Step{Label: formLabel, Answer: []cmdutil.FormAnswer{
			{Primary: true, Mode: 1, Picked: []int{1}},
			{Primary: false, Mode: 0},
		}},
	)
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1"))
	assert.NotContains(t, srv.body, "bank_config")
	assert.Equal(t, []any{"va_b2"}, srv.body["bank_sub_account_xids"], "the list account keeps its VA in the list")
}

func TestBankConfigFlag_RefusedBeforeTheWrite(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--bank-account", "ba_vaonly", "--bank-config", "ba_vaonly=on:none"}, "--bank-config ba_vaonly: this bank account only notifies virtual accounts; use off:all or off:list"},
		{[]string{"--bank-account", "ba_plain", "--bank-config", "ba_plain=off:all"}, "--bank-config ba_plain: this bank account has no virtual account to notify; use on:none, or issue one first with: sepay va issue"},
		{[]string{"--bank-account", "ba_novas", "--bank-config", "ba_novas=off:list", "--sub-account", "va_v1"}, "--bank-config ba_novas: va mode list needs at least one of this account's virtual accounts in --va-xid"},
		{[]string{"--bank-account", "ba_plain", "--bank-config", "ba_plain=off:list", "--sub-account", "va_p0"}, "--bank-config ba_plain: this bank account has no virtual account to notify; use on:none, or issue one first with: sepay va issue"},
		{[]string{"--bank-account", "ba_both", "--bank-config", "ba_both=off:none"}, "--bank-config ba_both=off:none notifies nothing; turn the account number on or pick a VA mode"},
		{[]string{"--bank-account", "ba_both", "--bank-config", "ba_both=on:list"}, "--bank-config ba_both uses list: name its virtual accounts with --va-xid"},
		{[]string{"--bank-account", "ba_both", "--bank-config", "ba_both=on:list", "--sub-account", "va_v1"}, "--bank-config ba_both: va mode list needs at least one of this account's virtual accounts in --va-xid"},
		{[]string{"--bank-account", "ba_both", "--bank-config", "ba_sms=on:all"}, "--bank-config ba_sms is not one of the targeted bank accounts"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			f, srv := newReachServer(t, "")
			err := run(NewCmdWebhook(f), append([]string{"create", "--url", "https://x.test", "--bank-mode", "single"}, c.args...)...)
			require.EqualError(t, err, c.want)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.False(t, srv.wrote)
		})
	}
}

func TestBankConfigFlag_AcceptedConfigsAreSent(t *testing.T) {
	f, srv := newReachServer(t, "")
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-mode", "multi",
		"--bank-account", "ba_hasva", "--bank-account", "ba_vaonly", "--bank-account", "ba_plain", "--bank-account", "ba_smsnova",
		"--bank-config", "ba_hasva=off:list", "--sub-account", "va_h1",
		"--bank-config", "ba_vaonly=off:all", "--bank-config", "ba_plain=on:none", "--bank-config", "ba_smsnova=off:all"))
	cfg, subs := srv.config(t)
	assert.Equal(t, map[string]any{
		"ba_hasva": entry(false, "list"), "ba_vaonly": entry(false, "all"), "ba_plain": entry(true, "none"), "ba_smsnova": entry(false, "all"),
	}, cfg, "an SMS account with no official VA and no VA yet still takes the account number off")
	assert.Equal(t, []any{"va_h1"}, subs)
}

func TestBankConfigFlag_VAOnlyAccountNumberOnIsSentOffWithAWarning(t *testing.T) {
	f, srv := newReachServer(t, "")
	require.NoError(t, run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-mode", "single",
		"--bank-account", "ba_vaonly", "--bank-config", "ba_vaonly=on:all"))
	cfg, _ := srv.config(t)
	assert.Equal(t, map[string]any{"ba_vaonly": entry(false, "all")}, cfg)
	assert.Contains(t, f.IOStreams.ErrOut.(*bytes.Buffer).String(),
		"warning: --bank-config ba_vaonly: this bank account only notifies virtual accounts; the account number is not notified")
}

func TestCreate_Explains422OnTheTargeting(t *testing.T) {
	cases := []struct {
		server    string
		explained bool
	}{
		{"bank account ba_x only delivers virtual-account transactions; receive_primary must be false", true},
		{"bank account ba_x would receive nothing: set receive_primary true or va_mode all or list", true},
		{"Unknown bank account xid(s) in bank_config: ba_x", false},
	}
	for _, c := range cases {
		t.Run(c.server, func(t *testing.T) {
			f, _, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				b, _ := json.Marshal(map[string]any{
					"status": "error", "error_code": "validation_error", "message": "invalid",
					"errors": map[string]string{"bank_config": c.server},
				})
				_, _ = w.Write(b)
			})
			err := run(NewCmdWebhook(f), "create", "--url", "https://x.test", "--bank-mode", "all")
			var he *api.HTTPError
			require.ErrorAs(t, err, &he)
			if c.explained {
				assert.Contains(t, he.Errors["bank_config"], "a virtual-account-only bank needs off:all or off:list")
				assert.Contains(t, he.Errors["bank_config"], "(server: "+c.server+")", "the server's words stay")
			} else {
				assert.Equal(t, c.server, he.Errors["bank_config"], "a different 422 keeps its own words")
			}
		})
	}
}

func TestUpdate_ListKeepsTheStoredVirtualAccounts(t *testing.T) {
	f, srv := newReachServer(t, storedMulti)
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1", "--bank-config", "ba_both=on:list"))
	cfg, _ := srv.config(t)
	assert.Equal(t, map[string]any{"ba_both": entry(true, "list")}, cfg)
	assert.NotContains(t, srv.body, "bank_sub_account_xids", "the stored list stays; the server checks it")

	f, srv = newReachServer(t, storedMulti)
	err := run(NewCmdWebhook(f), "update", "wh_1", "--bank-config", "ba_both=on:list", "--sub-account", "va_v1")
	require.EqualError(t, err, "--bank-config ba_both: va mode list needs at least one of this account's virtual accounts in --va-xid")
	assert.False(t, srv.wrote, "a list that is given is still checked")
}

func TestUpdate_EditorChoicesStartOnTheStoredValues(t *testing.T) {
	stored := `{"id":"wh_1","bank_mode":"all","event_type":"In_only","request_content_type":"Json","skip_if_no_code":true,` +
		`"bank_sub_account_xids":[],"prefix_filters":[],"active":true,"created_at":"x"}`
	f, srv := newReachServer(t, stored)
	f.Global.Yes = true
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Events (In_only)", "Content type (Json)", "Skip events with no pay code (yes)"}},
		prompttest.Step{Label: "Events", Answer: "in"},
		prompttest.Step{Label: "Content type", Answer: "json"},
		prompttest.Step{Label: "Skip events with no pay code", Answer: "yes"},
	)
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1"))
	assert.Equal(t, "in", prompts.Defaults["Events"], "In_only is the flag value in")
	assert.Equal(t, "json", prompts.Defaults["Content type"])
	assert.Equal(t, "yes", prompts.Defaults["Skip events with no pay code"])
	assert.Equal(t, "in", srv.body["event_type"])
	assert.Equal(t, true, srv.body["skip_if_no_code"])
}

const storedSinglePlain = `{"id":"wh_1","bank_mode":"single","bank_account_xid":"ba_plain","bank_sub_account_xids":[],"prefix_filters":[],"active":true,"created_at":"x"}`

func TestUpdate_MultiFlagUpdateCarriesTheStoredTargeting(t *testing.T) {
	stored := `{"id":"wh_1","bank_mode":"multi","bank_account_xids":["ba_both","ba_hasva"],` +
		`"bank_config":{"ba_both":{"receive_primary":true,"va_mode":"list"},"ba_hasva":{"receive_primary":true,"va_mode":"list"}},` +
		`"bank_sub_account_xids":["va_b2","va_h1"],"prefix_filters":[],"active":true,"created_at":"x"}`
	f, srv := newReachServer(t, stored)
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1", "--bank-config", "ba_both=on:list", "--sub-account", "va_b1"))

	assert.Equal(t, "multi", srv.body["bank_mode"], "the server needs the mode to change a multi webhook")
	assert.Equal(t, []any{"ba_both", "ba_hasva"}, srv.body["bank_account_xids"])
	assert.Equal(t, []any{"va_b1", "va_h1"}, srv.body["bank_sub_account_xids"],
		"ba_both's VAs are replaced; ba_hasva keeps the VA it had")
}

func TestUpdate_BankModeWithVAsKeepsOtherAccountsVAs(t *testing.T) {
	stored := `{"id":"wh_1","bank_mode":"multi","bank_account_xids":["ba_both","ba_hasva","ba_sms"],` +
		`"bank_config":{"ba_both":{"receive_primary":true,"va_mode":"list"},"ba_hasva":{"receive_primary":true,"va_mode":"list"},"ba_sms":{"receive_primary":true,"va_mode":"list"}},` +
		`"bank_sub_account_xids":["va_b2","va_h1","va_s1"],"prefix_filters":[],"active":true,"created_at":"x"}`
	f, srv := newReachServer(t, stored)
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1", "--bank-mode", "multi",
		"--bank-account", "ba_both", "--bank-account", "ba_hasva", "--sub-account", "va_b1"))

	assert.Equal(t, []any{"ba_both", "ba_hasva"}, srv.body["bank_account_xids"], "the accounts typed, not the stored ones")
	assert.Equal(t, []any{"va_b1", "va_h1"}, srv.body["bank_sub_account_xids"],
		"ba_hasva keeps its VA rather than widening to all; ba_sms is no longer targeted")
}

func TestUpdate_SubAccountAloneKeepsOtherAccountsVAs(t *testing.T) {
	stored := `{"id":"wh_1","bank_mode":"multi","bank_account_xids":["ba_both","ba_hasva"],` +
		`"bank_sub_account_xids":["va_b2","va_h1"],"prefix_filters":[],"active":true,"created_at":"x"}`
	f, srv := newReachServer(t, stored)
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1", "--sub-account", "va_b1"))
	assert.Equal(t, []any{"va_b1", "va_h1"}, srv.body["bank_sub_account_xids"])
}

func TestUpdate_AllWebhookNeedsAModeToConfigureAccounts(t *testing.T) {
	f, srv := newReachServer(t, `{"id":"wh_1","bank_mode":"all","bank_sub_account_xids":[],"prefix_filters":[],"active":true,"created_at":"x"}`)
	err := run(NewCmdWebhook(f), "update", "wh_1", "--bank-config", "ba_both=on:all")
	require.EqualError(t, err, "this webhook targets every bank account; pass --bank-mode single or multi with --bank-account to configure accounts")
	assert.False(t, srv.wrote)
}

func TestCreate_TerminalFormStartsOnTheFlagVAs(t *testing.T) {
	f, srv := newReachServer(t, "")
	prompts := prompttest.Use(t, f, prompttest.Step{Label: formLabel, Answer: []cmdutil.FormAnswer{{Primary: false, Mode: 1, Picked: []int{1}}}})
	require.NoError(t, run(NewCmdWebhook(f), append(createSingle, "ba_both", "--sub-account", "va_b2")...))
	form := prompts.Forms[formLabel][0]
	assert.Equal(t, labelList, form.Modes[form.Mode].Label, "--sub-account means only those VAs, as without a terminal")
	assert.Equal(t, []int{1}, form.Picked)
	_, subs := srv.config(t)
	assert.Equal(t, []any{"va_b2"}, subs)
}

func TestUpdate_FormReadsAnOlderWebhookWithoutBankConfig(t *testing.T) {
	stored := `{"id":"wh_1","bank_mode":"single","bank_account_xid":"ba_both","only_va":true,` +
		`"bank_sub_account_xids":["va_b1"],"prefix_filters":[],"active":true,"created_at":"x"}`
	f, _ := newReachServer(t, stored)
	f.Global.Yes = true
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Targeting (single · ba_both)"}},
		prompttest.Step{Label: "Targeting mode", Answer: "single"},
		prompttest.Step{Label: "Bank account", Answer: "ACB · 111"},
		prompttest.Step{Label: formLabel, Answer: []cmdutil.FormAnswer{{Primary: false, Mode: 1, Picked: []int{0}}}},
	)
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1"))
	form := prompts.Forms[formLabel][0]
	assert.False(t, form.Primary, "only_va means the account number is off")
	assert.Equal(t, labelList, form.Modes[form.Mode].Label, "its VA list means only those VAs")
	assert.Equal(t, []int{0}, form.Picked)
}

func TestUpdate_FlagUpdateKeepsAnInactiveStoredVA(t *testing.T) {
	stored := `{"id":"wh_1","bank_mode":"multi","bank_account_xids":["ba_both","ba_plain"],` +
		`"bank_config":{"ba_both":{"receive_primary":true,"va_mode":"list"},"ba_plain":{"receive_primary":false,"va_mode":"list"}},` +
		`"bank_sub_account_xids":["va_b2","va_p0"],"prefix_filters":[],"active":true,"created_at":"x"}`
	f, srv := newReachServer(t, stored)
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1", "--va-xid", "va_b1"))
	assert.Equal(t, []any{"va_b1", "va_p0"}, srv.body["bank_sub_account_xids"],
		"ba_plain's only VA is inactive but stored; dropping it would widen ba_plain to every transaction")
}

// openTargetingForm runs `webhook update wh_1` on a terminal, keeps the stored multi targeting of
// accounts and answers the accounts form with answers.
func openTargetingForm(t *testing.T, f *cmdutil.Factory, accounts []string, answers []cmdutil.FormAnswer) *prompttest.Prompter {
	t.Helper()
	f.Global.Yes = true
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Targeting (multi · 2 accounts)"}},
		prompttest.Step{Label: "Targeting mode", Answer: "multi"},
		prompttest.Step{Label: "Bank accounts to target", Answer: accounts},
		prompttest.Step{Label: formLabel, Answer: answers},
	)
	require.NoError(t, run(NewCmdWebhook(f), "update", "wh_1"))
	return prompts
}

func TestUpdate_FormKeepsAListOfInactiveVAsAsStored(t *testing.T) {
	stored := `{"id":"wh_1","bank_mode":"multi","bank_account_xids":["ba_both","ba_plain"],` +
		`"bank_config":{"ba_both":{"receive_primary":true,"va_mode":"list"},"ba_plain":{"receive_primary":false,"va_mode":"list"}},` +
		`"bank_sub_account_xids":["va_b2","va_p0"],"prefix_filters":[],"active":true,"created_at":"x"}`
	f, srv := newReachServer(t, stored)
	prompts := openTargetingForm(t, f, []string{"ACB · 111", "TPBank · 444"}, []cmdutil.FormAnswer{
		{Primary: true, Mode: 1, Picked: []int{1}},
		{Primary: false, Mode: 0},
	})

	plain := prompts.Forms[formLabel][1]
	assert.True(t, plain.PrimaryFixed, "the form cannot offer ba_plain's stored list, so it is not editable")
	assert.False(t, plain.Primary)
	require.Len(t, plain.Modes, 1)
	assert.Equal(t, labelList+" (kept as stored)", plain.Modes[0].Label)
	assert.NotContains(t, srv.body, "bank_config", "ba_plain was never on:none; sending it would notify its account number")
	assert.Equal(t, []any{"va_b2", "va_p0"}, srv.body["bank_sub_account_xids"],
		"ba_plain's inactive VA stays, or the server widens ba_plain to every transaction")
}

func TestUpdate_FormKeepsAVAOnlyListAsStored(t *testing.T) {
	stored := `{"id":"wh_1","bank_mode":"multi","bank_account_xids":["ba_both","ba_novas"],` +
		`"bank_config":{"ba_both":{"receive_primary":true,"va_mode":"all"},"ba_novas":{"receive_primary":false,"va_mode":"list"}},` +
		`"bank_sub_account_xids":[],"prefix_filters":[],"active":true,"created_at":"x"}`
	f, srv := newReachServer(t, stored)
	prompts := openTargetingForm(t, f, []string{"ACB · 111", "MSB · 666"}, []cmdutil.FormAnswer{
		{Primary: true, Mode: 0},
		{Primary: false, Mode: 0},
	})

	novas := prompts.Forms[formLabel][1]
	require.Len(t, novas.Modes, 1)
	assert.Equal(t, labelList+" (kept as stored)", novas.Modes[0].Label, "not All: that would notify every VA issued later")
	assert.NotContains(t, srv.body, "bank_config")
}

func TestUpdate_FormKeepsTheInactiveVAsOfAListAccount(t *testing.T) {
	saved := reachVAs["ba_both"]
	reachVAs["ba_both"] = `[{"id":"va_b1","va":"VAB1","active":1},{"id":"va_b2","va":"VAB2","active":1},{"id":"va_b3","va":"VAB3","active":0}]`
	t.Cleanup(func() { reachVAs["ba_both"] = saved })
	stored := strings.Replace(storedMulti, `"bank_sub_account_xids":["va_b2"]`, `"bank_sub_account_xids":["va_b2","va_b3"]`, 1)
	f, srv := newReachServer(t, stored)
	openTargetingForm(t, f, []string{"ACB · 111", "MSB · 333"}, []cmdutil.FormAnswer{
		{Primary: true, Mode: 1, Picked: []int{0}},
		{Primary: false, Mode: 0},
	})
	assert.Equal(t, []any{"va_b1", "va_b3"}, srv.body["bank_sub_account_xids"],
		"the form never shows va_b3, so the answer cannot have dropped it")
}

func TestUpdate_FormKeepsAListOfOnlyInactiveVAsBesideActiveOnes(t *testing.T) {
	saved := reachVAs["ba_both"]
	reachVAs["ba_both"] = `[{"id":"va_b1","va":"VAB1","active":1},{"id":"va_b2","va":"VAB2","active":1},{"id":"va_b3","va":"VAB3","active":0}]`
	t.Cleanup(func() { reachVAs["ba_both"] = saved })
	stored := strings.Replace(storedMulti, `"bank_sub_account_xids":["va_b2"]`, `"bank_sub_account_xids":["va_b3"]`, 1)
	f, srv := newReachServer(t, stored)
	prompts := openTargetingForm(t, f, []string{"ACB · 111", "MSB · 333"}, []cmdutil.FormAnswer{
		{Primary: true, Mode: 0},
		{Primary: false, Mode: 0},
	})

	both := prompts.Forms[formLabel][0]
	require.Len(t, both.Modes, 1, "Only selected with nothing checked would make the user check an active VA")
	assert.Equal(t, labelList+" (kept as stored)", both.Modes[0].Label)
	assert.NotContains(t, srv.body, "bank_config")
	assert.Equal(t, []any{"va_b3"}, srv.body["bank_sub_account_xids"])
}
