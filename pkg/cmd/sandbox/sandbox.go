// Package sandbox implements "sepay sandbox", the sandbox-only utilities.
package sandbox

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmd/bankaccount"
	vapkg "github.com/sepayvn/sepay-cli/pkg/cmd/va"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

var eventTypes = []string{"payment_in", "payment_out", "va_credit"}

const maxCodeLen = 10

// EventTypes returns the events sandbox simulate can fire.
func EventTypes() []string { return eventTypes }

// EnsureSandboxEnv forces the sandbox environment, refusing an explicit --env production, and validates
// the resolved host so no override can aim a sandbox command at production.
func EnsureSandboxEnv(f *cmdutil.Factory) error {
	if f.Global.Env == config.EnvProduction {
		return &cmdutil.FlagError{Err: fmt.Errorf("sandbox commands only run against the sandbox environment; drop --env production")}
	}
	f.Global.Env = config.EnvSandbox
	return f.AssertSandboxTarget()
}

// simulateOnce uses a fresh idempotency key per call, never --idempotency-key: a shared key would collapse
// repeated events into a replay of the first.
func simulateOnce(ctx context.Context, client *api.Client, body SimulateBody) (*api.Envelope, error) {
	key, err := api.NewIdempotencyKey()
	if err != nil {
		return nil, err
	}
	return client.Post(ctx, "/transactions/simulate", api.WriteOptions{Body: body, IdempotencyKey: key})
}

// Simulate fires one sandbox transaction and returns its result.
func Simulate(ctx context.Context, client *api.Client, body SimulateBody) (SimulateResult, error) {
	env, err := simulateOnce(ctx, client, body)
	if err != nil {
		return SimulateResult{}, err
	}
	return api.DataInto[SimulateResult](env)
}

// NewCmdSandbox builds the "sepay sandbox" command group.
func NewCmdSandbox(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sandbox <command>",
		Aliases: []string{"sbx"},
		Short:   "Sandbox-only utilities (simulate, init, seed, reset)",
		Long: `The sandbox is a separate test environment with its own data and no real money;
it is the default --env. These commands refuse every other environment.

"sandbox init" provisions a bank account, an optional VA and webhook, and sample
transactions in one go. "sandbox simulate" fires a canned transaction at your
webhooks, "sandbox seed" adds sample transactions and "sandbox reset" clears
webhooks and bank accounts.`,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return EnsureSandboxEnv(f)
		},
	}
	cmd.AddCommand(
		newSimulateCmd(f),
		newInitCmd(f),
		newSeedCmd(f),
		newResetCmd(f),
	)
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnEnvs: "sandbox",
	})
}

