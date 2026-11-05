package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmd/bankaccount"
	vapkg "github.com/sepayvn/sepay-cli/pkg/cmd/va"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
)

// VA modes of a bank_config entry.
const (
	vaModeAll  = "all"
	vaModeList = "list"
	vaModeNone = "none"
)

// accountReach is what one bank account can notify: the server's capability fields, never a
// guess from the bank's name, plus its active virtual accounts. inactive holds the xids of the
// rest, which the form never shows but a stored list may name.
type accountReach struct {
	account  bankaccount.BankAccount
	vas      []vapkg.VA
	inactive []string
}

// receivesVA reports whether a VA mode other than none can ever deliver anything: the bank's API
// issues official VAs, an SMS connection carries any VA, or the account already has an active one.
func (r accountReach) receivesVA() bool {
	return r.account.VAOnly || r.account.OfficialVASupported || r.account.OverSMS() || len(r.vas) > 0
}

func (r accountReach) ownsVA(xid string) bool {
	return slices.ContainsFunc(r.vas, func(v vapkg.VA) bool { return v.ID == xid })
}

func loadReach(ctx context.Context, client *api.Client, account bankaccount.BankAccount) (accountReach, error) {
	vas, err := vapkg.FetchAll(ctx, client, account.ID)
	if err != nil {
		return accountReach{}, err
	}
	reach := accountReach{account: account}
	for _, v := range vas {
		if int(v.Active) == 1 {
			reach.vas = append(reach.vas, v)
		} else {
			reach.inactive = append(reach.inactive, v.ID)
		}
	}
	return reach, nil
}

// check refuses an entry the server answers with 422 and that needs the account to tell: a
// VA-only account with no VAs, the account number off where no VA can
// ever arrive, and list without one of the account's own VAs. off:none needs no account and is
// refused by parseBankConfig. subs is the webhook's whole bank_sub_account_xids list; with
// subsKnown false (an update keeping its stored list) the list rule is left to the server.
func (r accountReach) check(xid string, e BankConfigEntry, subs []string, subsKnown bool) error {
	fail := func(format string, a ...any) error {
		return &cmdutil.FlagError{Err: fmt.Errorf("--bank-config %s: "+format, append([]any{xid}, a...)...)}
	}
	switch {
	case r.account.VAOnly && e.VaMode == vaModeNone:
		return fail("this bank account only notifies virtual accounts; use off:all or off:list")
	case !e.ReceivePrimary && !r.receivesVA():
		return fail("this bank account has no virtual account to notify; use on:none, or issue one first with: sepay va issue")
	case e.VaMode == vaModeList && subsKnown && !slices.ContainsFunc(subs, r.ownsVA):
		return fail("va mode list needs at least one of this account's virtual accounts in --va-xid")
	}
	return nil
}

// checkBankConfig reads each configured account before the write, so a config that would receive
// nothing fails here with the reason instead of as a bare 422. The server still decides. The
// account number turned on for a VA-only account is sent off with a warning: the server stores it
// off either way.
func checkBankConfig(ctx context.Context, f *cmdutil.Factory, body map[string]any, subsKnown bool) error {
	cfg, _ := body["bank_config"].(map[string]any)
	if len(cfg) == 0 {
		return nil
	}
	targeted := bodyAccounts(body)
	subs, _ := body["bank_sub_account_xids"].([]string)
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	for _, xid := range sortedKeys(cfg) {
		if targeted != nil && !slices.Contains(targeted, xid) {
			return &cmdutil.FlagError{Err: fmt.Errorf("--bank-config %s is not one of the targeted bank accounts", xid)}
		}
		account, err := bankaccount.Fetch(ctx, client, xid)
		if err != nil {
			return err
		}
		reach, err := loadReach(ctx, client, account)
		if err != nil {
			return err
		}
		e := entryOf(cfg[xid])
		if err := reach.check(xid, e, subs, subsKnown); err != nil {
			return err
		}
		if account.VAOnly && e.ReceivePrimary {
			fmt.Fprintf(f.IOStreams.ErrOut, "warning: --bank-config %s: this bank account only notifies virtual accounts; the account number is not notified\n", xid)
			cfg[xid] = map[string]any{"receive_primary": false, "va_mode": e.VaMode}
		}
	}
	return nil
}

