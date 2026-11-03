// Package webhook implements "sepay webhook" and its verbs (listen, test,
// list, get, create, update, delete, logs, replay).
package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/config"
	bankaccount "github.com/sepayvn/sepay-cli/pkg/cmd/bank-account"
	"github.com/sepayvn/sepay-cli/pkg/cmd/sandbox"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"
	"github.com/sepayvn/sepay-cli/pkg/listen"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Webhook is a webhook from GET /v2/webhooks; secret fields are set only by "get".
type Webhook struct {
	ID              string                     `json:"id" yaml:"id"`
	Name            string                     `json:"name" yaml:"name"`
	EventType       string                     `json:"event_type" yaml:"event_type"`
	AuthenType      string                     `json:"authen_type" yaml:"authen_type"`
	WebhookURL      string                     `json:"webhook_url" yaml:"webhook_url"`
	DeliveryMode    string                     `json:"delivery_mode,omitempty" yaml:"delivery_mode,omitempty"`
	IsVerifyPayment bool                       `json:"is_verify_payment" yaml:"is_verify_payment"`
	SkipIfNoCode    bool                       `json:"skip_if_no_code" yaml:"skip_if_no_code"`
	OnlyVA          bool                       `json:"only_va" yaml:"only_va"`
	BankMode        string                     `json:"bank_mode" yaml:"bank_mode"`
	BankAccountXID  *string                    `json:"bank_account_xid" yaml:"bank_account_xid"`
	BankAccountXIDs []string                   `json:"bank_account_xids,omitempty" yaml:"bank_account_xids,omitempty"`
	BankConfig      map[string]BankConfigEntry `json:"bank_config,omitempty" yaml:"bank_config,omitempty"`
	SubAccountXIDs  []string                   `json:"bank_sub_account_xids" yaml:"bank_sub_account_xids"`
	PrefixFilters   []string                   `json:"prefix_filters" yaml:"prefix_filters"`
	AlertThreshold  *int                       `json:"alert_threshold" yaml:"alert_threshold"`
	// Server enum ("failure", "recovery"); null when alerting was never set up. No omitempty.
	AlertEventTypes []string `json:"alert_event_types" yaml:"alert_event_types"`
	// No omitempty: null means "never configured for non-2xx retries".
	RetryConditions *RetryConditions `json:"retry_conditions" yaml:"retry_conditions"`
	Active          api.FlexBool     `json:"active" yaml:"active"`
	CreatedAt       string           `json:"created_at" yaml:"created_at"`

	// Provisioned by an onboarding partner; deliveries go to the partner's stream. No omitempty.
	ManagedByPartner bool `json:"managed_by_partner" yaml:"managed_by_partner"`

	RequestContentType   string `json:"request_content_type,omitempty" yaml:"request_content_type,omitempty"`
	APIKey               string `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	SecretKey            string `json:"secret_key,omitempty" yaml:"secret_key,omitempty"`
	OAuth2ClientID       string `json:"oauth2_client_id,omitempty" yaml:"oauth2_client_id,omitempty"`
	OAuth2ClientSecret   string `json:"oauth2_client_secret,omitempty" yaml:"oauth2_client_secret,omitempty"`
	OAuth2AccessTokenURL string `json:"oauth2_access_token_url,omitempty" yaml:"oauth2_access_token_url,omitempty"`
}

// RetryConditions names the delivery outcomes SePay re-sends beyond a transport failure.
type RetryConditions struct {
	Non2xxStatusCode int `json:"non_2xx_status_code" yaml:"non_2xx_status_code"`
}

func (w Webhook) retriesNon2xx() bool {
	return w.RetryConditions != nil && w.RetryConditions.Non2xxStatusCode != 0
}

// deliveryModeListen marks a sandbox webhook with no HTTP destination; empty means HTTP.
const deliveryModeListen = "listen"

const deliveryModeHTTP = "http"

// logStatusListen is the webhook-log status of a stream delivery (a different vocabulary from delivery_mode).
const logStatusListen = "listen"

func (w Webhook) listenOnly() bool { return w.DeliveryMode == deliveryModeListen }

func (l Log) urlLabel() string {
	if l.Status == logStatusListen {
		return "(listen-only)"
	}
	return jsonfmt.OrDash(l.RequestURL)
}

func (w Webhook) urlLabel() string {
	if w.listenOnly() {
		return "(listen-only)"
	}
	return w.WebhookURL
}

const redactedMark = "***"

// redacted masks the auth secrets so --json / -o yaml never carry them.
func (w Webhook) redacted() Webhook {
	mask := func(s string) string {
		if s == "" {
			return ""
		}
		return redactedMark
	}
	w.APIKey = mask(w.APIKey)
	w.SecretKey = mask(w.SecretKey)
	w.OAuth2ClientSecret = mask(w.OAuth2ClientSecret)
	return w
}

// BankConfigEntry is one account's targeting config in a multi-account webhook.
type BankConfigEntry struct {
	ReceivePrimary bool   `json:"receive_primary" yaml:"receive_primary"`
	VaMode         string `json:"va_mode" yaml:"va_mode"`
}

// Log is one delivery-log entry from GET /v2/webhook-logs, addressed by transaction + webhook.
type Log struct {
	TransactionID      *string `json:"transaction_id" yaml:"transaction_id"`
	WebhookXID         *string `json:"webhook_xid" yaml:"webhook_xid"`
	Replayable         bool    `json:"replayable" yaml:"replayable"`
	Status             string  `json:"status" yaml:"status"`
	Type               string  `json:"type" yaml:"type"`
	IsReplay           bool    `json:"is_replay" yaml:"is_replay"`
	RequestMethod      string  `json:"request_method" yaml:"request_method"`
	RequestURL         string  `json:"request_url" yaml:"request_url"`
	ConnectSuccess     bool    `json:"connect_success" yaml:"connect_success"`
	ResponseStatusCode *int    `json:"response_status_code" yaml:"response_status_code"`
	ResponseTimeMs     *int    `json:"response_time_ms" yaml:"response_time_ms"`
	ErrorMessage       *string `json:"error_message" yaml:"error_message"`
	ErrorCode          *int    `json:"error_code" yaml:"error_code"`
	CreatedAt          string  `json:"created_at" yaml:"created_at"`
}

// NewCmdWebhook builds the "sepay webhook" command group.
func NewCmdWebhook(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "webhook <command>",
		Aliases: []string{"wh"},
		Short:   "Manage webhooks and stream events to a local handler",
	}
	cmd.AddCommand(
		newListenCmd(f),
		newTestCmd(f),
		newListCmd(f),
		newGetCmd(f),
		newCreateCmd(f),
		newUpdateCmd(f),
		newDeleteCmd(f),
		newLogsCmd(f),
		newReplayCmd(f),
	)
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnGuidance: "Writing a handler? `sepay docs webhook-payload` has the event body, the signature headers, and what to return.",
	})
}

func newListenCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "listen",
		Aliases: []string{"tail"},
		Short:   "Stream events to a local handler over SSE",
		Long: `Hold one outbound SSE stream and POST every webhook delivery to a local handler,
so an integration can be built without exposing your machine.

Each event carries the same body bytes and the same headers your endpoint just
received, so your handler runs its real path — including its own signature check.
Credential headers are replayed to a loopback handler; for any other address they
are withheld unless you pass ` + "`--forward-credentials`" + `. The signature is always sent.

It mirrors deliveries, so it needs a webhook that targets the bank account, and it
reports first attempts and replays only — SePay's own retries never reach the stream.
Every attempt SePay made is in ` + "`sepay webhook logs`" + `.

A forward your handler fails is re-sent locally on SePay's schedule (` + "`--forward-retries 0`" + `
delivers once). Those attempts are the CLI's: they replay the original bytes and
signature, and they end with the session.

