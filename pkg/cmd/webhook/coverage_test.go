package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hookFor(xid string) Webhook {
	return Webhook{ID: "wh_1", BankMode: "single", BankAccountXID: &xid, EventType: "All", Active: true}
}

func allAccountsHook() Webhook {
	return Webhook{ID: "wh_all", BankMode: "all", EventType: "All", Active: true}
}

// Anything the list cannot decide must come back unknown, so the gate never blocks an unprovable drop.
func TestWebhookDelivers(t *testing.T) {
	withConfig := func(w Webhook, xid string, primary bool, vaMode string) Webhook {
		w.BankConfig = map[string]BankConfigEntry{xid: {ReceivePrimary: primary, VaMode: vaMode}}
		return w
	}

	cases := []struct {
		name  string
		hook  Webhook
		event string
		code  bool
		want  match
		why   string
	}{
		{
			name: "an all-accounts webhook covers an account it never names",
			hook: allAccountsHook(), event: eventPaymentIn, want: matchDelivers,
		},
		{
			name: "the targeted account",
			hook: hookFor("ba_1"), event: eventPaymentIn, want: matchDelivers,
		},
		{
			name: "a webhook on another account is the common trap",
			hook: hookFor("ba_9"), event: eventPaymentIn, want: matchElsewhere,
		},
		{
			name: "virtual-account only: the account number itself is silent",
			hook: func() Webhook {
				w := hookFor("ba_1")
				w.OnlyVA, w.SubAccountXIDs = true, []string{"va_1"}
				return w
			}(),
			event: eventPaymentIn, want: matchDrops, why: "virtual accounts only",
		},
		{
			name:  "virtual-account only with nothing on its list says so for the account number too",
			hook:  func() Webhook { w := hookFor("ba_1"); w.OnlyVA = true; return w }(),
			event: eventPaymentIn, want: matchDrops, why: "that list is empty",
		},
		{
			name: "virtual-account only delivers a VA credit, which the list cannot fully decide",
			hook: func() Webhook {
				w := hookFor("ba_1")
				w.OnlyVA, w.SubAccountXIDs = true, []string{"va_1"}
				return w
			}(),
			event: eventVACredit, want: matchUnknown,
		},
		{
			name:  "virtual-account only with nothing on its list delivers nothing",
			hook:  func() Webhook { w := hookFor("ba_1"); w.OnlyVA = true; return w }(),
			event: eventVACredit, want: matchDrops, why: "that list is empty",
		},
		{
			name:  "receive_primary off drops a transfer to the account number",
			hook:  withConfig(hookFor("ba_1"), "ba_1", false, "all"),
			event: eventPaymentIn, want: matchDrops, why: "skip transfers to the account number itself",
		},
		{
			name:  "receive_primary on delivers it",
			hook:  withConfig(hookFor("ba_1"), "ba_1", true, "all"),
			event: eventPaymentIn, want: matchDelivers,
		},
		{
			name:  "va_mode none drops a VA credit",
			hook:  withConfig(hookFor("ba_1"), "ba_1", true, "none"),
			event: eventVACredit, want: matchDrops, why: "skip virtual accounts",
		},
		{
			name:  "va_mode list with an empty list drops a VA credit",
			hook:  withConfig(hookFor("ba_1"), "ba_1", true, "list"),
			event: eventVACredit, want: matchDrops, why: "that list is empty",
		},
		{
			name: "va_mode list with a list is undecidable from a list response",
			hook: func() Webhook {
				w := withConfig(hookFor("ba_1"), "ba_1", true, "list")
				w.SubAccountXIDs = []string{"va_1"}
				return w
			}(),
			event: eventVACredit, want: matchUnknown,
		},
		{
			name:  "an incoming-only webhook drops an outgoing transfer",
			hook:  func() Webhook { w := hookFor("ba_1"); w.EventType = "In_only"; return w }(),
			event: eventPaymentOut, want: matchDrops, why: "incoming transfers only",
		},
		{
			name:  "an incoming-only webhook forwards a VA credit, which is incoming",
			hook:  func() Webhook { w := hookFor("ba_1"); w.EventType = "In_only"; return w }(),
			event: eventVACredit, want: matchDelivers,
		},
		{
			name:  "an outgoing-only webhook drops an incoming transfer",
			hook:  func() Webhook { w := hookFor("ba_1"); w.EventType = "Out_only"; return w }(),
			event: eventPaymentIn, want: matchDrops, why: "outgoing transfers only",
		},
		{
			name:  "skip_if_no_code drops an event that carries no code",
			hook:  func() Webhook { w := hookFor("ba_1"); w.SkipIfNoCode = true; return w }(),
			event: eventPaymentIn, want: matchDrops, why: "without a pay code",
		},
		{
			name:  "skip_if_no_code is no obstacle once a code is present",
			hook:  func() Webhook { w := hookFor("ba_1"); w.SkipIfNoCode = true; return w }(),
			event: eventPaymentIn, code: true, want: matchDelivers,
		},
		{
			name:  "a prefix filter drops an event that carries no code",
			hook:  func() Webhook { w := hookFor("ba_1"); w.PrefixFilters = []string{"SP"}; return w }(),
			event: eventPaymentIn, want: matchDrops, why: "codes starting with SP",
		},
		{
			name:  "a multi webhook delivers for an account on its list",
			hook:  Webhook{BankMode: "multi", EventType: "All", Active: true, BankAccountXIDs: []string{"ba_1", "ba_2"}},
			event: eventPaymentIn, want: matchDelivers,
		},
		{
			name:  "a multi webhook is undecidable for an account off its list",
			hook:  Webhook{BankMode: "multi", EventType: "All", Active: true, BankAccountXIDs: []string{"ba_7"}},
			event: eventPaymentIn, want: matchUnknown,
		},
		{
			name: "a multi webhook off its list still drops what its only_va gate drops",
			hook: Webhook{
				BankMode: "multi", EventType: "All", Active: true,
				BankAccountXIDs: []string{"ba_7"}, OnlyVA: true, SubAccountXIDs: []string{"va_1"},
			},
			event: eventPaymentIn, want: matchDrops, why: "virtual accounts only",
		},
		{
			name: "a multi webhook off its list still drops the wrong direction",
			hook: Webhook{
				BankMode: "multi", EventType: "In_only", Active: true,
				BankAccountXIDs: []string{"ba_7"},
			},
			event: eventPaymentOut, want: matchDrops, why: "incoming transfers only",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := tc.hook.delivers(delivery{account: "ba_1", event: tc.event, hasCode: tc.code})
			assert.Equal(t, tc.want, got, why)
			if tc.why != "" {
				assert.Contains(t, why, tc.why)
			}
		})
	}
}