// bodyAccounts is the account list the body sets, nil when it leaves the stored one alone.
func bodyAccounts(body map[string]any) []string {
	if xid, ok := body["bank_account_xid"].(string); ok {
		return []string{xid}
	}
	if xids, ok := body["bank_account_xids"].([]string); ok {
		return xids
	}
	return nil
}

func entryOf(v any) BankConfigEntry {
	m, _ := v.(map[string]any)
	primary, _ := m["receive_primary"].(bool)
	mode, _ := m["va_mode"].(string)
	return BankConfigEntry{ReceivePrimary: primary, VaMode: mode}
}

// askBankConfig shows one form for every targeted account, offering only what each can deliver,
// as the web form does, and returns bank_config and the checked VAs. stored is the webhook being
// edited, nil on create; subsFlag, when given, are the VAs checked to start with. On an update an
// account whose stored config is answered unchanged is left out of bank_config, so the server keeps
// it as stored; its checked VAs still go into the list, which is replaced whole, with its stored
// inactive VAs. A stored config the form cannot show, such as a list of inactive VAs only, is kept
// as stored: offering only the modes the form has would widen the account.
func askBankConfig(ctx context.Context, f *cmdutil.Factory, client *api.Client, accounts []bankaccount.BankAccount, stored *Webhook, subsFlag []string) (map[string]any, []string, error) {
	reaches := make([]accountReach, len(accounts))
	forms := make([]cmdutil.FormAccount, len(accounts))
	kept := make([]bool, len(accounts))
	var storedSubs []string
	if stored != nil {
		storedSubs = stored.SubAccountXIDs
	}
	for i, account := range accounts {
		reach, err := loadReach(ctx, client, account)
		if err != nil {
			return nil, nil, err
		}
		def, defSubs := storedEntry(stored, account.ID, reach)
		// VAs named by --va-xid mean "only these" for the account they belong to, as they do
		// without a terminal.
		if own := ownedBy(reach, subsFlag); len(own) > 0 {
			def.VaMode, defSubs = vaModeList, own
		}
		reaches[i], forms[i] = reach, accountForm(reach, def, defSubs)
		if _, ok := storedConfig(stored, account.ID); ok && !shows(forms[i], def, defSubs) {
			forms[i], kept[i] = keptForm(reach, def), true
		}
	}
	answers, err := f.AccountsForm("What each bank account notifies", forms)
	if err != nil {
		return nil, nil, err
	}
	cfg := make(map[string]any, len(accounts))
	var subs []string
	for i, ans := range answers {
		if kept[i] {
			subs = append(subs, reaches[i].storedVAs(storedSubs)...)
			continue
		}
		mode := formModes[forms[i].Modes[ans.Mode].Label]
		if !keptAsStored(stored, accounts[i].ID, BankConfigEntry{ReceivePrimary: ans.Primary, VaMode: mode}) {
			cfg[accounts[i].ID] = map[string]any{"receive_primary": ans.Primary, "va_mode": mode}
		}
		if mode == vaModeList {
			for _, v := range ans.Picked {
				subs = append(subs, reaches[i].vas[v].ID)
			}
			subs = append(subs, reaches[i].storedInactive(storedSubs)...)
		}
	}
	return cfg, subs, nil
}

// storedConfig is the bank_config the webhook being edited stores for the account, if any.
func storedConfig(w *Webhook, xid string) (BankConfigEntry, bool) {
	if w == nil {
		return BankConfigEntry{}, false
	}
	e, ok := w.BankConfig[xid]
	return e, ok
}

// shows reports whether form starts on e: the same account-number choice and VA mode, and for
// list at least one of the account's active VAs checked.
func shows(form cmdutil.FormAccount, e BankConfigEntry, subs []string) bool {
	return form.Primary == e.ReceivePrimary &&
		formModes[form.Modes[form.Mode].Label] == e.VaMode &&
		(e.VaMode != vaModeList || len(subs) > 0)
}

// keptForm shows a stored config the form cannot offer, fixed, so it is sent back unchanged.
func keptForm(r accountReach, e BankConfigEntry) cmdutil.FormAccount {
	mode := cmdutil.FormMode{Label: labelAll + " (kept as stored)", Short: "all virtual accounts"}
	switch e.VaMode {
	case vaModeList:
		mode = cmdutil.FormMode{Label: labelList + " (kept as stored)", Short: "stored virtual accounts"}
	case vaModeNone:
		mode = cmdutil.FormMode{Label: labelNone, None: true}
	}
	return cmdutil.FormAccount{
		Title:        bankaccount.Label(r.account),
		Note:         "kept as stored: this form cannot show it",
		PrimaryFixed: true,
		Primary:      e.ReceivePrimary,
		Modes:        []cmdutil.FormMode{mode},
	}
}

