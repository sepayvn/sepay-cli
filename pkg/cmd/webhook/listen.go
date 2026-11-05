package webhook

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sepayvn/sepay-cli/internal/listen"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
)

func newListenCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "listen",
		Aliases: []string{"tail"},
		Short:   "Stream events to a local handler over SSE (sandbox only)",
		Long: `Hold one outbound SSE stream and POST every webhook delivery to a local handler,
so an integration can be built without exposing your machine.

Each event carries the same body bytes and the same headers your endpoint just
received, so your handler runs its real path, including its own signature check.
Credential headers are replayed to a loopback handler; for any other address they
are withheld unless you pass ` + "`--forward-credentials`" + `. The signature is always sent.

It runs on sandbox only: a production webhook delivers to its own URL.

It mirrors deliveries, so it needs a webhook that targets the bank account, and it
reports first attempts and replays only: SePay's own retries never reach the stream.
Every attempt SePay made is in ` + "`sepay webhook logs`" + `.

A forward your handler fails is re-sent locally on SePay's schedule (` + "`--forward-retries 0`" + `
delivers once). Those attempts are the CLI's: they replay the original bytes and
signature, and they end with the session.

Body reference: https://developer.sepay.vn/en/sepay-webhooks/tich-hop-webhook.`,
		Example: `  # forward every delivery to a local handler
  sepay webhook listen --forward localhost:3000

  # only money in for one webhook, printing each body
  sepay webhook listen --forward localhost:3000 --webhook <webhook-xid> --events payment_in --print`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := f.AssertSandboxOnly("webhook listen"); err != nil {
				return err
			}
			return runListen(cmd, f)
		},
	}
	listen.AddFlags(cmd)
	listen.DescribeEvents(cmd, listenEvents)
	cmd.Flags().String("webhook", "", "Only forward deliveries of this webhook xid (see: sepay webhook list)")
	return cmdutil.Annotate(cmd, map[string]string{cmdutil.AnnEnvs: "sandbox"})
}

// mintPath mints a webhook listen session for "listen" and "test".
const mintPath = "/webhooks/listen"

func runListen(cmd *cobra.Command, f *cmdutil.Factory) error {
	opts, err := listen.ReadOptions(cmd)
	if err != nil {
		return err
	}
	if err := listen.CheckEvents(opts.Events, listenEvents); err != nil {
		return err
	}
	forwardAddr, err := listen.ResolveForward(f, opts.Forward)
	if err != nil {
		return err
	}
	opts.Forward = forwardAddr

	webhookXid, _ := cmd.Flags().GetString("webhook")

	// retry_conditions only comes with the webhook list.
	set := fetchWebhookSet(cmd.Context(), f)
	// Only when retry runs, or this note would contradict the guard's.
	if set.err != nil && listen.RetryEnabled(opts) {
		fmt.Fprintln(f.IOStreams.ErrOut,
			"note: could not read your webhooks, so only a transport failure is retried")
	}

	return listen.Run(cmd, f, listen.Source{
		MintPath:   mintPath,
		RevokePath: mintPath,
		Noun:       "events",
		Scope:      "webhook",
		Describe: func(ctx context.Context, f *cmdutil.Factory) {
			describeCoverage(ctx, f, set)
		},
		Accept:      acceptWebhook(webhookXid),
		Tag:         webhookTag,
		RetryNon2xx: retryNon2xx(set),
	}, opts)
}

// retryNon2xx reports whether a frame's non-2xx delivery is re-sent, per its webhook's retry_conditions.
// An unnamed origin falls back to any eligible webhook; nil when the list is unavailable.
func retryNon2xx(set webhookSet) func(listen.Frame) bool {
	if set.err != nil || len(set.hooks) == 0 {
		return nil
	}
	enabled := make(map[string]bool, len(set.hooks))
	anyEnabled := false
	for _, w := range set.hooks {
		enabled[w.ID] = w.retriesNon2xx()
		// A partner-managed row delivers on the partner's stream.
		if bool(w.Active) && !w.ManagedByPartner && w.retriesNon2xx() {
			anyEnabled = true
		}
	}
	return func(fr listen.Frame) bool {
		if on, ok := enabled[webhookXID(fr)]; ok {
			return on
		}
		return anyEnabled
	}
}

// acceptWebhook narrows the stream to one webhook; an unknown origin passes through.
func acceptWebhook(want string) func(listen.Frame) bool {
	if want == "" {
		return nil
	}
	return func(fr listen.Frame) bool {
		xid := webhookXID(fr)
		return xid == "" || xid == want
	}
}

// webhookXID tells a delivery from its fan-out siblings; empty on older backends.
func webhookXID(fr listen.Frame) string {
	var envelope struct {
		WebhookXID string `json:"webhook_xid"`
	}
	if err := json.Unmarshal(fr.Raw, &envelope); err != nil {
		return ""
	}
	return envelope.WebhookXID
}

// subAccount returns the VA a delivery arrived on, empty for the account number itself.
func subAccount(fr listen.Frame) string {
	var p struct {
		SubAccount string `json:"subAccount"`
	}
	if err := json.Unmarshal(fr.Payload, &p); err != nil {
		return ""
	}
	return p.SubAccount
}

// webhookTag prints both ids whole, so each pastes into `sepay transaction get` / `sepay webhook get`.
func webhookTag(io *iostreams.IOStreams, fr listen.Frame) string {
	return listen.Tag(io, "txn", transactionXID(fr)) +
		listen.Tag(io, "wh", webhookXID(fr))
}

// transactionXID reads the envelope: the signed payload holds only an internal numeric id. Empty on older backends.
func transactionXID(fr listen.Frame) string {
	var envelope struct {
		TransactionXID string `json:"transaction_xid"`
	}
	if err := json.Unmarshal(fr.Raw, &envelope); err != nil {
		return ""
	}
	return envelope.TransactionXID
}
