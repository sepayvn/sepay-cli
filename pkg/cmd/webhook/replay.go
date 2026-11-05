package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// logPickerSize caps the replay picker at one page; deliveries are unbounded.
const logPickerSize = 20

type replayTarget struct {
	TransactionID string `json:"transaction_id"`
	WebhookXID    string `json:"webhook_xid"`
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
	cmd := &cobra.Command{
		Use:   "replay [transaction-id]",
		Short: "Re-deliver a logged webhook event (sandbox only)",
		Long: `Re-send one delivery to its webhook, addressed by the transaction it carried
and the webhook that received it. Both come from "sepay webhook logs", and from
the txn= and wh= values "sepay webhook listen" prints.

With only a transaction, the webhook is taken from its logged deliveries when
there is one, and asked for on a terminal when there are several. With neither,
a terminal offers the recent replayable deliveries.`,
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
			// Before the lookup: production has no replay endpoint, and --yes skips the confirm.
			if err := f.AssertSandboxOnly("webhook replay"); err != nil {
				return err
			}
			target, err := resolveReplayTarget(cmd.Context(), f, transactionID, webhookXID)
			if err != nil {
				return err
			}
			return runReplay(cmd.Context(), f, target)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&transactionID, "transaction", "", "Transaction id of the delivery (same as the argument)")
	fl.StringVar(&webhookXID, "webhook", "", "Webhook xid of the delivery")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnEnvs:     "sandbox",
		cmdutil.AnnConfirms: "always",
	})
}

func runReplay(ctx context.Context, f *cmdutil.Factory, target replayTarget) error {
	if err := f.ConfirmMutation(fmt.Sprintf("replay transaction %s to webhook %s", target.TransactionID, target.WebhookXID), true); err != nil {
		return err
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	key, err := f.IdempotencyKey()
	if err != nil {
		return err
	}
	env, err := client.Post(ctx, "/webhook-logs/replay", api.WriteOptions{Body: target, IdempotencyKey: key})
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
				deliveredWord(res.Delivered), target.TransactionID, target.WebhookXID)
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