func newSimulateCmd(f *cmdutil.Factory) *cobra.Command {
	var (
		eventFlag     string
		amount        int64
		code          string
		bankAccount   string
		vaNumber      string
		overridePairs []string
	)
	cmd := &cobra.Command{
		Use:   "simulate [event]",
		Short: "Fire a canned sandbox transaction",
		Long: `Fire a canned sandbox transaction on a bank account, as if the bank had reported
it, so your webhooks and "sepay webhook listen" receive it.

The event is payment_in, payment_out or va_credit, given as [event] or --event;
va_credit also needs --va, the VA number. Event, bank account and amount are
prompted for on a terminal; required otherwise.`,
		Example: `  sepay sandbox simulate payment_in --bank-account <bank-account-xid> --amount 50000
  sepay sandbox simulate va_credit --bank-account <bank-account-xid> --va <va-number> --amount 50000`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(code) > maxCodeLen {
				return &cmdutil.FlagError{Err: fmt.Errorf("--code must be at most %d characters", maxCodeLen)}
			}
			event := eventFlag
			if len(args) > 0 {
				event = args[0]
			}
			// The event may also come positionally, so check the resolved value.
			event, err := f.SelectOrFlag(event, "event", "Event type", eventTypes)
			if err != nil {
				return err
			}
			if !slices.Contains(eventTypes, event) {
				return &cmdutil.FlagError{Err: fmt.Errorf("event must be one of: %s", strings.Join(eventTypes, ", "))}
			}
			if vaNumber != "" && event != "va_credit" {
				return &cmdutil.FlagError{Err: fmt.Errorf("--va applies to va_credit only, not %s", event)}
			}
			if cmd.Flags().Changed("amount") {
				if err := CheckAmount(amount); err != nil {
					return err
				}
			}
			baXID, err := bankaccount.PickOne(cmd.Context(), f, bankAccount, "bank-account", "Bank account")
			if err != nil {
				return err
			}
			amt, err := ResolveAmount(f, amount, cmd.Flags().Changed("amount"))
			if err != nil {
				return err
			}
			va := vaNumber
			if event == "va_credit" {
				if va, err = vapkg.PickNumber(cmd.Context(), f, baXID, vaNumber, "va", "Virtual account"); err != nil {
					return err
				}
			}
			overrides, err := parseOverrides(overridePairs)
			if err != nil {
				return err
			}
			return runSimulate(cmd.Context(), f, SimulateBody{
				Type:           event,
				Amount:         amt,
				BankAccountXID: baXID,
				Code:           code,
				VANumber:       va,
				Overrides:      overrides,
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&eventFlag, "event", "", "Event type (or pass as the positional arg): "+strings.Join(eventTypes, ", "))
	fl.Int64Var(&amount, "amount", 0, "Amount in VND (required)")
	fl.StringVar(&code, "code", "", fmt.Sprintf("Pay code (max %d chars)", maxCodeLen))
	fl.StringVar(&bankAccount, "bank-account", "", "Bank account xid (required)")
	fl.StringVar(&vaNumber, "va", "", "Virtual account number (required for va_credit)")
	fl.StringArrayVar(&overridePairs, "override", nil, "Override a payload field, key=val (repeatable; only 'content')")
	// --event can be positional; --va applies to va_credit only.
	return cmdutil.MarkRequired(cmd, "bank-account", "amount")
}

// SimulateBody is the POST /v2/transactions/simulate request.
type SimulateBody struct {
	Type           string            `json:"type"`
	Amount         int64             `json:"amount"`
	BankAccountXID string            `json:"bank_account_xid"`
	Code           string            `json:"code,omitempty"`
	VANumber       string            `json:"va_number,omitempty"`
	Overrides      map[string]string `json:"overrides,omitempty"`
}

// SimulateResult is the simulate response: the new xid plus warnings about degraded side effects.
type SimulateResult struct {
	XID      string   `json:"xid" yaml:"xid"`
	Warnings []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

func runSimulate(ctx context.Context, f *cmdutil.Factory, body SimulateBody) error {
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	res, err := Simulate(ctx, client, body)
	if err != nil {
		return err
	}

	return f.RenderOutput(res, func() error {
		f.ErrPrintf("Simulated %s event.\n", body.Type)
		for _, w := range res.Warnings {
			if w == "accumulated_not_updated" {
				w = "accumulated balance was not updated for this transaction"
			}
			f.ErrPrintf("! %s\n", w)
		}
		fmt.Fprintln(f.IOStreams.Out, res.XID)
		return nil
	})
}

// ResolveAmount reads --amount or prompts for it. given tells an absent flag from an explicit 0.
func ResolveAmount(f *cmdutil.Factory, amount int64, given bool) (int64, error) {
	n := amount
	if !given {
		val, err := f.InputOrFlag("", "amount", "Amount (VND)")
		if err != nil {
			return 0, err
		}
		if n, err = strconv.ParseInt(strings.TrimSpace(val), 10, 64); err != nil {
			return 0, &cmdutil.FlagError{Err: fmt.Errorf("--amount must be an integer")}
		}
	}
	return n, CheckAmount(n)
}

// Simulated amounts the server accepts, in VND.
const (
	simulateAmountMin = 1000
	simulateAmountMax = 499_000_000
)

// CheckAmount refuses an amount the simulate endpoint rejects.
func CheckAmount(n int64) error {
	if n < simulateAmountMin || n > simulateAmountMax {
		return &cmdutil.FlagError{Err: fmt.Errorf("--amount must be between %d and %d", simulateAmountMin, simulateAmountMax)}
	}
	return nil
}

func parseOverrides(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("--override must be key=val, got %q", p)}
		}
		if k != "content" {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("--override only supports 'content', got %q", k)}
		}
		m[k] = v
	}
	return m, nil
}

