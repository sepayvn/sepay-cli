package webhook

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmd/bankaccount"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
)

// va_credit is a delivery-filter input, not a frame type: the hub types a VA credit payment_in.
const (
	eventPaymentIn  = "payment_in"
	eventPaymentOut = "payment_out"
	eventVACredit   = "va_credit"
)

// listenEvents are the frame types, so the only values --events accepts.
var listenEvents = []string{eventPaymentIn, eventPaymentOut}

var streamEvents = []string{eventPaymentIn, eventPaymentOut, eventVACredit}

// kindLabels keep va_credit from reading like an --events value.
var kindLabels = map[string]string{
	eventVACredit: "virtual-account transfers",
}

func joinKinds(kinds []string) string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		if label, ok := kindLabels[k]; ok {
			out = append(out, label)
			continue
		}
		out = append(out, k)
	}
	return strings.Join(out, ", ")
}

// anyAccount is an account no webhook names; only a company-wide webhook delivers for it.
const anyAccount = ""

const reasonEmptyVAList = "its webhook forwards only the virtual accounts on its list, and that list is empty"

type delivery struct {
	account string
	event   string
	hasCode bool
}

func (d delivery) onVA() bool { return d.event == eventVACredit }

type match int

const reasonPartnerManaged = "its webhook belongs to a partner integration, so its deliveries go to that partner, not this stream"

const (
	matchElsewhere match = iota
	matchDrops
	matchDelivers
	// a filter depends on data the webhook list lacks
	matchUnknown
)

// delivers reports what w does with d, in the backend's filter order; inactive webhooks are the caller's to skip.
func (w Webhook) delivers(d delivery) (match, string) {
	// A partner-provisioned webhook delivers on the partner's stream, never this one.
	if w.ManagedByPartner {
		return matchDrops, reasonPartnerManaged
	}

	// Defer matchUnknown: a provable drop holds whatever an undecidable filter would do.
	undecided := false

	switch w.BankMode {
	case "all":
	case "single":
		if w.BankAccountXID == nil || *w.BankAccountXID != d.account {
			return matchElsewhere, ""
		}
	case "multi":
		// bank_account_id = 0: sandbox selects it for every account, production skips it.
		undecided = !slices.Contains(w.BankAccountXIDs, d.account)
	default:
		return matchUnknown, ""
	}

	switch w.EventType {
	case "In_only":
		if d.event == eventPaymentOut {
			return matchDrops, "its webhook forwards incoming transfers only"
		}
	case "Out_only":
		if d.event != eventPaymentOut {
			return matchDrops, "its webhook forwards outgoing transfers only"
		}
	}

	if cfg, ok := w.BankConfig[d.account]; ok {
		switch {
		case !d.onVA() && !cfg.ReceivePrimary:
			return matchDrops, "its webhook is set to skip transfers to the account number itself on this account"
		case d.onVA() && cfg.VaMode == "none":
			return matchDrops, "its webhook is set to skip virtual-account transfers on this account"
		case d.onVA() && cfg.VaMode == "list":
			if len(w.SubAccountXIDs) == 0 {
				return matchDrops, reasonEmptyVAList
			}
			// A VA list holds xids while an event names a VA number.
			undecided = true
		}
	} else if w.OnlyVA {
		// No per-account config: the backend falls back to only_va.
		if !d.onVA() {
			return matchDrops, "its webhook covers virtual-account transfers only, so a transfer to the account number itself is not delivered"
		}
		if len(w.SubAccountXIDs) == 0 {
			return matchDrops, reasonEmptyVAList
		}
		undecided = true
	}

	if !d.hasCode {
		if w.SkipIfNoCode {
			return matchDrops, "its webhook skips events without a pay code, and this event carries none"
		}
		if len(w.PrefixFilters) > 0 {
			return matchDrops, "its webhook only forwards pay codes starting with " +
				strings.Join(w.PrefixFilters, " or ") + ", and this event carries no code"
		}
	}

	if undecided {
		return matchUnknown, ""
	}
	return matchDelivers, ""
}

// deliverable reports whether any webhook delivers d, else why not; undecidable counts as deliverable.
func deliverable(hooks []Webhook, d delivery) (bool, string) {
	active, reason := 0, ""
	for _, w := range hooks {
		if !bool(w.Active) {
			continue
		}
		active++
		m, why := w.delivers(d)
		if m == matchDelivers || m == matchUnknown {
			return true, ""
		}
		if m == matchDrops && reason == "" {
			reason = why
		}
	}
	switch {
	case reason != "":
		return false, reason
	case active == 0:
		return false, "no active webhook covers this company"
	default:
		return false, "no active webhook targets this bank account"
	}
}

// coverageFor reports which streamEvents reach account, else why not. Code filters are ignored:
// real traffic may carry a pay code.
func coverageFor(hooks []Webhook, account string) ([]string, string) {
	var kinds []string
	reason := ""
	for _, event := range streamEvents {
		ok, why := deliverable(hooks, delivery{account: account, event: event, hasCode: true})
		switch {
		case ok:
			kinds = append(kinds, event)
		case reason == "":
			reason = why
		}
	}
	return kinds, reason
}

