package bankhub

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"
	"github.com/sepayvn/sepay-cli/pkg/listen"

	"github.com/spf13/cobra"
)

// These live on my.sepay.vn, not the Bank Hub host.
const (
	ipnPath       = "/bankhub/ipn"
	ipnListenPath = "/bankhub/ipn/listen"
)

// IPNEvents are the frame types this channel carries (the body's transfer_type).
var IPNEvents = []string{"transaction_credit", "transaction_debit"}

// ipnConfig is GET /v2/bankhub/ipn; APIKey arrives server-masked.
type ipnConfig struct {
	Configured bool   `json:"configured" yaml:"configured"`
	IpnURL     string `json:"ipn_url" yaml:"ipn_url"`
	APIKey     string `json:"api_key" yaml:"api_key"`
	// Set only: linked accounts moved to the new endpoint, out of AccountsTotal.
	AccountsUpdated *int `json:"accounts_updated,omitempty" yaml:"accounts_updated,omitempty"`
	AccountsTotal   *int `json:"accounts_total,omitempty" yaml:"accounts_total,omitempty"`
}

func newIpnCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ipn <command>",
		Short: "Bank Hub IPN: stream it locally, or configure where it goes",
		Long: `A transfer on a merchant's linked account reports back over IPN (a balance-change notification)
— a channel of its own, separate from the ` + "`sepay bankhub webhook`" + ` that carries
account-linking events.

Point IPN at your endpoint with ` + "`sepay bankhub ipn set`" + `, then
` + "`sepay bankhub ipn listen`" + ` mirrors each delivery to a handler on your machine —
same body, same headers — so you can build against real notifications.

Body reference: https://developer.sepay.vn/en/bankhub/thong-bao-bien-dong-so-du`,
	}
	cmd.AddCommand(newIpnListenCmd(f), newIpnGetCmd(f), newIpnSetCmd(f))
	return cmd
}

func newIpnListenCmd(f *cmdutil.Factory) *cobra.Command {
	var bankAccount string
	cmd := &cobra.Command{
		Use:     "listen",
		Aliases: []string{"tail"},
		Short:   "Stream Bank Hub IPN to a local handler over SSE",
		Long: `Hold one outbound SSE stream and POST every IPN notification to a local handler,
so a Bank Hub integration can be built without exposing your machine.

Each event carries the same body bytes and the same headers your configured IPN
endpoint just received, so your handler runs its real path — including its own
Authorization check. That Authorization is replayed to a loopback handler; for any
other address it is withheld unless you pass ` + "`--forward-credentials`" + `.

It mirrors deliveries, so it needs an endpoint set with ` + "`sepay bankhub ipn set`" + ` and
reports first attempts only, not SePay's retries.

A forward your handler cannot receive is re-sent locally on SePay's schedule
(` + "`--forward-retries 0`" + ` delivers once).`,
		Example: `  # stream to a local handler
  sepay bankhub ipn listen --forward localhost:3000

  # only money in, printing each body
  sepay bankhub ipn listen --forward localhost:3000 --events transaction_credit --print`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := listen.ReadOptions(cmd)
			if err != nil {
				return err
			}
			if err := listen.CheckEvents(opts.Events, IPNEvents); err != nil {
				return err
			}
			forwardAddr, err := listen.ResolveForward(f, opts.Forward)
			if err != nil {
				return err
			}
			opts.Forward = forwardAddr

			return listen.Run(cmd, f, ipnListenSource(bankAccount), opts)
		},
	}
	listen.AddFlags(cmd)
	listen.DescribeEvents(cmd, IPNEvents)
	cmd.Flags().StringVar(&bankAccount, "bank-account", "", "Only forward IPN for this bank account xid")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnGuidance: "Bank Hub IPN has no signature — a static `Authorization: Apikey …` header is its only " +
			"auth, so do not write an HMAC check against it. Needs an endpoint set by `sepay bankhub ipn set`.",
	})
}

