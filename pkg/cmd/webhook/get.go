package webhook

import (
	"context"
	"sort"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmd/bankaccount"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

func newGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get <webhook-xid>",
		Short: "Show a webhook",
		Long: `Show one webhook with its destination, targeting, filters and auth settings.

<webhook-xid> is the ID column of "sepay webhook list". Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			xid, err := pickWebhook(cmd.Context(), f, cmdutil.FirstArg(args))
			if err != nil {
				return err
			}
			return runGet(cmd.Context(), f, xid)
		},
	}
}

func runGet(ctx context.Context, f *cmdutil.Factory, xid string) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/webhooks/"+xidSeg, nil)
	if err != nil {
		return err
	}
	w, err := api.DataInto[Webhook](env)
	if err != nil {
		return err
	}
	w = w.redacted()
	return f.RenderOutput(w, func() error { return renderWebhookDetail(ctx, f, w) })
}

// renderWebhookDetail prints the human detail table, labelling bank-account xids (falling back to the xid).
func renderWebhookDetail(ctx context.Context, f *cmdutil.Factory, w Webhook) error {
	labels := webhookBankLabels(ctx, f, w)
	t := jsonfmt.NewTable("FIELD", "VALUE").Truncatable("VALUE")
	t.AddRow("ID", w.ID)
	t.AddRow("Name", w.Name)
	t.AddRow("URL", w.urlLabel())
	t.AddRow("Event", w.EventType)
	t.AddRow("Auth", w.AuthenType)
	t.AddRow("Target", w.BankMode)
	if w.BankAccountXID != nil {
		t.AddRow("Bank account", labelOr(labels, *w.BankAccountXID))
	}
	for _, xid := range w.BankAccountXIDs {
		t.AddRow("Bank account", labelOr(labels, xid))
	}
	for _, xid := range sortedKeys(w.BankConfig) {
		c := w.BankConfig[xid]
		primary := "off"
		if c.ReceivePrimary {
			primary = "on"
		}
		t.AddRow("Config "+labelOr(labels, xid), "primary="+primary+" va="+c.VaMode)
	}
	t.AddRow("Only VA", f.YesNo(w.OnlyVA))
	t.AddRow("Verify payment", f.YesNo(w.IsVerifyPayment))
	t.AddRow("Skip if no code", f.YesNo(w.SkipIfNoCode))
	t.AddRow("Active", f.YesNo(bool(w.Active)))
	t.AddRow("Created", w.CreatedAt)
	return f.RenderTable(t)
}

// webhookBankLabels returns xid->label for the targeted accounts, or nil on failure (best-effort).
func webhookBankLabels(ctx context.Context, f *cmdutil.Factory, w Webhook) map[string]string {
	if w.BankAccountXID == nil && len(w.BankAccountXIDs) == 0 && len(w.BankConfig) == 0 {
		return nil
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return nil
	}
	if w.BankAccountXID != nil && len(w.BankAccountXIDs) == 0 && len(w.BankConfig) == 0 {
		a, ferr := bankaccount.Fetch(ctx, client, *w.BankAccountXID)
		if ferr != nil {
			return nil
		}
		return map[string]string{a.ID: bankaccount.Label(a)}
	}
	accts, err := bankaccount.FetchAll(ctx, client)
	if err != nil {
		return nil
	}
	m := make(map[string]string, len(accts))
	for _, a := range accts {
		m[a.ID] = bankaccount.Label(a)
	}
	return m
}

func labelOr(labels map[string]string, xid string) string {
	if l, ok := labels[xid]; ok {
		return l
	}
	return xid
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