Body reference: ` + "`sepay docs webhook-payload`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runListen(cmd, f)
		},
	}
	listen.AddFlags(cmd)
	listen.DescribeEvents(cmd, listenEvents)
	cmd.Flags().String("webhook", "", "Only forward deliveries of this webhook xid (see: sepay webhook list)")
	return cmd
}

func newTestCmd(f *cmdutil.Factory) *cobra.Command {
	var (
		forwardAddr, event, bankAccount, vaNumber, webhookXID string
		forwardCreds                                          bool
		amount                                                int64
		timeout                                               time.Duration
		expectStatus                                          int
	)
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Fire an event and assert your handler responds (sandbox only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := resolveTestOptions(cmd, f, testOptions{
				forward: forwardAddr, forwardCreds: forwardCreds,
				event: event, bankAccount: bankAccount,
				va: vaNumber, amount: amount, timeout: timeout, expectStatus: expectStatus,
				webhook: webhookXID,
			})
			if err != nil {
				return err
			}
			return runTest(cmd.Context(), f, opts)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&forwardAddr, "forward", "", "Local handler address, e.g. localhost:3000 (required)")
	fl.BoolVar(&forwardCreds, "forward-credentials", false,
		"Replay credential headers (Authorization, X-Secret-Key) to a non-loopback handler; they are withheld by default")
	fl.StringVar(&event, "event", "payment_in", "Event type to fire: "+strings.Join(sandbox.EventTypes(), ", "))
	fl.StringVar(&bankAccount, "bank-account", "", "Sandbox bank account xid to fire under")
	fl.Int64Var(&amount, "amount", 0, "Amount in VND")
	fl.StringVar(&vaNumber, "va", "", "Virtual account number (for va_credit)")
	fl.StringVar(&webhookXID, "webhook", "", "Judge only this webhook's delivery (see: sepay webhook list)")
	fl.DurationVar(&timeout, "timeout", 10*time.Second, "How long to wait for the handler")
	fl.IntVar(&expectStatus, "expect-status", 0, "HTTP status to assert (0 = any 2xx)")
	cmdutil.MarkRequired(cmd, "forward", "amount")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnEnvs: "sandbox",
		cmdutil.AnnGuidance: "It fabricates a transaction, which has no production meaning — " +
			"to check a live endpoint, observe real events with `sepay webhook listen --env production`.",
	})
}

func newListCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List webhooks",
		Args:    cobra.NoArgs,
	}
	collect := cmdutil.BindListFlags(cmd.Flags(), map[string]string{
		"active": "Filter by status: 0|1",
	})
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		// The API ignores an unknown filter value and returns an unfiltered page.
		if err := cmdutil.ValidateChoice(cmd, "active", []string{"0", "1"}); err != nil {
			return err
		}
		return runList(cmd.Context(), f, collect())
	}
	return cmd
}

func newGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get [xid]",
		Short: "Show a webhook",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			xid, err := pickWebhook(cmd.Context(), f, cmdutil.FirstArg(args))
			if err != nil {
				return err
			}
			return runGet(cmd.Context(), f, xid)
		},
	}
}

const maxWebhookPickerPages = 50

func fetchAllWebhooks(ctx context.Context, client *api.Client) ([]Webhook, error) {
	var all []Webhook
	for page := 1; page <= maxWebhookPickerPages; page++ {
		env, err := client.Get(ctx, "/webhooks", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		hooks, pg, err := api.ListInto[Webhook](env)
		if err != nil {
			return nil, err
		}
		all = append(all, hooks...)
		if pg == nil || !pg.HasMore {
			break
		}
	}
	return all, nil
}

// pickWebhook returns val, or prompts for a webhook on an interactive terminal.
func pickWebhook(ctx context.Context, f *cmdutil.Factory, val string) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.CanPrompt() {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("a webhook xid is required")}
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return "", err
	}
	hooks, err := fetchAllWebhooks(ctx, client)
	if err != nil {
		return "", err
	}
	if len(hooks) == 0 {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("no webhooks found; create one first (sepay webhook create)")}
	}
	choices := make([]cmdutil.PromptChoice, 0, len(hooks))
	for _, w := range hooks {
		label := w.WebhookURL
		if w.Name != "" {
			label = w.Name + " · " + w.WebhookURL
		}
		choices = append(choices, cmdutil.PromptChoice{Label: label + " [" + w.BankMode + "]", Value: w.ID})
	}
	return f.SelectChoiceOrFlag("", "webhook", "Webhook", choices)
}

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

// logTimeLayouts are read by the server in its own zone.
var logTimeLayouts = []string{"2006-01-02", "2006-01-02 15:04", "2006-01-02 15:04:05", time.RFC3339}

// logTimeFlag validates --since/--until; a duration is sent in UTC so the server cannot reinterpret its zone.
func logTimeFlag(flag, v string, allowDuration bool) (string, error) {
	v = strings.TrimSpace(v)
	if allowDuration {
		if d, err := time.ParseDuration(v); err == nil {
			if d <= 0 {
				return "", &cmdutil.FlagError{Err: fmt.Errorf("--%s must be a positive duration or a date, e.g. 24h or 2026-11-01", flag)}
			}
			return time.Now().UTC().Add(-d).Format(time.RFC3339), nil
		}
	}
	for _, layout := range logTimeLayouts {
		if _, err := time.Parse(layout, v); err == nil {
			return v, nil
		}
	}
	want := "a date or datetime, e.g. 2026-11-01 or \"2026-11-01 18:30\""
	if allowDuration {
		want = "a duration or a date, e.g. 24h or 2026-11-01"
	}
	return "", &cmdutil.FlagError{Err: fmt.Errorf("--%s must be %s, got %q", flag, want, v)}
}

func newLogsCmd(f *cmdutil.Factory) *cobra.Command {
	var (
		webhookXID, transactionID, status, since, until string
		perPage, page                                   int
	)
	cmd := &cobra.Command{
		Use:     "logs [webhook-xid]",
		Aliases: []string{"log"},
		Short:   "List webhook delivery logs",
		Long: `List delivery logs, newest first. With no argument it lists every webhook's;