// storedVAs keeps the VAs in subs that belong to the account, active or not.
func (r accountReach) storedVAs(subs []string) []string {
	return slices.DeleteFunc(slices.Clone(subs), func(s string) bool {
		return !r.ownsVA(s) && !slices.Contains(r.inactive, s)
	})
}

// storedInactive keeps the account's inactive VAs in subs: the form never shows them, so an
// answer cannot have unchecked them.
func (r accountReach) storedInactive(subs []string) []string {
	return slices.DeleteFunc(slices.Clone(subs), func(s string) bool { return !slices.Contains(r.inactive, s) })
}

// keptAsStored reports whether the webhook being edited already stores e for the account.
func keptAsStored(w *Webhook, xid string, e BankConfigEntry) bool {
	stored, ok := storedConfig(w, xid)
	return ok && stored == e
}

// storedEntry is the config the account has on the webhook being edited, else what a new
// webhook starts with: the account number when it can be notified, and every virtual account.
// An older webhook the account belongs to but with no bank_config for it reads as only_va says,
// narrowed to its VA list when that names any of the account's VAs.
func storedEntry(w *Webhook, xid string, r accountReach) (BankConfigEntry, []string) {
	if w == nil {
		return BankConfigEntry{ReceivePrimary: !r.account.VAOnly, VaMode: vaModeAll}, nil
	}
	own := ownedBy(r, w.SubAccountXIDs)
	if e, ok := w.BankConfig[xid]; ok {
		return e, own
	}
	targeted := (w.BankAccountXID != nil && *w.BankAccountXID == xid) || slices.Contains(w.BankAccountXIDs, xid)
	if !targeted {
		return BankConfigEntry{ReceivePrimary: !r.account.VAOnly, VaMode: vaModeAll}, nil
	}
	e := BankConfigEntry{ReceivePrimary: !w.OnlyVA && !r.account.VAOnly, VaMode: vaModeAll}
	if len(own) > 0 {
		e.VaMode = vaModeList
	}
	return e, own
}

// ownedBy keeps the VAs in subs that belong to the account.
func ownedBy(r accountReach, subs []string) []string {
	return slices.DeleteFunc(slices.Clone(subs), func(s string) bool { return !r.ownsVA(s) })
}

// The VA modes as the form labels them.
const (
	labelAll  = "All, including ones created later"
	labelList = "Only selected"
	labelNone = "None"
)

var formModes = map[string]string{labelAll: vaModeAll, labelList: vaModeList, labelNone: vaModeNone}

// accountForm lays one account out as the web form does:
//
//	VA-only bank          account number fixed off; all or selected VAs
//	can notify VAs        account number on/off; all, selected, or no VAs
//	has VAs, bank can't   same as above
//	no VAs at all         account number fixed on, no VAs; nothing to edit
//
// Selected is offered only when the account has a VA to check. The form refuses an account that
// would notify nothing, so off with no VAs never reaches the server.
func accountForm(r accountReach, def BankConfigEntry, defSubs []string) cmdutil.FormAccount {
	form := cmdutil.FormAccount{Title: bankaccount.Label(r.account), Primary: def.ReceivePrimary}
	if !r.receivesVA() {
		form.Note = "no virtual accounts"
		form.PrimaryFixed, form.Primary = true, true
		form.Modes = []cmdutil.FormMode{{Label: labelNone, None: true}}
		return form
	}

	switch {
	case r.account.VAOnly && len(r.vas) == 0:
		form.Note = "virtual accounts only · none issued yet, so nothing is notified until one is"
	case r.account.VAOnly:
		form.Note = "virtual accounts only"
	default:
		form.Note = r.account.Connection()
	}
	if r.account.VAOnly {
		form.PrimaryFixed, form.Primary = true, false
	}

	form.Modes = []cmdutil.FormMode{{Label: labelAll, Short: "all virtual accounts"}}
	if len(r.vas) > 0 {
		form.Modes = append(form.Modes, cmdutil.FormMode{Label: labelList, Short: "selected virtual accounts", Pick: true})
	}
	if !r.account.VAOnly {
		form.Modes = append(form.Modes, cmdutil.FormMode{Label: labelNone, None: true})
	}
	for i, m := range form.Modes {
		if formModes[m.Label] == def.VaMode {
			form.Mode = i
		}
	}
	for i, v := range r.vas {
		form.VAs = append(form.VAs, cmdutil.FormOption{Label: vapkg.Label(v), Short: v.VA})
		if slices.Contains(defSubs, v.ID) {
			form.Picked = append(form.Picked, i)
		}
	}
	return form
}