func TestDeliverable(t *testing.T) {
	inactive := allAccountsHook()
	inactive.Active = false
	vaOnly := hookFor("ba_1")
	vaOnly.OnlyVA, vaOnly.SubAccountXIDs = true, []string{"va_1"}

	cases := []struct {
		name  string
		hooks []Webhook
		ok    bool
		why   string
	}{
		{name: "no webhook at all", why: "no active webhook covers this company"},
		{name: "only inactive webhooks", hooks: []Webhook{inactive}, why: "no active webhook covers this company"},
		{name: "a webhook elsewhere", hooks: []Webhook{hookFor("ba_9")}, why: "no active webhook targets this bank account"},
		{name: "a webhook that filters the event out", hooks: []Webhook{vaOnly}, why: "virtual accounts only"},
		{
			name:  "one webhook covering all rescues an account another filters out",
			hooks: []Webhook{vaOnly, allAccountsHook()}, ok: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, why := deliverable(tc.hooks, delivery{account: "ba_1", event: eventPaymentIn})
			assert.Equal(t, tc.ok, ok)
			if tc.why != "" {
				assert.Contains(t, why, tc.why)
			}
		})
	}
}

// A multi webhook (bank_account_id = 0) covers every account in sandbox until a filter proves the drop.
func TestWebhookCoverage_MultiAccount(t *testing.T) {
	multi := func(onlyVA bool, xids ...string) Webhook {
		return Webhook{
			ID: "wh_m", BankMode: "multi", EventType: "All", Active: true,
			BankAccountXIDs: xids, OnlyVA: onlyVA, SubAccountXIDs: []string{"va_1"},
		}
	}

	cases := []struct {
		name    string
		hook    Webhook
		account string
		ok      bool
		why     string
	}{
		{name: "the first account on the list", hook: multi(false, "ba_3", "ba_4"), account: "ba_3", ok: true},
		{name: "the second account on the list", hook: multi(false, "ba_3", "ba_4"), account: "ba_4", ok: true},
		{
			name: "an account off the list is undecidable, so the run is allowed",
			hook: multi(false, "ba_3"), account: "ba_5", ok: true,
		},
		{
			name: "an account off the list whose only_va gate drops the event is refused",
			hook: multi(true, "ba_3"), account: "ba_5", why: "virtual accounts only",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, why := deliverable([]Webhook{tc.hook}, delivery{account: tc.account, event: eventPaymentIn})
			assert.Equal(t, tc.ok, ok, why)
			if tc.why != "" {
				assert.Contains(t, why, tc.why)
			}
		})
	}
}