an xid narrows it to one, the same as --webhook. --transaction narrows it to the
deliveries of one transaction (the txn= value "webhook listen" prints).

A delivery is addressed by its transaction and webhook: pass both to
"sepay webhook replay" to re-send it. --since takes a duration (24h) or a date or
datetime; --until a date or datetime, a bare date meaning the end of that day.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cmdutil.ValidateChoice(cmd, "status", []string{"success", "failed", logStatusListen, "skipped"}); err != nil {
				return err
			}
			// Accept the positional id so cobra does not answer "unknown command <xid>".
			if len(args) > 0 {
				if webhookXID != "" {
					return &cmdutil.FlagError{Err: errors.New("pass the webhook xid as an argument or --webhook, not both")}
				}
				webhookXID = args[0]
			}
			q := url.Values{}
			if webhookXID != "" {
				q.Set("webhook_xid", webhookXID)
			}
			if v := strings.TrimSpace(transactionID); v != "" {
				q.Set("transaction_id", v)
			} else if cmd.Flags().Changed("transaction") {
				return &cmdutil.FlagError{Err: errors.New("--transaction needs a transaction id")}
			}
			if status != "" {
				q.Set("status", status)
			}
			if cmd.Flags().Changed("since") {
				v, err := logTimeFlag("since", since, true)
				if err != nil {
					return err
				}
				q.Set("since", v)
			}
			if cmd.Flags().Changed("until") {
				v, err := logTimeFlag("until", until, false)
				if err != nil {
					return err
				}
				q.Set("until", v)
			}
			if perPage > 0 {
				q.Set("per_page", strconv.Itoa(perPage))
			}
			if page > 0 {
				q.Set("page", strconv.Itoa(page))
			}
			return runLogs(cmd.Context(), f, q)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&webhookXID, "webhook", "", "Filter by webhook xid")
	fl.StringVar(&transactionID, "transaction", "", "Filter by transaction id")
	fl.StringVar(&status, "status", "", "Filter by delivery status: success|failed|listen|skipped")
	fl.StringVar(&since, "since", "", "Only logs from this point: a duration (24h) or a date/datetime")
	fl.StringVar(&until, "until", "", "Only logs up to this date/datetime (a date means its end)")
	fl.IntVar(&perPage, "per-page", 0, "Rows per page (max 100)")
	fl.IntVar(&page, "page", 0, "Page number")
	return cmd
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
		Args: cobra.MaximumNArgs(1),
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

func newDeleteCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:     "delete [xid]",
		Aliases: []string{"rm", "del"},
		Short:   "Delete a webhook",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			xid, err := pickWebhook(cmd.Context(), f, cmdutil.FirstArg(args))
			if err != nil {
				return err
			}
			return runDelete(cmd.Context(), f, xid)
		},
	}, map[string]string{
		cmdutil.AnnScope:       "webhook:write",
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnConfirms:    "always",
	})
}

func runList(ctx context.Context, f *cmdutil.Factory, query url.Values) error {

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/webhooks", query)
	if err != nil {
		return err
	}
	hooks, page, err := api.ListInto[Webhook](env)
	if err != nil {
		return err
	}
	for i := range hooks {
		hooks[i] = hooks[i].redacted()
	}

	if err := f.RenderOutput(hooks, func() error {
		if len(hooks) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No webhooks found.")
			return nil
		}
		t := jsonfmt.NewTable("ID", "NAME", "URL", "EVENT", "AUTH", "TARGET", "ACTIVE").Truncatable("NAME", "URL", "TARGET")
		for _, w := range hooks {
			t.AddRow(w.ID, w.Name, w.urlLabel(), w.EventType, w.AuthenType, w.BankMode, f.YesNo(bool(w.Active)))
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	f.PrintPageInfo(page, len(hooks))
	return nil
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

func runLogs(ctx context.Context, f *cmdutil.Factory, query url.Values) error {

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/webhook-logs", query)
	if err != nil {
		return err
	}
	logs, page, err := api.ListInto[Log](env)
	if err != nil {
		return err
	}

	if err := f.RenderOutput(logs, func() error {
		if len(logs) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No webhook logs found.")
			return nil
		}
		t := jsonfmt.NewTable("DATE", "STATUS", "CODE", "REPLAY", "TRANSACTION", "WEBHOOK", "URL").Truncatable("URL")
		for _, l := range logs {
			t.AddRow(l.CreatedAt, logStatus(f, l.Status), intOrDash(l.ResponseStatusCode), f.YesNo(l.IsReplay),
				jsonfmt.OrDash(strOrEmpty(l.TransactionID)), jsonfmt.OrDash(strOrEmpty(l.WebhookXID)), l.urlLabel())
		}
		return f.RenderTable(t)
	}); err != nil {
		return err
	}
	f.PrintPageInfo(page, len(logs))
	return nil
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
		if res.Log == nil {
			f.ErrPrintf("Replayed (%s). Check the result with: sepay webhook logs --transaction %s --webhook %s\n",
				deliveredWord(res.Delivered), target.TransactionID, target.WebhookXID)
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
	return "not delivered: the handler did not answer 2xx"
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

func runDelete(ctx context.Context, f *cmdutil.Factory, xid string) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation(fmt.Sprintf("delete webhook %s", xid), true); err != nil {
		return err
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	if _, err := client.Delete(ctx, "/webhooks/"+xidSeg, api.WriteOptions{}); err != nil {
		return err
	}
	res := struct {
		ID      string `json:"id" yaml:"id"`
		Deleted bool   `json:"deleted" yaml:"deleted"`
	}{xid, true}
	return f.RenderOutput(res, func() error {
		f.ErrPrintf("%s Deleted webhook %s.\n", f.SuccessIcon(), xid)
		return nil
	})
}

func newCreateCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"new"},
		Short:   "Create a webhook",
		Example: `  # SePay POSTs every event to your URL
  sepay webhook create --url https://example.com/hook --bank-mode all

  # no URL: watch the events here instead (sandbox)
  sepay webhook create --listen-only --bank-mode all
  sepay webhook listen --forward localhost:3000

  # signed, so the handler can verify what it receives
  sepay webhook create --listen-only --bank-mode all --auth-type hmac --secret "$SECRET"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := buildWriteBody(cmd.Context(), f, cmd, writeOptions{forCreate: true})
			if err != nil {
				return err
			}
			return runCreate(cmd.Context(), f, body)
		},
	}
	addWriteFlags(cmd)
	// --url and --bank-account requirements depend on other flags, so only --bank-mode is marked.
	cmdutil.MarkRequired(cmd, "bank-mode")
	return cmdutil.Annotate(cmd, map[string]string{cmdutil.AnnScope: "webhook:write"})
}

func newUpdateCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "update [xid]",
		Aliases: []string{"edit"},
		Short:   "Update a webhook (only the flags you pass change)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			xid, err := pickWebhook(cmd.Context(), f, cmdutil.FirstArg(args))
			if err != nil {
				return err
			}
			opts := writeOptions{}
			if !anyWriteFlagChanged(cmd) {
				if opts, err = promptFieldsToEdit(cmd.Context(), f, cmd, xid); err != nil {
					return err
				}
			}
			body, err := buildWriteBody(cmd.Context(), f, cmd, opts)
			if err != nil {
				return err
			}
			if len(body) == 0 {
				return &cmdutil.FlagError{Err: fmt.Errorf("provide at least one field to update")}
			}
			return runUpdate(cmd.Context(), f, xid, body)
		},
	}
	addWriteFlags(cmd)
	return cmdutil.Annotate(cmd, map[string]string{cmdutil.AnnScope: "webhook:write"})
}

// editableField is one editor choice; the answer is written onto flag so it follows the flag-driven path.
// A field with no flag is a compound section prompted by buildWriteBody.
type editableField struct {
	label string
	flag  string
	// false where an empty answer is a slip (url)
	blankClears bool
	current     func(Webhook) string
	ask         func(f *cmdutil.Factory, label string) (string, error)
	section     func(*writeOptions)
}

func editableFields() []editableField {
	yesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	askBool := func(f *cmdutil.Factory, label string) (string, error) {
		return f.SelectChoiceOrFlag("", "", label, []cmdutil.PromptChoice{
			{Label: "yes", Value: "true"}, {Label: "no", Value: "false"},
		})
	}
	askEnum := func(options []string) func(*cmdutil.Factory, string) (string, error) {
		return func(f *cmdutil.Factory, label string) (string, error) {
			return f.SelectOrFlag("", "", label, options)
		}
	}
	askText := func(f *cmdutil.Factory, label string) (string, error) { return f.Input(label) }
	return []editableField{
		{label: "Name", flag: "name", blankClears: true,
			current: func(w Webhook) string { return w.Name },
			ask:     askText},
		{label: "Destination URL", flag: "url",
			current: func(w Webhook) string { return w.urlLabel() },
			ask:     askText},
		{label: "Events", flag: "event-type",
			current: func(w Webhook) string { return w.EventType },
			ask:     askEnum([]string{"all", "in", "out"})},
		{label: "Content type", flag: "content-type",
			current: func(w Webhook) string { return w.RequestContentType },
			ask:     askEnum([]string{"json", "form", "multipart"})},
		{label: "Only verified payments", flag: "verify-payment",
			current: func(w Webhook) string { return yesNo(w.IsVerifyPayment) },
			ask:     askBool},
		{label: "Skip events with no payment code", flag: "skip-if-no-code",
			current: func(w Webhook) string { return yesNo(w.SkipIfNoCode) },
			ask:     askBool},
		{label: "Payment-code prefixes", flag: "prefix-filter", blankClears: true,
			current: func(w Webhook) string { return strings.Join(w.PrefixFilters, ",") },
			ask:     askText},
		{label: "Authentication", current: func(w Webhook) string { return w.AuthenType },
			section: func(o *writeOptions) { o.auth = true }},
		{label: "Targeting", current: targetingSummary,
			section: func(o *writeOptions) { o.targeting = true }},
	}
}

func targetingSummary(w Webhook) string {
	switch w.BankMode {
	case "single":
		if w.BankAccountXID != nil {
			return "single · " + *w.BankAccountXID
		}
		return "single"
	case "multi":
		return fmt.Sprintf("multi · %d accounts", len(w.BankAccountXIDs))
	}
	return w.BankMode
}

// anyWriteFlagChanged reports whether any local field flag was passed.
// LocalFlags is a rebuilt set whose NFlag is always 0, so Changed is checked per flag.
func anyWriteFlagChanged(cmd *cobra.Command) bool {
	changed := false
	cmd.LocalFlags().VisitAll(func(fl *pflag.Flag) {
		if fl.Changed && fl.Name != "help" {
			changed = true
		}
	})
	return changed
}

// promptFieldsToEdit runs the interactive editor, writing each answer back onto its flag.
func promptFieldsToEdit(ctx context.Context, f *cmdutil.Factory, cmd *cobra.Command, xid string) (writeOptions, error) {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return writeOptions{}, err
	}
	var opts writeOptions
	if !f.CanPrompt() {
		return opts, &cmdutil.FlagError{Err: fmt.Errorf("provide at least one field to update")}
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return opts, err
	}
	env, err := client.Get(ctx, "/webhooks/"+xidSeg, nil)
	if err != nil {
		return opts, err
	}
	current, err := api.DataInto[Webhook](env)
	if err != nil {
		return opts, err
	}

	fields := editableFields()
	choices := make([]cmdutil.PromptChoice, len(fields))
	for i, fd := range fields {
		label := fd.label
		if cur := fd.current(current); cur != "" {
			label += " (" + cur + ")"
		}
		choices[i] = cmdutil.PromptChoice{Label: label, Value: strconv.Itoa(i)}
	}
	picked, err := f.MultiSelectChoicesOrFlag(nil, "", "What do you want to edit?", choices)
	if err != nil {
		return opts, err
	}

	// Update never sets --listen-only; the stored transport decides the offerable auth schemes.
	opts.listenOnly = current.listenOnly()

	for _, p := range picked {
		fd := fields[mustAtoi(p)]
		if fd.section != nil {
			fd.section(&opts)
			continue
		}
		v, err := fd.ask(f, fd.promptLabel())
		if err != nil {
			return opts, err
		}
		if v == "" && !fd.blankClears {
			continue
		}
		if fd.flag == "prefix-filter" {
			if v, err = normalizePrefixes(v); err != nil {
				return opts, err
			}
		}
		// Set marks the flag Changed even when empty, which is how a blankClears field is emptied.
		if err := cmd.Flags().Set(fd.flag, v); err != nil {
			return opts, err
		}
	}
	return opts, nil
}

func (fd editableField) promptLabel() string {
	if fd.blankClears {
		return fd.label + " (blank to clear)"
	}
	return fd.label
}

// normalizePrefixes turns "A, B" into a CSV pflag parses cleanly; quotes are refused because
// pflag would report them against a flag the user never typed.
func normalizePrefixes(answer string) (string, error) {
	var out []string
	for _, p := range strings.Split(answer, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.ContainsAny(p, `",`) {
			return "", &cmdutil.FlagError{Err: fmt.Errorf("payment-code prefix %q cannot contain a quote or comma", p)}
		}
		out = append(out, p)
	}
	return strings.Join(out, ","), nil
}