// ipnListenSource describes this channel to the listen loop. Its webhook row has no retry_conditions, so RetryNon2xx stays nil.
func ipnListenSource(bankAccount string) listen.Source {
	return listen.Source{
		MintPath:   ipnListenPath,
		RevokePath: ipnListenPath,
		Noun:       "Bank Hub IPN",
		Scope:      "bankhub_ipn",
		Describe:   describeIpnCoverage,
		Accept:     acceptBankAccount(bankAccount),
		Tag:        bankAccountTag,
	}
}

// describeIpnCoverage warns up front when nothing can arrive. Best-effort.
func describeIpnCoverage(ctx context.Context, f *cmdutil.Factory) {
	client, err := f.NewAPIClient()
	if err != nil {
		return
	}
	cfg, err := fetchIpnConfig(ctx, client)

	out := f.IOStreams.ErrOut
	switch {
	case api.IsNotFound(err):
		// `bankhub ipn set` answers the same 404, so do not point at it.
		fmt.Fprintf(out, "! this company has no Bank Hub sandbox merchant, so no IPN can be configured or delivered\n")
		fmt.Fprintf(out, "  create one: sepay bankhub auth\n")
	case err != nil:
	case !cfg.Configured:
		fmt.Fprintf(out, "! no IPN endpoint configured, so nothing is delivered and nothing reaches this stream\n")
		fmt.Fprintf(out, "  set one: sepay bankhub ipn set <url>\n")
	case cfg.AccountsTotal != nil && *cfg.AccountsTotal == 0:
		fmt.Fprintf(out, "! no bank account is linked to this Bank Hub merchant, so nothing is delivered and nothing reaches this stream\n")
		fmt.Fprintf(out, "  link one: sepay bankhub link-token create\n")
	}
}

// acceptBankAccount filters to one linked account, read from the body since the envelope omits it.
func acceptBankAccount(want string) func(listen.Frame) bool {
	if want == "" {
		return nil
	}
	return func(fr listen.Frame) bool {
		return bankAccountXID(fr) == want
	}
}

func bankAccountXID(fr listen.Frame) string {
	var p struct {
		BankAccountXID string `json:"bank_account_xid"`
	}
	if err := json.Unmarshal(fr.Payload, &p); err != nil {
		return ""
	}
	return p.BankAccountXID
}

// bankAccountTag prints the transaction_id and account id whole, as commands accept them (Bank Hub ids are not UUIDs).
func bankAccountTag(io *iostreams.IOStreams, fr listen.Frame) string {
	return listen.Tag(io, "txn", listen.PayloadString(fr, "transaction_id")) +
		listen.Tag(io, "ba", bankAccountXID(fr))
}

func newIpnGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "get",
		Aliases: []string{"show"},
		Short:   "Show where Bank Hub IPN is delivered",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := f.NewAPIClient()
			if err != nil {
				return err
			}
			cfg, err := fetchIpnConfig(cmd.Context(), client)
			if err != nil {
				return err
			}
			return renderIpnConfig(f, cfg)
		},
	}
}

func newIpnSetCmd(f *cmdutil.Factory) *cobra.Command {
	var apiKey string
	cmd := &cobra.Command{
		Use:   "set [url]",
		Short: "Set where Bank Hub IPN is delivered",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := f.NewAPIClient()
			if err != nil {
				return err
			}
			// A failed read is not an empty config; the auth prompt needs to tell them apart.
			current, readErr := fetchIpnConfig(cmd.Context(), client)
			if readErr != nil {
				current = ipnConfig{}
			}

			arg := cmdutil.FirstArg(args)
			if arg == "" && current.Configured && f.CanPrompt() {
				f.ErrPrintf("%s\n", f.IOStreams.Dim("current: "+current.IpnURL))
			}
			url, err := f.InputOrArg(arg, "an IPN URL", "IPN URL")
			if err != nil {
				return err
			}
			if err := cmdutil.ValidateDestinationURL(url, "IPN URL"); err != nil {
				return err
			}

			body := map[string]any{"ipn_url": url}
			if cmd.Flags().Changed("api-key") {
				resolved, kerr := f.SecretFromFlag(apiKey, "api-key")
				if kerr != nil {
					return kerr
				}
				body["api_key"] = resolved
			}
			if err := promptIpnAuth(f, cmd, body, current, readErr == nil); err != nil {
				return err
			}

			if err := f.ConfirmMutation("set the Bank Hub IPN URL", true); err != nil {
				return err
			}

			env, err := client.Post(cmd.Context(), ipnPath, api.WriteOptions{Body: body})
			if err != nil {
				return err
			}
			cfg, err := api.DataInto[ipnConfig](env)
			if err != nil {
				return err
			}
			// POST echoes are not masked like the GET.
			cfg.APIKey = cmdutil.MaskSecret(cfg.APIKey)
			return renderIpnConfig(f, cfg)
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Shared key sent as `Authorization: Apikey …`, or - to read it from stdin (empty turns auth off)")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnConfirms: "always",
		cmdutil.AnnGuidance: "Changes only what you pass: omitting --api-key keeps the stored key, so setting a URL " +
			"cannot turn auth off. Also rewrites the endpoint on every already-linked account, reported as " +
			"accounts_updated. The URL must be publicly reachable — SePay delivers there, and " +
			"`sepay bankhub ipn listen` mirrors those deliveries.",
	})
}

