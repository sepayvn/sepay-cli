package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmd/transaction"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// logPickerSize caps the replay picker at one page; deliveries are unbounded.
const logPickerSize = 20

type replayTarget struct {
	TransactionID string `json:"transaction_id"`
	WebhookXID    string `json:"webhook_xid"`
}

type replayRequest struct {
	replayTarget
	Force bool `json:"force,omitempty"`
}

// target returns false when the row is not replayable or misses either half.
func (l Log) target() (replayTarget, bool) {
	if !l.Replayable || l.TransactionID == nil || l.WebhookXID == nil ||
		*l.TransactionID == "" || *l.WebhookXID == "" {
		return replayTarget{}, false
	}
	return replayTarget{TransactionID: *l.TransactionID, WebhookXID: *l.WebhookXID}, true
}

// resolveReplayTarget completes the (transaction, webhook) pair, picking or prompting for the missing half.
func resolveReplayTarget(ctx context.Context, f *cmdutil.Factory, transactionID, webhookXID string) (replayTarget, error) {
	if transactionID != "" && webhookXID != "" {
		return replayTarget{TransactionID: transactionID, WebhookXID: webhookXID}, nil
	}
	if transactionID == "" && !f.CanPrompt() {
		return replayTarget{}, &cmdutil.FlagError{Err: errors.New("a transaction id is required (pass it, or --transaction; see: sepay webhook logs)")}
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return replayTarget{}, err
	}
	q := url.Values{"per_page": {strconv.Itoa(logPickerSize)}}
	if transactionID != "" {
		q.Set("transaction_id", transactionID)
		q.Set("per_page", "100")
	}
	if webhookXID != "" {
		q.Set("webhook_xid", webhookXID)
	}
	env, err := client.Get(ctx, "/webhook-logs", q)
	if err != nil {
		return replayTarget{}, err
	}
	logs, _, err := api.ListInto[Log](env)
	if err != nil {
		return replayTarget{}, err
	}

	// Retries and replays of one delivery share a pair and re-send the same original.
	seen := map[replayTarget]bool{}
	var targets []replayTarget
	var choices []cmdutil.PromptChoice
	for _, l := range logs {
		t, ok := l.target()
		if !ok || seen[t] {
			continue
		}
		seen[t] = true
		targets = append(targets, t)
		choices = append(choices, cmdutil.PromptChoice{
			Label: fmt.Sprintf("%s · %s · %s · txn %s · %s", l.CreatedAt, l.Status, intOrDash(l.ResponseStatusCode),
				t.TransactionID, l.urlLabel()),
			Value: strconv.Itoa(len(targets) - 1),
		})
	}

	switch {
	case len(targets) == 0 && transactionID != "":
		return replayTarget{}, fmt.Errorf("no replayable delivery of transaction %s; see: sepay webhook logs --transaction %s", transactionID, transactionID)
	case len(targets) == 0 && len(logs) == 0:
		return replayTarget{}, &cmdutil.FlagError{Err: errors.New("no webhook logs yet")}
	case len(targets) == 0:
		return replayTarget{}, &cmdutil.FlagError{Err: fmt.Errorf(
			"none of the %d most recent logs can be replayed; pass a transaction id from: sepay webhook logs", len(logs))}
	case len(targets) == 1 && transactionID != "":
		return targets[0], nil
	case !f.CanPrompt():
		xids := make([]string, len(targets))
		for i, t := range targets {
			xids[i] = t.WebhookXID
		}
		return replayTarget{}, &cmdutil.FlagError{Err: fmt.Errorf(
			"transaction %s was delivered to %d webhooks; pass --webhook with one of: %s", transactionID, len(targets), strings.Join(xids, ", "))}
	}
	picked, err := f.SelectChoiceOrFlag("", "webhook", "Delivery to replay", choices)
	if err != nil {
		return replayTarget{}, err
	}
	n, err := strconv.Atoi(picked)
	if err != nil {
		return replayTarget{}, err
	}
	return targets[n], nil
}