func mustAtoi(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}

func addWriteFlags(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.String("url", "", "Destination URL (required unless --listen-only)")
	fl.Bool("listen-only", false, "No destination URL; watch events with \"sepay webhook listen\" (sandbox only)")
	fl.String("name", "", "Webhook name")
	fl.String("event-type", "all", "Which events: all|in|out")
	fl.String("auth-type", "", "Auth scheme: none|apikey|hmac|oauth2")
	fl.String("secret", "", "Shared secret for apikey/hmac auth, or - to read it from stdin")
	fl.String("content-type", "", "Request content type: json|form|multipart")
	fl.Bool("verify-payment", false, "Only forward verified payments")
	fl.Bool("skip-if-no-code", false, "Skip events without a payment code")
	fl.StringSlice("prefix-filter", nil, "Only forward these payment-code prefixes")
	fl.String("oauth2-client-id", "", "OAuth2 client id (auth-type oauth2)")
	fl.String("oauth2-client-secret", "", "OAuth2 client secret (auth-type oauth2), or - to read it from stdin")
	fl.String("oauth2-token-url", "", "OAuth2 access-token URL (auth-type oauth2)")
	fl.String("bank-mode", "", "Targeting: single|multi|all (required on create when not prompting)")
	fl.StringSlice("bank-account", nil, "Bank account xid, repeat for multi (required for --bank-mode single|multi)")
	fl.StringSlice("sub-account", nil, "Virtual-account xid to target (repeatable)")
	fl.Bool("only-va", false, "Only virtual-account transactions")
	fl.StringSlice("bank-config", nil, "Per-account config xid=on|off:all|list|none (repeatable)")
}