// promptIpnAuth asks for the delivery auth, reading the key without echo.
// Skipped when the read failed, so a transient error cannot offer to wipe a live key.
func promptIpnAuth(f *cmdutil.Factory, cmd *cobra.Command, body map[string]any, current ipnConfig, readOK bool) error {
	if cmd.Flags().Changed("api-key") || !f.CanPrompt() {
		return nil
	}
	if !readOK {
		fmt.Fprintf(f.IOStreams.ErrOut, "%s\n",
			f.IOStreams.Dim("could not read the current config; keeping the stored key (pass --api-key to change it)"))
		return nil
	}

	var choices []cmdutil.PromptChoice
	if current.Configured && current.APIKey != "" {
		choices = append(choices, cmdutil.PromptChoice{
			Label: "Keep the current key (" + current.APIKey + ")", Value: "keep",
		})
	}
	choices = append(choices,
		cmdutil.PromptChoice{Label: "Shared key, sent as Authorization: Apikey …", Value: "key"},
		cmdutil.PromptChoice{Label: "No authentication", Value: "none"},
	)

	pick, err := f.SelectChoiceOrFlag("", "api-key", "Authentication", choices)
	if err != nil {
		return err
	}

	switch pick {
	case "keep":
	case "key":
		s, err := f.Password("Shared key")
		if err != nil {
			return err
		}
		if s == "" {
			return &cmdutil.FlagError{Err: fmt.Errorf("a shared key cannot be empty")}
		}
		body["api_key"] = s
	case "none":
		// An empty key removes it.
		body["api_key"] = ""
	}

	return nil
}

// fetchIpnConfig returns the error so callers can tell no URL, no merchant and a failed read apart.
func fetchIpnConfig(ctx context.Context, client *api.Client) (ipnConfig, error) {
	env, err := client.Get(ctx, ipnPath, nil)
	if err != nil {
		return ipnConfig{}, err
	}
	return api.DataInto[ipnConfig](env)
}

func renderIpnConfig(f *cmdutil.Factory, cfg ipnConfig) error {
	return f.RenderOutput(cfg, func() error {
		if !cfg.Configured {
			fmt.Fprintf(f.IOStreams.Out,
				"No IPN configured, so nothing is delivered and `sepay bankhub ipn listen` receives nothing. Add an endpoint: sepay bankhub ipn set <url>\n")
			return nil
		}
		f.Printf("URL:      %s\nKey:      %s\n", cfg.IpnURL, jsonfmt.OrDash(cfg.APIKey))
		if cfg.AccountsUpdated != nil && cfg.AccountsTotal != nil {
			fmt.Fprintf(f.IOStreams.Out, "Accounts: %d of %d updated\n", *cfg.AccountsUpdated, *cfg.AccountsTotal)
			if *cfg.AccountsUpdated < *cfg.AccountsTotal {
				fmt.Fprintf(f.IOStreams.ErrOut,
					"! %d linked account(s) still deliver to the previous endpoint; run the same set again to finish\n",
					*cfg.AccountsTotal-*cfg.AccountsUpdated)
			}
		}
		return nil
	})
}