func newReplayCmd(f *cmdutil.Factory) *cobra.Command {
	var transactionID, webhookXID string
	var force bool
	cmd := &cobra.Command{
		Use:   "replay [transaction-id]",
		Short: "Re-deliver a logged webhook event",
		Long: `Re-send one delivery to its webhook, addressed by the transaction it carried
and the webhook that received it. Both come from "sepay webhook logs", and from
the txn= and wh= values "sepay webhook listen" prints.

With only a transaction, the webhook is taken from its logged deliveries when
there is one, and asked for on a terminal when there are several. With neither,
a terminal offers the recent replayable deliveries.

On production a replay reaches your live handler, which credits the order again
unless it dedupes. So the transaction, its amount, the URL and the last result
are shown before the confirm; a delivery your handler already acknowledged is
refused unless you pass --force and type the transaction id back; and SePay
caps how often one transaction is replayed, saying when to try again. Reading
the transaction and its last delivery needs the "read" scope on production,
besides "webhook:write".`,
		Example: `  sepay webhook replay <transaction-id> --webhook <webhook-xid>`,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				if transactionID != "" {
					return &cmdutil.FlagError{Err: errors.New("pass the transaction id as an argument or --transaction, not both")}
				}
				transactionID = args[0]
			}
			for flag, v := range map[string]*string{"transaction": &transactionID, "webhook": &webhookXID} {
				*v = strings.TrimSpace(*v)
				if *v == "" && cmd.Flags().Changed(flag) {
					return &cmdutil.FlagError{Err: fmt.Errorf("--%s needs a value", flag)}
				}
			}
			if err := f.CheckProdWrite("replay a webhook delivery", transactionID); err != nil {
				return err
			}
			// Before any lookup: a forced production replay is a terminal-only action. A dry run
			// previews one anywhere, and names this refusal when the production rule let it through.
			production := f.IsProductionTarget()
			if force && production && !f.CanPrompt() {
				if err := f.RefuseUnlessDryRun(&cmdutil.FlagError{Err: fmt.Errorf("--force on production must be confirmed on a terminal, and --yes does not confirm it; %s", f.ReachTheConfirm())}); err != nil {
					return err
				}
			}
			target, err := resolveReplayTarget(cmd.Context(), f, transactionID, webhookXID)
			if err != nil {
				return err
			}
			if production && !f.Global.DryRun {
				if err := previewProductionReplay(cmd.Context(), f, target); err != nil {
					return err
				}
			}
			return runReplay(cmd.Context(), f, replayRequest{replayTarget: target, Force: force}, production)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&transactionID, "transaction", "", "Transaction id of the delivery (same as the argument)")
	fl.StringVar(&webhookXID, "webhook", "", "Webhook xid of the delivery")
	fl.BoolVar(&force, "force", false, "Replay a delivery the handler already acknowledged")
	cmdutil.AddDryRunFlag(cmd, f)
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnScope:    "webhook:write",
		cmdutil.AnnConfirms: "sandbox=always,production=terminal",
	})
}

// previewProductionReplay shows what a production replay re-sends, before the confirm and on its stream.
func previewProductionReplay(ctx context.Context, f *cmdutil.Factory, target replayTarget) error {
	// Unattended without --yes the confirm fails anyway; reading first would only change its exit code.
	if f.Global.Output == "json" || f.Global.Output == "yaml" || (!f.Global.Yes && !f.CanPrompt()) {
		return nil
	}
	out := f.ConfirmOut()
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	p, err := readReplayPreview(ctx, client, target)
	if err != nil {
		return err
	}
	p.print(f, out)
	cmdutil.Fprintf(out, "%s\n", doubleCreditWarning)
	return nil
}

const doubleCreditWarning = "! a handler that does not dedupe may credit this order twice"

// replayPreview is what a replay re-sends: the transaction, its amount, and its last delivery.
type replayPreview struct {
	TransactionID string `json:"transaction_id" yaml:"transaction_id"`
	Amount        string `json:"amount" yaml:"amount"`
	SentTo        string `json:"sent_to" yaml:"sent_to"`
	LastResult    string `json:"last_result" yaml:"last_result"`

	tx transaction.Transaction
}

func readReplayPreview(ctx context.Context, client *api.Client, target replayTarget) (replayPreview, error) {
	txSeg, err := cmdutil.PathSegment("transaction id", target.TransactionID)
	if err != nil {
		return replayPreview{}, err
	}
	env, err := client.Get(ctx, "/transactions/"+txSeg, nil)
	if err != nil {
		return replayPreview{}, err
	}
	tx, err := api.DataInto[transaction.Transaction](env)
	if err != nil {
		return replayPreview{}, err
	}
	env, err = client.Get(ctx, "/webhook-logs", url.Values{
		"transaction_id": {target.TransactionID}, "webhook_xid": {target.WebhookXID}, "per_page": {"1"},
	})
	if err != nil {
		return replayPreview{}, err
	}
	logs, _, err := api.ListInto[Log](env)
	if err != nil {
		return replayPreview{}, err
	}

	p := replayPreview{TransactionID: target.TransactionID, Amount: transaction.SignedAmountText(tx), SentTo: "-", LastResult: "-", tx: tx}
	if len(logs) > 0 {
		l := logs[0]
		p.SentTo = l.urlLabel()
		p.LastResult = fmt.Sprintf("%s · HTTP %s · %s", l.Status, intOrDash(l.ResponseStatusCode), l.CreatedAt)
	}
	return p, nil
}