func parseBankConfig(pairs []string) (map[string]any, error) {
	out := make(map[string]any, len(pairs))
	for _, p := range pairs {
		xid, spec, ok := strings.Cut(p, "=")
		if !ok || xid == "" {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("--bank-config must be xid=primary:va_mode, got %q", p)}
		}
		primary, vaMode, ok := strings.Cut(spec, ":")
		if !ok {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("--bank-config %q: expected primary:va_mode", p)}
		}
		var receivePrimary bool
		switch primary {
		case "on", "true", "1":
			receivePrimary = true
		case "off", "false", "0":
			receivePrimary = false
		default:
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("--bank-config %q: primary must be on|off", p)}
		}
		switch vaMode {
		case "all", "list", "none":
		default:
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("--bank-config %q: va_mode must be all|list|none", p)}
		}
		if _, dup := out[xid]; dup {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("--bank-config lists %s twice; give each bank account one config", xid)}
		}
		out[xid] = map[string]any{"receive_primary": receivePrimary, "va_mode": vaMode}
	}
	return out, nil
}

// writeOptions says which prompts buildWriteBody may run; a flag-driven update stays silent.
type writeOptions struct {
	forCreate  bool
	auth       bool // prompt for the auth scheme and its secrets
	targeting  bool // prompt for bank_mode and its accounts
	listenOnly bool // stored transport; narrows auth schemes
}