func activeWebhooks(hooks []Webhook) []Webhook {
	out := make([]Webhook, 0, len(hooks))
	for _, w := range hooks {
		if bool(w.Active) {
			out = append(out, w)
		}
	}
	return out
}

func companyWide(hooks []Webhook) []Webhook {
	out := make([]Webhook, 0, len(hooks))
	for _, w := range hooks {
		if w.BankMode == "all" {
			out = append(out, w)
		}
	}
	return out
}

func targetedAccounts(hooks []Webhook) []string {
	seen := map[string]bool{}
	for _, w := range hooks {
		if w.BankAccountXID != nil && *w.BankAccountXID != "" {
			seen[*w.BankAccountXID] = true
		}
		for _, xid := range w.BankAccountXIDs {
			seen[xid] = true
		}
	}
	out := make([]string, 0, len(seen))
	for xid := range seen {
		out = append(out, xid)
	}
	sort.Strings(out)
	return out
}

// webhookSet is one listen session's webhook list; err keeps "could not ask" apart from "none configured".
type webhookSet struct {
	hooks []Webhook
	err   error
}

func fetchWebhookSet(ctx context.Context, f *cmdutil.Factory) webhookSet {
	client, err := f.NewAPIClient()
	if err != nil {
		return webhookSet{err: err}
	}
	hooks, err := fetchAllWebhooks(ctx, client)
	return webhookSet{hooks: hooks, err: err}
}

// describeCoverage states per account what the stream will deliver, so a silent session reads as a config gap.
func describeCoverage(ctx context.Context, f *cmdutil.Factory, set webhookSet) {
	if set.err != nil {
		return
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return
	}
	hooks := activeWebhooks(set.hooks)
	out := f.IOStreams.ErrOut

	if len(hooks) == 0 {
		fmt.Fprintln(out, "! No active webhook covers this company, so no event can reach this stream.")
		fmt.Fprintln(out, "  Create one: sepay webhook create --url https://example.com/hook --bank-mode all")
		fmt.Fprintln(out, "  Or, in sandbox, one you only watch here: sepay webhook create --listen-only --bank-mode all")
		return
	}

	remainder, _ := coverageFor(companyWide(hooks), anyAccount)
	named := targetedAccounts(hooks)
	if len(named) == 0 && len(remainder) > 0 {
		cmdutil.Fprintf(out, "  %s for every bank account of this company.\n", deliveringPhrase(remainder))
		return
	}

	accounts, err := bankaccount.FetchAll(ctx, client)
	if err != nil {
		accounts = nil
	}
	label := func(xid string) string {
		for _, a := range accounts {
			if a.ID == xid {
				return bankaccount.Label(a)
			}
		}
		return xid
	}

	full := map[string]bool{}
	var notes []string
	for _, xid := range named {
		kinds, why := coverageFor(hooks, xid)
		switch {
		case len(kinds) == len(streamEvents):
			full[xid] = true
		case len(kinds) > 0:
			notes = append(notes, label(xid)+" — delivering "+joinKinds(kinds)+" only")
		default:
			notes = append(notes, label(xid)+" — "+why)
		}
	}
	if len(full) > 0 {
		cmdutil.Fprintf(out, "  Delivering for: %s\n", strings.Join(sortedLabels(full, label), ", "))
	}
	for _, s := range notes {
		cmdutil.Fprintf(out, "  %s.\n", s)
	}
	if len(remainder) == 0 {
		fmt.Fprintln(out, "  Any other bank account of this company produces no events.")
		return
	}
	cmdutil.Fprintf(out, "  %s for every other bank account of this company.\n", deliveringPhrase(remainder))
}

func deliveringPhrase(kinds []string) string {
	if len(kinds) == len(streamEvents) {
		return "Delivering"
	}
	return "Delivering " + joinKinds(kinds)
}

func sortedLabels(set map[string]bool, label func(string) string) []string {
	out := make([]string, 0, len(set))
	for xid := range set {
		out = append(out, label(xid))
	}
	sort.Strings(out)
	return out
}

// assertDeliverable refuses a test event the delivery filter would drop, which would otherwise surface
// as a handler timeout.
func assertDeliverable(ctx context.Context, client *api.Client, opts testOptions) error {
	hooks, err := fetchAllWebhooks(ctx, client)
	if err != nil {
		return nil // the check is an aid, not a gate
	}
	// webhook test sends no pay code, so skip_if_no_code and prefix filters drop it for certain.
	ok, why := deliverable(hooks, delivery{account: opts.bankAccount, event: opts.event})
	if ok {
		return nil
	}
	return &cmdutil.FlagError{Err: fmt.Errorf(
		"this event would not be delivered: %s.\n"+
			"  A webhook delivers an event only when every filter on it matches: the bank account,\n"+
			"  the event direction, the virtual-account rules and the pay code rules.\n"+
			"  List them with: sepay webhook list\n"+
			"  Cover every account without sending HTTP anywhere:\n"+
			"    sepay webhook create --listen-only --bank-mode all", why)}
}
