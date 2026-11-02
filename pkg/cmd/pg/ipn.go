package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
	"github.com/sepayvn/sepay-cli/pkg/listen"

	"github.com/spf13/cobra"
)

// These live on my.sepay.vn, not the gateway host.
const (
	ipnPath       = "/pg/ipn"
	ipnListenPath = "/pg/ipn/listen"
)

// IPNEvents are the gateway's notification_type values, used by --events.
var IPNEvents = []string{"ORDER_PAID", "RENEWAL_ORDER_PAID", "TRANSACTION_VOID"}

// Empty --auth-type turns auth off, so it is not in the set.
var (
	ipnAuthTypes    = []string{"SECRET_KEY"}
	ipnContentTypes = []string{"application/json"}
)

// ipnConfig is GET /v2/pg/ipn; SecretKey arrives server-masked.
type ipnConfig struct {
	Configured         bool         `json:"configured" yaml:"configured"`
	IpnURL             string       `json:"ipn_url" yaml:"ipn_url"`
	AuthType           string       `json:"auth_type" yaml:"auth_type"`
	RequestContentType string       `json:"request_content_type" yaml:"request_content_type"`
	SecretKey          string       `json:"secret_key" yaml:"secret_key"`
	Active             api.FlexBool `json:"active" yaml:"active"`
}

func newIpnCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ipn <command>",
		Short: "Gateway IPN: stream it locally, or configure where it goes",
		Long: `A paid gateway order reports back over IPN, a channel of its own — not over
` + "`sepay webhook`" + `.

Point IPN at your endpoint with ` + "`sepay pg ipn set`" + `, then ` + "`sepay pg ipn listen`" + ` mirrors
each delivery to a handler on your machine — same body, same headers — so you can
build against real notifications.

Body reference: ` + "`sepay docs ipn-payload`" + `.`,
	}
	cmd.AddCommand(newIpnListenCmd(f), newIpnGetCmd(f), newIpnSetCmd(f))
	return cmd
}

func newIpnListenCmd(f *cmdutil.Factory) *cobra.Command {
	var order string
	cmd := &cobra.Command{
		Use:     "listen",
		Aliases: []string{"tail"},
		Short:   "Stream gateway IPN to a local handler over SSE",
		Long: `Hold one outbound SSE stream and POST every IPN notification to a local handler,
so a gateway integration can be built without exposing your machine.

Each event carries the same body bytes and the same headers your configured IPN
endpoint just received, so your handler runs its real path — including its own
X-Secret-Key check. That X-Secret-Key is replayed to a loopback handler; for any
other address it is withheld unless you pass ` + "`--forward-credentials`" + `.

It mirrors deliveries, so it needs an IPN endpoint set with ` + "`sepay pg ipn set`" + ` and
reports first attempts only, not SePay's retries.

A forward your handler cannot receive is re-sent locally on SePay's schedule
(` + "`--forward-retries 0`" + ` delivers once). As on the real channel, a handler that answers
4xx or 5xx counts as delivered and is not retried.`,
		Example: `  # stream to a local handler
  sepay pg ipn listen --forward localhost:3000

  # only paid orders, printing each body
  sepay pg ipn listen --forward localhost:3000 --events ORDER_PAID --print`,
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

			return listen.Run(cmd, f, ipnListenSource(order), opts)
		},
	}
	listen.AddFlags(cmd)
	listen.DescribeEvents(cmd, IPNEvents)
	cmd.Flags().StringVar(&order, "order", "", "Only forward IPN for this order id")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnGuidance: "Gateway IPN has no signature — a static X-Secret-Key header is its only auth, " +
			"so do not write an HMAC check against it. Needs an endpoint set by `sepay pg ipn set`.",
	})
}

// ipnListenSource describes this channel to the listen loop.
func ipnListenSource(order string) listen.Source {
	return listen.Source{
		MintPath:   ipnListenPath,
		RevokePath: ipnListenPath,
		Noun:       "gateway IPN",
		Scope:      "pg_ipn",
		Describe:   describeIpnCoverage,
		Accept:     acceptOrder(order),
		Tag:        orderTag,
		// Any HTTP answer is a completed delivery here, and the first retry is due at once.
		FirstRetryImmediate: true,
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
		// `pg ipn set` answers the same 404, so do not point at it.
		fmt.Fprintf(out, "! this company has no active Payment Gateway merchant, so no IPN can be configured or delivered\n")
		fmt.Fprintf(out, "  check it: sepay pg merchant show\n")
	case err != nil:
	case !cfg.Configured:
		fmt.Fprintf(out, "! no IPN endpoint configured, so nothing is delivered and nothing reaches this stream\n")
		fmt.Fprintf(out, "  set one: sepay pg ipn set <url>\n")
	case !bool(cfg.Active):
		fmt.Fprintf(out, "! IPN delivery is off, so nothing reaches this stream\n")
		cmdutil.Fprintf(out, "  turn it on: sepay pg ipn set %s --active\n", cfg.IpnURL)
	}
}

// acceptOrder filters to one order id, read from the body since the envelope omits it.
func acceptOrder(want string) func(listen.Frame) bool {
	if want == "" {
		return nil
	}
	return func(fr listen.Frame) bool {
		return orderID(fr) == want
	}
}

func orderID(fr listen.Frame) string {
	var p struct {
		Order struct {
			OrderID string `json:"order_id"`
		} `json:"order"`
	}
	if err := json.Unmarshal(fr.Payload, &p); err != nil {
		return ""
	}
	return p.Order.OrderID
}