// buildWriteBody assembles the create/update request; update sends only changed fields.
func buildWriteBody(ctx context.Context, f *cmdutil.Factory, cmd *cobra.Command, opts writeOptions) (map[string]any, error) {
	forCreate := opts.forCreate
	fl := cmd.Flags()
	body := map[string]any{}

	enums := map[string][]string{
		"event-type":   {"all", "in", "out"},
		"auth-type":    {"none", "apikey", "hmac", "oauth2"},
		"content-type": {"json", "form", "multipart"},
		"bank-mode":    {"single", "multi", "all"},
	}
	promptsForAuth := (forCreate || opts.auth) && f.CanPrompt()

	// The API reads a secret only inside its auth_type branch and otherwise drops it silently.
	// A prompted scheme supplies auth_type, so it is exempt.
	if !fl.Changed("auth-type") && !promptsForAuth {
		for _, flag := range []string{"secret", "oauth2-client-secret"} {
			if fl.Changed(flag) {
				return nil, &cmdutil.FlagError{Err: fmt.Errorf("--%s needs --auth-type; a secret alone is ignored", flag)}
			}
		}
	}

	if err := cmdutil.ValidateChoices(cmd, enums); err != nil {
		return nil, err
	}

	addStr := func(flag, key string) {
		v, _ := fl.GetString(flag)
		if fl.Changed(flag) || (forCreate && v != "") {
			body[key] = v
		}
	}
	addBool := func(flag, key string) {
		if fl.Changed(flag) {
			v, _ := fl.GetBool(flag)
			body[key] = v
		}
	}

	listenOnly, _ := fl.GetBool("listen-only")
	if !listenOnly && fl.Changed("listen-only") {
		return nil, &cmdutil.FlagError{Err: fmt.Errorf(
			"--listen-only=false does not set a destination; pass --url to move the webhook back to HTTP")}
	}
	url, _ := fl.GetString("url")

	// Ask for the transport first, or listen-only would only be reachable through the flag.
	if forCreate && !listenOnly && url == "" && f.CanPrompt() && f.ActiveEnv() != config.EnvProduction {
		mode, err := f.SelectChoiceOrFlag("", "url", "Delivery", []cmdutil.PromptChoice{
			{Label: "Send events to my URL", Value: deliveryModeHTTP},
			{Label: `Watch events locally with "sepay webhook listen" (no URL)`, Value: deliveryModeListen},
		})
		if err != nil {
			return nil, err
		}
		listenOnly = mode == deliveryModeListen
	}

	// Below the picker, which is the second place that decides the transport.
	if listenOnly {
		if at, _ := fl.GetString("auth-type"); at == "oauth2" {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf(
				"--auth-type oauth2 needs a destination to authenticate against, so it cannot be combined with --listen-only")}
		}
		if fl.Changed("url") {
			// Not MarkFlagsMutuallyExclusive, so the exit code is the usage one.
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("--url and --listen-only are mutually exclusive")}
		}
		if err := f.AssertSandboxOnly("--listen-only"); err != nil {
			return nil, err
		}
	}

	switch {
	case listenOnly:
		body["delivery_mode"] = deliveryModeListen
	case forCreate:
		var err error
		if url, err = f.InputOrFlag(url, "url", "Webhook URL"); err != nil {
			return nil, err
		}
		body["url"] = url
	case fl.Changed("url"):
		body["url"] = url
		// A URL moves a listen-only webhook back to HTTP; production has no mode column (422).
		if f.ActiveEnv() != config.EnvProduction {
			body["delivery_mode"] = deliveryModeHTTP
		}
	}

	// Fail before the remaining prompts and the targeting API calls.
	if err := validateBodyURL(body, "url", "--url"); err != nil {
		return nil, err
	}

	name, _ := fl.GetString("name")
	if forCreate && name == "" && f.CanPrompt() {
		v, err := f.Input("Webhook name (optional)")
		if err != nil {
			return nil, err
		}
		name = v
	}
	if fl.Changed("name") || (forCreate && name != "") {
		body["name"] = name
	}

	addStr("event-type", "event_type")
	addStr("auth-type", "auth_type")
	addStr("content-type", "content_type")
	addStr("oauth2-client-id", "oauth2_client_id")
	addStr("oauth2-token-url", "oauth2_access_token_url")

	// "-" reads a secret from stdin, keeping it out of shell history and the process list.
	for _, sec := range []struct{ flag, key string }{
		{"secret", "secret"},
		{"oauth2-client-secret", "oauth2_client_secret"},
	} {
		v, _ := fl.GetString(sec.flag)
		if !fl.Changed(sec.flag) && (!forCreate || v == "") {
			continue
		}
		resolved, err := f.SecretFromFlag(v, sec.flag)
		if err != nil {
			return nil, err
		}
		body[sec.key] = resolved
	}
	if promptsForAuth && !fl.Changed("auth-type") {
		// Narrow schemes by the stored transport without re-sending delivery_mode.
		if err := promptAuth(f, body, listenOnly || opts.listenOnly); err != nil {
			return nil, err
		}
	}

	// Below promptAuth, which also writes this key.
	if err := validateBodyURL(body, "oauth2_access_token_url", "--oauth2-token-url"); err != nil {
		return nil, err
	}

	addBool("verify-payment", "is_verify_payment")
	addBool("skip-if-no-code", "skip_if_no_code")
	if prefixes, _ := fl.GetStringSlice("prefix-filter"); fl.Changed("prefix-filter") || (forCreate && len(prefixes) > 0) {
		body["prefix_filters"] = prefixes
	}

	if err := resolveTargeting(ctx, f, cmd, body, opts); err != nil {
		return nil, err
	}
	return body, nil
}

// validateBodyURL skips a key the write does not send.
func validateBodyURL(body map[string]any, key, flag string) error {
	dest, ok := body[key].(string)
	if !ok || dest == "" {
		return nil
	}
	return cmdutil.ValidateDestinationURL(dest, flag)
}

// authChoices withholds OAuth2 from listen-only webhooks: there is no destination to fetch a token from.
func authChoices(listenOnly bool) []cmdutil.PromptChoice {
	choices := []cmdutil.PromptChoice{
		{Label: "None", Value: "none"},
		{Label: "API key", Value: "apikey"},
		{Label: "HMAC signature", Value: "hmac"},
	}
	if !listenOnly {
		choices = append(choices, cmdutil.PromptChoice{Label: "OAuth2", Value: "oauth2"})
	}
	return choices
}

// authFieldsUnused lists the body keys each scheme ignores; the API would drop them silently.
var authFieldsUnused = map[string][]string{
	"none":   {"secret", "oauth2_client_id", "oauth2_client_secret", "oauth2_access_token_url"},
	"apikey": {"oauth2_client_id", "oauth2_client_secret", "oauth2_access_token_url"},
	"hmac":   {"oauth2_client_id", "oauth2_client_secret", "oauth2_access_token_url"},
	"oauth2": {"secret"},
}

// clearUnusedAuthFields drops keys the chosen scheme cannot use; keys it uses are kept.
func clearUnusedAuthFields(body map[string]any, authType string) {
	for _, k := range authFieldsUnused[authType] {
		delete(body, k)
	}
}

// promptAuth asks for the auth scheme and reads its secrets without echo.
func promptAuth(f *cmdutil.Factory, body map[string]any, listenOnly bool) error {
	authType, err := f.SelectChoiceOrFlag("", "auth-type", "Authentication", authChoices(listenOnly))
	if err != nil {
		return err
	}
	body["auth_type"] = authType
	clearUnusedAuthFields(body, authType)

	switch authType {
	case "apikey", "hmac":
		secret, err := f.Password("Shared secret")
		if err != nil {
			return err
		}
		if secret != "" {
			body["secret"] = secret
		}
	case "oauth2":
		clientID, err := f.InputOrFlag("", "oauth2-client-id", "OAuth2 client id")
		if err != nil {
			return err
		}
		body["oauth2_client_id"] = clientID
		secret, err := f.Password("OAuth2 client secret")
		if err != nil {
			return err
		}
		if secret != "" {
			body["oauth2_client_secret"] = secret
		}
		tokenURL, err := f.InputOrFlag("", "oauth2-token-url", "OAuth2 access-token URL")
		if err != nil {
			return err
		}
		body["oauth2_access_token_url"] = tokenURL
	}
	return nil
}