func (p replayPreview) print(f *cmdutil.Factory, out io.Writer) {
	cmdutil.Fprintf(out, "Transaction  %s\n", p.TransactionID)
	cmdutil.Fprintf(out, "Amount       %s\n", transaction.SignedAmount(f, p.tx))
	cmdutil.Fprintf(out, "Sent to      %s\n", p.SentTo)
	cmdutil.Fprintf(out, "Last result  %s\n", p.LastResult)
}

// dryRunReplay shows the delivery a replay would re-send.
func dryRunReplay(ctx context.Context, f *cmdutil.Factory, client *api.Client, req replayRequest, production bool) error {
	p, err := readReplayPreview(ctx, client, req.replayTarget)
	if err != nil {
		return err
	}
	// The body as JSON sends it: replayRequest embeds an unexported type YAML cannot encode.
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	return f.RenderDryRun(cmdutil.DryRun{
		Request: cmdutil.DryRunRequest{Method: http.MethodPost, Path: "/webhook-logs/replay", Body: body},
		Before:  p,
	}, func() error {
		p.print(f, f.IOStreams.Out)
		if production {
			f.ErrPrintf("%s\n", doubleCreditWarning)
		}
		return nil
	})
}

func runReplay(ctx context.Context, f *cmdutil.Factory, req replayRequest, production bool) error {
	action := fmt.Sprintf("replay transaction %s to webhook %s", req.TransactionID, req.WebhookXID)
	if err := f.ConfirmMutation(action, req.TransactionID, true); err != nil {
		return err
	}
	if req.Force && production {
		if err := f.ConfirmByTyping("forced "+action, req.TransactionID); err != nil {
			return err
		}
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	key, err := f.IdempotencyKey()
	if err != nil {
		return err
	}
	if f.Global.DryRun {
		return dryRunReplay(ctx, f, client, req, production)
	}
	env, err := client.Post(ctx, "/webhook-logs/replay", api.WriteOptions{Body: req, IdempotencyKey: key})
	if err != nil {
		return err
	}
	res, err := api.DataInto[replayResult](env)
	if err != nil {
		return err
	}

	return f.RenderOutput(res, func() error {
		// A listen-mode webhook delivers to the stream, so its log holds no handler response.
		if res.Log == nil || res.Log.Status == logStatusListen {
			f.ErrPrintf("Replayed (%s). Check the result with: sepay webhook logs --transaction %s --webhook %s\n",
				deliveredWord(res.Delivered), req.TransactionID, req.WebhookXID)
			return nil
		}
		if res.Log.ResponseStatusCode == nil {
			if res.Delivered {
				f.ErrPrintf("%s Replayed.\n", f.SuccessIcon())
				return nil
			}
			f.ErrPrintf("! Replayed, but the handler could not be reached%s.\n", errorSuffix(res.Log.ErrorMessage))
			return nil
		}
		if res.Delivered {
			f.ErrPrintf("%s Replayed: handler answered %s in %sms.\n", f.SuccessIcon(), intOrDash(res.Log.ResponseStatusCode), intOrDash(res.Log.ResponseTimeMs))
			return nil
		}
		// The request succeeded; the handler's failure is the result, not a CLI error.
		f.ErrPrintf("! Replayed, but the handler answered %s%s.\n", intOrDash(res.Log.ResponseStatusCode), errorSuffix(res.Log.ErrorMessage))
		return nil
	})
}

func deliveredWord(delivered bool) string {
	if delivered {
		return "delivered"
	}
	return "not delivered"
}

func errorSuffix(msg *string) string {
	if msg == nil || *msg == "" {
		return ""
	}
	return " (" + *msg + ")"
}

// replayResult is the replay response; Log is null when the server could not read it back.
type replayResult struct {
	Delivered bool   `json:"delivered" yaml:"delivered"`
	Message   string `json:"message" yaml:"message"`
	Log       *Log   `json:"log" yaml:"log"`
}