func webhookList(t *testing.T, hooks ...Webhook) string {
	t.Helper()
	data, err := json.Marshal(hooks)
	require.NoError(t, err)
	return `{"status":"success","data":` + string(data) + `,"meta":{"pagination":` +
		`{"total":1,"per_page":100,"current_page":1,"last_page":1,"has_more":false}}}`
}

// Refuse before the transaction is created, for the event being fired.
func TestAssertDeliverable(t *testing.T) {
	vaOnly := hookFor("ba_1")
	vaOnly.OnlyVA, vaOnly.SubAccountXIDs = true, []string{"va_1"}
	noPrimary := hookFor("ba_1")
	noPrimary.BankConfig = map[string]BankConfigEntry{"ba_1": {ReceivePrimary: false, VaMode: "all"}}
	inOnly := hookFor("ba_1")
	inOnly.EventType = "In_only"
	noCode := hookFor("ba_1")
	noCode.SkipIfNoCode = true
	// No per-account row for ba_1, so the only_va gate drops it.
	multiVAOnly := Webhook{
		ID: "wh_m", BankMode: "multi", EventType: "All", Active: true,
		BankAccountXIDs: []string{"ba_other"}, OnlyVA: true, SubAccountXIDs: []string{"va_1"},
	}

	cases := []struct {
		name    string
		hooks   []Webhook
		event   string
		wantErr string
	}{
		{"covered by an all-accounts webhook", []Webhook{allAccountsHook()}, eventPaymentIn, ""},
		{"covered by its own webhook", []Webhook{hookFor("ba_1")}, eventPaymentIn, ""},
		{"a webhook on another account", []Webhook{hookFor("ba_2")}, eventPaymentIn, "no active webhook targets this bank account"},
		{"no webhook at all", nil, eventPaymentIn, "no active webhook covers this company"},
		{"a VA-only webhook is the right target for a VA credit", []Webhook{vaOnly}, eventVACredit, ""},
		{"a VA-only webhook is the wrong target for a plain transfer", []Webhook{vaOnly}, eventPaymentIn, "virtual accounts only"},
		{"receive_primary off on a single-bank webhook", []Webhook{noPrimary}, eventPaymentIn, "skip transfers to the account number itself"},
		{"an incoming-only webhook and an outgoing event", []Webhook{inOnly}, eventPaymentOut, "incoming transfers only"},
		{"skip_if_no_code and an event with no code", []Webhook{noCode}, eventPaymentIn, "without a pay code"},
		{"a multi webhook off its list whose only_va gate drops the event", []Webhook{multiVAOnly}, eventPaymentIn, "virtual accounts only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := webhookList(t, tc.hooks...)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			c := api.NewClient(srv.URL, "test")
			c.MaxRetries = 0

			err := assertDeliverable(context.Background(), c, testOptions{bankAccount: "ba_1", event: tc.event})
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), "sepay webhook list")
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

// A VA-only webhook still covers VA credits on the account.
func TestDescribeCoverage_NamesTheEventKindsDelivered(t *testing.T) {
	vaOnly := hookFor("ba_1")
	vaOnly.OnlyVA, vaOnly.SubAccountXIDs = true, []string{"va_1"}
	primaryOnly := hookFor("ba_1")
	primaryOnly.BankConfig = map[string]BankConfigEntry{"ba_1": {ReceivePrimary: true, VaMode: "none"}}
	incomingEverywhere := allAccountsHook()
	incomingEverywhere.EventType = "In_only"
	incomingNamed := hookFor("ba_1")
	incomingNamed.EventType = "In_only"
	vaOnlyOutgoing := vaOnly
	vaOnlyOutgoing.EventType = "Out_only"
	emptyVAList := hookFor("ba_1")
	emptyVAList.OnlyVA = true

	cases := []struct {
		name     string
		hooks    []Webhook
		contains string
	}{
		{
			"every kind on a named account reads as plain coverage",
			[]Webhook{hookFor("ba_1")},
			"Delivering for: ba_1",
		},
		{
			"a virtual-account-only webhook names the VA credits it does deliver",
			[]Webhook{vaOnly},
			"ba_1 — delivering payment_in to virtual accounts only.",
		},
		{
			"va_mode none names the transfers it does deliver",
			[]Webhook{primaryOnly},
			"ba_1 — delivering payment_in except to virtual accounts, and payment_out.",
		},
		{
			"an incoming-only company webhook names the kinds it carries",
			[]Webhook{incomingEverywhere},
			"Delivering payment_in only for every bank account",
		},
		{
			"a named account with the same kinds as the rest folds into one line",
			[]Webhook{incomingNamed, incomingEverywhere},
			"  Delivering payment_in only for every bank account of this company.\n",
		},
		{
			"an account no kind reaches names every filter that drops one",
			[]Webhook{vaOnlyOutgoing},
			"ba_1 — its webhook forwards outgoing transfers only; its webhook covers virtual accounts only, " +
				"so a transfer to the account number itself is not delivered.",
		},
		{
			"an empty VA list is named for every kind it drops",
			[]Webhook{emptyVAList},
			"ba_1 — its webhook forwards only the virtual accounts on its list, and that list is empty.\n",
		},
		{
			"a webhook elsewhere still leaves its own account uncovered",
			[]Webhook{hookFor("ba_9")},
			"Any other bank account of this company produces no events.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := webhookList(t, tc.hooks...)
			f, _, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			})
			describeCoverage(context.Background(), f, fetchWebhookSet(context.Background(), f))
			assert.Contains(t, errOut.String(), tc.contains)
		})
	}
}

func TestPartnerManagedWebhookIsNotCoverage(t *testing.T) {
	xid := "ba_1"
	partner := Webhook{Active: true, BankMode: "single", BankAccountXID: &xid, EventType: "All", ManagedByPartner: true}

	ok, why := deliverable([]Webhook{partner}, delivery{account: xid, event: eventPaymentIn})

	assert.False(t, ok)
	assert.Equal(t, reasonPartnerManaged, why)
}

func TestOwnWebhookIsStillCoverage(t *testing.T) {
	xid := "ba_1"
	own := Webhook{Active: true, BankMode: "single", BankAccountXID: &xid, EventType: "All"}

	ok, _ := deliverable([]Webhook{own}, delivery{account: xid, event: eventPaymentIn})

	assert.True(t, ok)
}