// resolveTargeting fills bank_mode and its accounts; update touches them only when a targeting flag is set.
func resolveTargeting(ctx context.Context, f *cmdutil.Factory, cmd *cobra.Command, body map[string]any, opts writeOptions) error {
	fl := cmd.Flags()
	// Without prompting, a partial flag set must stay a usage error.
	prompting := opts.forCreate || opts.targeting
	touched := fl.Changed("bank-mode") || fl.Changed("bank-account") || fl.Changed("sub-account") || fl.Changed("only-va") || fl.Changed("bank-config")
	if !prompting && !touched {
		return nil
	}

	banks, _ := fl.GetStringSlice("bank-account")
	subs, _ := fl.GetStringSlice("sub-account")
	mode, _ := fl.GetString("bank-mode")
	if mode == "" {
		switch {
		case len(banks) > 1:
			mode = "multi"
		case len(banks) == 1:
			mode = "single"
		}
	}
	if prompting && mode == "" {
		var err error
		if mode, err = f.SelectOrFlag("", "bank-mode", "Targeting mode", []string{"single", "multi", "all"}); err != nil {
			return err
		}
	}
	if mode != "" {
		body["bank_mode"] = mode
	}

	// Reject extras for the mode rather than drop them and widen the webhook's scope.
	switch mode {
	case "single":
		if len(banks) > 1 {
			return &cmdutil.FlagError{Err: fmt.Errorf("--bank-mode single takes a single --bank-account; use --bank-mode multi for several")}
		}
		xid := ""
		if len(banks) > 0 {
			xid = banks[0]
		}
		if prompting && xid == "" {
			var err error
			if xid, err = bankaccount.PickOne(ctx, f, "", "bank-account", "Bank account"); err != nil {
				return err
			}
		}
		if xid != "" {
			body["bank_account_xid"] = xid
		}
	case "multi":
		if prompting && len(banks) == 0 && f.CanPrompt() {
			picked, err := bankaccount.PickMany(ctx, f, "bank-account", "Bank accounts to target")
			if err != nil {
				return err
			}
			banks = picked
		}
		if len(banks) == 0 {
			return &cmdutil.FlagError{Err: fmt.Errorf("--bank-mode multi needs at least one --bank-account")}
		}
		body["bank_account_xids"] = banks
	case "all":
		// The server discards per-account narrowing in all mode, only_va included.
		if len(banks) > 0 || len(subs) > 0 || fl.Changed("bank-config") || fl.Changed("only-va") {
			return &cmdutil.FlagError{Err: fmt.Errorf("--bank-mode all targets every account; remove --bank-account/--sub-account/--bank-config/--only-va")}
		}
	}

	if len(subs) > 0 {
		body["bank_sub_account_xids"] = subs
	}
	if fl.Changed("only-va") {
		v, _ := fl.GetBool("only-va")
		// In multi mode the server rewrites only_va to 0 when the VA list is empty, so the flag alone is a no-op.
		// Single mode reads it as receive_primary=0 + va_mode=all.
		if v && len(subs) == 0 && mode == "multi" {
			return &cmdutil.FlagError{Err: fmt.Errorf(
				"--bank-mode multi ignores --only-va without --sub-account; use --bank-config <xid>=off:all to skip the account number itself")}
		}
		body["only_va"] = v
	}
	if fl.Changed("bank-config") {
		raw, _ := fl.GetStringSlice("bank-config")
		cfg, err := parseBankConfig(raw)
		if err != nil {
			return err
		}
		body["bank_config"] = cfg
	}
	return nil
}

func runCreate(ctx context.Context, f *cmdutil.Factory, body map[string]any) error {

	if err := f.ConfirmMutation("create webhook", false); err != nil {
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
	env, err := client.Post(ctx, "/webhooks", api.WriteOptions{Body: body, IdempotencyKey: key})
	if err != nil {
		return err
	}
	w, err := api.DataInto[Webhook](env)
	if err != nil {
		return err
	}
	return f.RenderOutput(w, func() error {
		f.ErrPrintf("%s Created webhook %s.\n", f.SuccessIcon(), w.ID)
		return renderWebhookDetail(ctx, f, w)
	})
}

func runUpdate(ctx context.Context, f *cmdutil.Factory, xid string, body map[string]any) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}

	if err := f.ConfirmMutation("update webhook", false); err != nil {
		return err
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Put(ctx, "/webhooks/"+xidSeg, api.WriteOptions{Body: body})
	if err != nil {
		return err
	}
	w, err := api.DataInto[Webhook](env)
	if err != nil {
		return err
	}
	return f.RenderOutput(w, func() error {
		f.ErrPrintf("%s Updated webhook %s.\n", f.SuccessIcon(), w.ID)
		return renderWebhookDetail(ctx, f, w)
	})
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

func logStatus(f *cmdutil.Factory, status string) string {
	switch status {
	case "success", logStatusListen:
		return f.IOStreams.Green(status)
	case "failed":
		return f.IOStreams.Red(status)
	case "skipped":
		// The webhook's own filters dropped the event before any request.
		return f.IOStreams.Dim(status)
	}
	return status
}

func intOrDash(n *int) string {
	if n == nil {
		return "-"
	}
	return strconv.Itoa(*n)
}

func sortedKeys(m map[string]BankConfigEntry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