// targetingRejections explain the 422 field errors a bank_config the server refuses comes back with.
var targetingRejections = map[string]string{
	"bank_config": "a bank account would be notified of nothing: a virtual-account-only bank needs off:all or off:list, " +
		"an account with no virtual account needs on, and off:none notifies nothing (check with: sepay bank-account get <xid>)",
	"bank_sub_account_xids": "every --va-xid must belong to a targeted bank account, and an account set to list needs " +
		"at least one of its own virtual accounts in --va-xid",
}

// targetingRuleMarkers pick out the 422s that state a "receives nothing" rule, from the messages
// the server's checkVaModes writes; an unknown xid or a VA that is not found keeps its own words.
var targetingRuleMarkers = []string{"receive_primary", "va_mode", "would receive nothing", "do not belong"}

// explainTargeting rewrites those 422 field errors into the rule broken, keeping the server's words.
func explainTargeting(err error) error {
	var he *api.HTTPError
	if !errors.As(err, &he) || he.StatusCode != http.StatusUnprocessableEntity {
		return err
	}
	for field, why := range targetingRejections {
		msg, ok := he.Errors[field]
		if ok && slices.ContainsFunc(targetingRuleMarkers, func(m string) bool { return strings.Contains(msg, m) }) {
			he.Errors[field] = why + " (server: " + msg + ")"
		}
	}
	return err
}

// fetchWebhook reads one webhook as stored.
func fetchWebhook(ctx context.Context, client *api.Client, xid string) (Webhook, error) {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return Webhook{}, err
	}
	env, err := client.Get(ctx, "/webhooks/"+xidSeg, nil)
	if err != nil {
		return Webhook{}, err
	}
	return api.DataInto[Webhook](env)
}

// carryStoredTargeting completes a flag update that configures accounts or VAs. Without
// --bank-mode (storedMode) it resends the stored mode and accounts, which the server needs to
// change a multi webhook's targeting. bank_sub_account_xids is one list for all accounts, replaced
// whole when sent: the stored VAs of targeted accounts this update leaves alone are carried over,
// or the server would widen those accounts to every transaction.
func carryStoredTargeting(ctx context.Context, f *cmdutil.Factory, body map[string]any, xid string, storedMode bool) error {
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	stored, err := fetchWebhook(ctx, client, xid)
	if err != nil {
		return err
	}
	switch mode := stored.BankMode; {
	case !storedMode:
	case mode == "all":
		return &cmdutil.FlagError{Err: errors.New(
			"this webhook targets every bank account; pass --bank-mode single or multi with --bank-account to configure accounts")}
	case mode == "multi":
		body["bank_mode"] = "multi"
		body["bank_account_xids"] = stored.BankAccountXIDs
	case mode == "single":
		if stored.BankAccountXID != nil {
			body["bank_account_xid"] = *stored.BankAccountXID
		}
	}

	subs, _ := body["bank_sub_account_xids"].([]string)
	if len(subs) == 0 {
		return nil
	}
	owner := map[string]string{}
	for _, accountXID := range bodyAccounts(body) {
		// Inactive VAs too: the server counts a stored inactive VA towards an account's list mode.
		vas, err := vapkg.FetchAll(ctx, client, accountXID)
		if err != nil {
			return err
		}
		for _, v := range vas {
			owner[v.ID] = accountXID
		}
	}
	touched := map[string]bool{}
	cfg, _ := body["bank_config"].(map[string]any)
	for accountXID := range cfg {
		touched[accountXID] = true
	}
	for _, s := range subs {
		touched[owner[s]] = true
	}
	for _, s := range stored.SubAccountXIDs {
		// A VA of an account no longer targeted stays out.
		if owner[s] != "" && !touched[owner[s]] && !slices.Contains(subs, s) {
			subs = append(subs, s)
		}
	}
	body["bank_sub_account_xids"] = subs
	return nil
}