// orderTag names the order by invoice number, the value `pg order get` takes.
func orderTag(io *iostreams.IOStreams, fr listen.Frame) string {
	return listen.Tag(io, "order", orderInvoice(fr))
}

// orderInvoice falls back to order_id when the payload has no invoice number.
func orderInvoice(fr listen.Frame) string {
	if inv := listen.PayloadString(fr, "order", "order_invoice_number"); inv != "" {
		return inv
	}
	return orderID(fr)
}

func newIpnGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "get",
		Aliases: []string{"show"},
		Short:   "Show where gateway IPN is delivered",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := f.NewAPIClient()
			if err != nil {
				return err
			}
			env, err := client.Get(cmd.Context(), ipnPath, nil)
			if err != nil {
				return err
			}
			cfg, err := api.DataInto[ipnConfig](env)
			if err != nil {
				return err
			}
			return renderIpnConfig(f, cfg)
		},
	}
}

func newIpnSetCmd(f *cmdutil.Factory) *cobra.Command {
	var secret, authType, contentType string
	var active bool
	cmd := &cobra.Command{
		Use:   "set [url]",
		Short: "Set where gateway IPN is delivered",
		Args:  cmdutil.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The server answers a bad enum with a retryable 422, so check before the write.
			if err := cmdutil.ValidateChoices(cmd, map[string][]string{
				"auth-type":    ipnAuthTypes,
				"content-type": ipnContentTypes,
			}); err != nil {
				return err
			}
			client, err := f.NewAPIClient()
			if err != nil {
				return err
			}
			current := readIpnConfig(cmd.Context(), client)

			var arg string
			if len(args) > 0 {
				arg = args[0]
			}
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
			if cmd.Flags().Changed("secret") {
				resolved, serr := f.SecretFromFlag(secret, "secret")
				if serr != nil {
					return serr
				}
				body["secret_key"] = resolved
				body["auth_type"] = "SECRET_KEY"
			}
			if cmd.Flags().Changed("auth-type") {
				body["auth_type"] = authType
			}
			if cmd.Flags().Changed("content-type") {
				body["request_content_type"] = contentType
			}
			if cmd.Flags().Changed("active") {
				body["active"] = boolToInt(active)
			}
			if err := promptIpnAuth(f, cmd, body, current); err != nil {
				return err
			}

			if err := f.ConfirmMutation("set the gateway IPN URL", true); err != nil {
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
			cfg.SecretKey = cmdutil.MaskSecret(cfg.SecretKey)
			return renderIpnConfig(f, cfg)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&secret, "secret", "", "Shared secret sent as X-Secret-Key, or - to read it from stdin (also turns auth on)")
	fl.StringVar(&authType, "auth-type", "", "Auth scheme: SECRET_KEY, or empty to turn auth off")
	fl.StringVar(&contentType, "content-type", "", "Request content type: application/json")
	fl.BoolVar(&active, "active", false, "Turn delivery on (--active) or off (--active=false)")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnConfirms: "always",
		cmdutil.AnnGuidance: "Changes only what you pass: a key you omit keeps its stored value, so setting a URL " +
			"cannot turn auth off. The URL must be publicly reachable — SePay delivers there, and " +
			"`sepay pg ipn listen` mirrors those deliveries.",
	})
}

// promptIpnAuth asks for the delivery auth, reading the secret without echo.
func promptIpnAuth(f *cmdutil.Factory, cmd *cobra.Command, body map[string]any, current ipnConfig) error {
	if cmd.Flags().Changed("secret") || cmd.Flags().Changed("auth-type") || !f.CanPrompt() {
		return nil
	}

	var choices []cmdutil.PromptChoice
	if current.Configured && current.SecretKey != "" {
		choices = append(choices, cmdutil.PromptChoice{
			Label: "Keep the current secret (" + current.SecretKey + ")", Value: "keep",
		})
	}
	choices = append(choices,
		cmdutil.PromptChoice{Label: "Shared secret, sent as X-Secret-Key", Value: "secret"},
		cmdutil.PromptChoice{Label: "No authentication", Value: "none"},
	)

	pick, err := f.SelectChoiceOrFlag("", "auth-type", "Authentication", choices)
	if err != nil {
		return err
	}

	switch pick {
	case "keep":
	case "secret":
		s, err := f.Password("Shared secret")
		if err != nil {
			return err
		}
		if s == "" {
			return &cmdutil.FlagError{Err: fmt.Errorf("a shared secret cannot be empty")}
		}
		body["secret_key"] = s
		body["auth_type"] = "SECRET_KEY"
	case "none":
		// An empty auth_type removes it.
		body["auth_type"] = ""
	}

	return nil
}

// readIpnConfig is fetchIpnConfig that treats any failure as unset.
func readIpnConfig(ctx context.Context, client *api.Client) ipnConfig {
	cfg, err := fetchIpnConfig(ctx, client)
	if err != nil {
		return ipnConfig{}
	}
	return cfg
}

// fetchIpnConfig returns the error so callers can tell no IPN row from no PG merchant.
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
				"No IPN configured, so nothing is delivered and `sepay pg ipn listen` receives nothing. Add an endpoint: sepay pg ipn set <url>\n")
			return nil
		}
		secret := cfg.SecretKey
		if secret == "" {
			secret = "-"
		}
		f.Printf(
			"URL:      %s\nAuth:     %s\nContent:  %s\nSecret:   %s\nActive:   %s\n",
			cfg.IpnURL, cfg.AuthType, cfg.RequestContentType, secret, f.YesNo(bool(cfg.Active)))
		return nil
	})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