func newInitCmd(f *cmdutil.Factory) *cobra.Command {
	var opts initOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Provision a starter sandbox (account, VA, webhook, seed data)",
		Long: `Provision a starter sandbox: a bank account, optionally a virtual account and a
webhook, and optionally a few sample transactions.

A webhook is created either with an HTTP destination (--with-webhook) or with no
destination at all (--with-webhook-listen), which only "sepay webhook listen"
consumes.

On a terminal, whatever you leave off the command line is asked for. Pass every
flag, or --yes, to take the defaults and run without questions.`,
		Example: `  # bank account, VA, a webhook for "webhook listen" and 10 sample transactions
  sepay sandbox init --with-va --with-webhook-listen --seed 10 --yes

  # a starter account whose webhook posts to your server
  sepay sandbox init --with-webhook https://example.com/hook --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Validate before the first write, or a late rejection leaves a half-provisioned sandbox.
			if err := validateSeedCount(opts.seed, "--seed"); err != nil {
				return err
			}
			if opts.webhook != "" && opts.webhookListen {
				return &cmdutil.FlagError{Err: fmt.Errorf("--with-webhook and --with-webhook-listen are mutually exclusive")}
			}
			if opts.webhook != "" {
				if err := cmdutil.ValidateDestinationURL(opts.webhook, "--with-webhook"); err != nil {
					return err
				}
			}
			if cmd.Flags().Changed("bank") {
				var err error
				if opts.bank, err = bankaccount.CheckBankCode(cmd.Context(), f, opts.bank); err != nil {
					return err
				}
			}
			if err := promptInit(cmd.Context(), f, cmd, &opts); err != nil {
				return err
			}
			// The supported codes differ by deployment, so the default comes from the server's list.
			if opts.bank == "" {
				var err error
				if opts.bank, err = bankaccount.DefaultBankCode(cmd.Context(), f); err != nil {
					return err
				}
			}
			return runInit(cmd.Context(), f, opts)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&opts.bank, "bank", "", "Bank code for the starter account (default ACB when the server offers it, else its first bank)")
	fl.BoolVar(&opts.withVA, "with-va", false, "Also issue a virtual account")
	fl.StringVar(&opts.webhook, "with-webhook", "", "Also create a webhook to this URL")
	fl.BoolVar(&opts.webhookListen, "with-webhook-listen", false, `Also create a webhook with no URL, for "sepay webhook listen"`)
	fl.IntVar(&opts.seed, "seed", 0, fmt.Sprintf("Seed this many sample transactions (max %d)", maxSeedCount))
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnGuidance: "Provisions several resources in sequence. Arguments are validated before the first " +
			"write, but a step the API refuses leaves the earlier ones standing: the result printed then carries " +
			"partial:true with the xids that do exist, and `sepay sandbox reset` clears the whole sandbox.",
	})
}

type initOptions struct {
	bank          string
	withVA        bool
	webhook       string
	webhookListen bool
	seed          int
}

// promptInit asks for each flag not given; --yes takes the defaults.
func promptInit(ctx context.Context, f *cmdutil.Factory, cmd *cobra.Command, opts *initOptions) error {
	if !f.CanPrompt() || f.Global.Yes {
		return nil
	}
	fl := cmd.Flags()
	var err error

	if opts.bank == "" {
		if opts.bank, err = bankaccount.PickBankCode(ctx, f, ""); err != nil {
			return err
		}
	}
	if !fl.Changed("with-va") {
		if opts.withVA, err = f.AskYesNo("Issue a virtual account?", opts.withVA); err != nil {
			return err
		}
	}
	if !fl.Changed("with-webhook") && !fl.Changed("with-webhook-listen") {
		if err := promptWebhook(f, opts); err != nil {
			return err
		}
	}
	if !fl.Changed("seed") {
		answer, err := f.Input(fmt.Sprintf("Sample transactions to seed (blank for none, max %d)", maxSeedCount))
		if err != nil {
			return err
		}
		if opts.seed, err = parseSeedCount(answer); err != nil {
			return err
		}
	}
	return nil
}

func parseSeedCount(answer string) (int, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(answer)
	if err != nil {
		return 0, &cmdutil.FlagError{Err: fmt.Errorf("seed count must be an integer, got %q", answer)}
	}
	if err := validateSeedCount(n, "seed count"); err != nil {
		return 0, err
	}
	return n, nil
}

// deliveryModeListen duplicates the webhook package's constant, which imports this package.
const deliveryModeListen = "listen"

// promptWebhook asks for the webhook kind before the URL, so listen-only is discoverable.
func promptWebhook(f *cmdutil.Factory, opts *initOptions) error {
	kind, err := f.SelectChoiceOrFlag("", "with-webhook", "Webhook", []cmdutil.PromptChoice{
		{Label: "None", Value: "none"},
		{Label: "Send events to my URL", Value: "http"},
		{Label: `Watch events locally with "sepay webhook listen" (no URL)`, Value: deliveryModeListen},
	})
	if err != nil {
		return err
	}
	switch kind {
	case "http":
		answer, err := f.Input("Webhook URL")
		if err != nil {
			return err
		}
		opts.webhook = strings.TrimSpace(answer)
		if opts.webhook == "" {
			return &cmdutil.FlagError{Err: fmt.Errorf("webhook URL cannot be empty; pick the listen-only option for a webhook with no destination")}
		}
		return cmdutil.ValidateDestinationURL(opts.webhook, "webhook URL")
	case deliveryModeListen:
		opts.webhookListen = true
	}
	return nil
}

func validateSeedCount(n int, name string) error {
	switch {
	case n < 0:
		return &cmdutil.FlagError{Err: fmt.Errorf("%s must be 0 or more", name)}
	case n > maxSeedCount:
		return &cmdutil.FlagError{Err: fmt.Errorf("%s must be %d or less", name, maxSeedCount)}
	}
	return nil
}

// initResult is what "sandbox init" provisioned; Partial is set only when a run stopped part-way.
type initResult struct {
	BankAccount string `json:"bank_account" yaml:"bank_account"`
	VA          string `json:"va,omitempty" yaml:"va,omitempty"`
	Webhook     string `json:"webhook,omitempty" yaml:"webhook,omitempty"`
	Seeded      int    `json:"seeded" yaml:"seeded"`
	Partial     bool   `json:"partial,omitempty" yaml:"partial,omitempty"`
}

type idItem struct {
	ID string `json:"id"`
}

// runInit reports what exists afterwards even on failure: nothing rolls the writes back,
// so a --json caller needs the xids a failed run left behind.
func runInit(ctx context.Context, f *cmdutil.Factory, opts initOptions) error {
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}

	var res initResult
	provisionErr := provisionInit(ctx, f, client, opts, &res)
	if provisionErr != nil {
		if res.BankAccount == "" {
			return provisionErr // nothing was created
		}
		res.Partial = true
	}

	renderErr := f.RenderOutput(res, func() error {
		fmt.Fprintln(f.IOStreams.Out, res.BankAccount)
		return nil
	})
	if provisionErr != nil {
		fmt.Fprintln(f.IOStreams.ErrOut, "! stopped part-way; what it created above already exists")
		return provisionErr
	}
	return renderErr
}

func provisionInit(ctx context.Context, f *cmdutil.Factory, client *api.Client, opts initOptions, res *initResult) error {
	errOut := f.IOStreams.ErrOut

	baKey, err := f.IdempotencyKey()
	if err != nil {
		return err
	}
	env, err := client.Post(ctx, "/bank-accounts", api.WriteOptions{
		Body:           map[string]any{"bank": opts.bank, "account_type": "individual", "connection_type": "api", "label": "sandbox"},
		IdempotencyKey: baKey,
	})
	if err != nil {
		return err
	}
	ba, err := api.DataInto[idItem](env)
	if err != nil {
		return err
	}
	cmdutil.Fprintf(errOut, "Created bank account %s (%s).\n", ba.ID, opts.bank)
	res.BankAccount = ba.ID

	if opts.withVA {
		key, err := f.IdempotencyKey()
		if err != nil {
			return err
		}
		baSeg, err := cmdutil.PathSegment("bank account xid", ba.ID)
		if err != nil {
			return fmt.Errorf("unusable bank account id in the API response")
		}
		env, err := client.Post(ctx, "/bank-accounts/"+baSeg+"/va", api.WriteOptions{
			Body: map[string]any{"label": "sandbox va"}, IdempotencyKey: key,
		})
		if err != nil {
			return err
		}
		va, err := api.DataInto[struct {
			VA string `json:"va"`
		}](env)
		if err != nil {
			return err
		}
		res.VA = va.VA
		cmdutil.Fprintf(errOut, "Issued virtual account %s.\n", va.VA)
	}

	if opts.webhook != "" || opts.webhookListen {
		key, err := f.IdempotencyKey()
		if err != nil {
			return err
		}
		body := map[string]any{"bank_mode": "single", "bank_account_xid": ba.ID}
		kind := "webhook"
		if opts.webhookListen {
			// url must be omitted, not empty, in listen mode.
			body["delivery_mode"] = deliveryModeListen
			kind = "listen-only webhook"
		} else {
			body["url"] = opts.webhook
		}
		env, err := client.Post(ctx, "/webhooks", api.WriteOptions{Body: body, IdempotencyKey: key})
		if err != nil {
			return err
		}
		wh, err := api.DataInto[idItem](env)
		if err != nil {
			return err
		}
		res.Webhook = wh.ID
		cmdutil.Fprintf(errOut, "Created %s %s.\n", kind, wh.ID)
	}

	for i := 0; i < opts.seed; i++ {
		body := SimulateBody{
			Type:           "payment_in",
			Amount:         seedAmounts[i%len(seedAmounts)],
			BankAccountXID: ba.ID,
			Overrides:      map[string]string{"content": fmt.Sprintf("Sample transaction %d", i+1)},
		}
		if _, err := simulateOnce(ctx, client, body); err != nil {
			return fmt.Errorf("seeded %d of %d, then failed: %w", res.Seeded, opts.seed, err)
		}
		res.Seeded++
	}
	if opts.seed > 0 {
		fmt.Fprintf(errOut, "Seeded %d transactions.\n", res.Seeded)
	}
	return nil
}

const defaultSeedCount = 5

// maxSeedCount keeps a seed run out of the rate limiter (one API call per transaction).
const maxSeedCount = 100

// seedAmounts cycle deterministically so a seed run is reproducible.
var seedAmounts = []int64{20000, 50000, 100000, 200000, 500000}

func newSeedCmd(f *cmdutil.Factory) *cobra.Command {
	var (
		bankAccount string
		count       int
	)
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Add sample transactions to the sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Before the picker, so a refused count asks nothing first.
			if err := validateSeedCount(count, "--count"); err != nil {
				return err
			}
			if count == 0 {
				count = defaultSeedCount
			}
			ba, err := bankaccount.PickOne(cmd.Context(), f, bankAccount, "bank-account", "Bank account")
			if err != nil {
				return err
			}
			return runSeed(cmd.Context(), f, ba, count)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&bankAccount, "bank-account", "", "Bank account xid to seed under (required)")
	fl.IntVar(&count, "count", 0, fmt.Sprintf("How many transactions to create, 1-%d (default %d)", maxSeedCount, defaultSeedCount))
	return cmd
}

func runSeed(ctx context.Context, f *cmdutil.Factory, baXID string, count int) error {
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}

	created := 0
	for i := 0; i < count; i++ {
		body := SimulateBody{
			Type:           "payment_in",
			Amount:         seedAmounts[i%len(seedAmounts)],
			BankAccountXID: baXID,
			Overrides:      map[string]string{"content": fmt.Sprintf("Sample transaction %d", i+1)},
		}
		if _, err := simulateOnce(ctx, client, body); err != nil {
			return fmt.Errorf("seeded %d of %d, then failed: %w", created, count, err)
		}
		created++
	}

	res := struct {
		Seeded int `json:"seeded" yaml:"seeded"`
	}{created}
	return f.RenderOutput(res, func() error {
		f.ErrPrintf("Seeded %d transactions on %s.\n", created, baXID)
		return nil
	})
}

func newResetCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:   "reset",
		Short: "Clear this company's sandbox data (webhooks + bank accounts)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReset(cmd.Context(), f)
		},
	}, map[string]string{
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnGuidance:    "Wipes this company's whole sandbox: every webhook and bank account, with their VAs and transactions.",
		cmdutil.AnnConfirms:    "always",
	})
}

type resetResult struct {
	Webhooks     int `json:"webhooks" yaml:"webhooks"`
	BankAccounts int `json:"bank_accounts" yaml:"bank_accounts"`
}

func runReset(ctx context.Context, f *cmdutil.Factory) error {
	// Print the resolved target so the confirm cannot say "sandbox" while aimed elsewhere.
	f.ErrPrintf("Target: %s\n", f.Target())
	if err := f.ConfirmMutation("clear all sandbox webhooks and bank accounts (and their VAs/transactions)", true); err != nil {
		return err
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	// Webhooks first; deleting an account cascades its VAs and transactions.
	var res resetResult
	if res.Webhooks, err = deleteAll(ctx, client, "/webhooks"); err != nil {
		return fmt.Errorf("reset webhooks: %w", err)
	}
	if res.BankAccounts, err = deleteAll(ctx, client, "/bank-accounts"); err != nil {
		return fmt.Errorf("reset bank accounts: %w", err)
	}
	return f.RenderOutput(res, func() error {
		fmt.Fprintf(f.IOStreams.ErrOut, "Deleted %d webhooks, %d bank accounts.\n", res.Webhooks, res.BankAccounts)
		return nil
	})
}

// maxResetPages backstops a server that keeps returning rows it never deletes.
const maxResetPages = 1000

// deleteAll deletes every row under listPath. A 404 counts as gone; a listing failure or an all-failed page
// is returned so the reset is not reported clean.
func deleteAll(ctx context.Context, client *api.Client, listPath string) (int, error) {
	deleted := 0
	for page := 0; page < maxResetPages; page++ {
		q := url.Values{}
		q.Set("per_page", "100")
		env, err := client.Get(ctx, listPath, q)
		if err != nil {
			return deleted, err
		}
		items, _, err := api.ListInto[idItem](env)
		if err != nil {
			return deleted, err
		}
		if len(items) == 0 {
			return deleted, nil
		}
		progressed := false
		var firstErr error
		for _, it := range items {
			if it.ID == "" {
				continue
			}
			idSeg, segErr := cmdutil.PathSegment("id", it.ID)
			if segErr != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("unusable id in the API response for %s", listPath)
				}
				continue
			}
			switch _, err := client.Delete(ctx, listPath+"/"+idSeg, api.WriteOptions{}); {
			case err == nil:
				deleted++
				progressed = true
			case api.IsNotFound(err):
				progressed = true // already gone
			case firstErr == nil:
				firstErr = err
			}
		}
		if !progressed {
			return deleted, firstErr
		}
	}
	return deleted, fmt.Errorf("reset did not converge after %d pages", maxResetPages)
}
