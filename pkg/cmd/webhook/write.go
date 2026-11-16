package webhook

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func newCreateCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"new"},
		Short:   "Create a webhook",
		Long: `Create a webhook: an HTTPS endpoint SePay POSTs each matching transaction to.
--bank-mode picks the bank accounts it covers, --event-type the direction and
--auth-type how SePay authenticates to your endpoint. On a terminal, whatever you
leave off is asked for. --bank-config <ba-xid>=on|off:all|list|none sets what each
account notifies: its account number (on or off) and its virtual accounts (all, the
--va-xid list, or none).`,
		Example: `  # SePay POSTs every event to your URL
  sepay webhook create --url https://example.com/hook --bank-mode all

  # no URL: watch the events here instead (sandbox)
  sepay webhook create --listen-only --bank-mode all
  sepay webhook listen --forward localhost:3000

  # signed, so the handler can verify what it receives
  sepay webhook create --listen-only --bank-mode all --auth-type hmac --secret "$SECRET"

  # an account that only notifies virtual accounts: two of them
  sepay webhook create --url https://example.com/hook --bank-mode single --bank-account <ba-xid> --bank-config <ba-xid>=off:list --va-xid <va-xid> --va-xid <va-xid>

  # a VA-only account (every VA) and an SMS-connected account (account number and every VA)
  sepay webhook create --url https://example.com/hook --bank-mode multi --bank-account <va-only-ba-xid> --bank-account <sms-ba-xid> --bank-config <va-only-ba-xid>=off:all --bank-config <sms-ba-xid>=on:all`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// --listen-only answers production with its own sandbox-only error, before any prompt.
			if listenOnly, _ := cmd.Flags().GetBool("listen-only"); !listenOnly {
				if err := f.CheckProdWrite("create webhook", ""); err != nil {
					return err
				}
			}
			body, err := buildWriteBody(cmd.Context(), f, cmd, writeOptions{forCreate: true})
			if err != nil {
				return err
			}
			return runCreate(cmd.Context(), f, body)
		},
	}
	addWriteFlags(cmd)
	cmdutil.AddDryRunFlag(cmd, f)
	// --url and --bank-account requirements depend on other flags, so only --bank-mode is marked.
	cmdutil.MarkRequired(cmd, "bank-mode")
	return cmdutil.Annotate(cmd, map[string]string{cmdutil.AnnScope: "webhook:write", cmdutil.AnnConfirms: "production=terminal"})
}

func newUpdateCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "update <webhook-xid>",
		Aliases: []string{"edit"},
		Short:   "Update a webhook (only the flags you pass change)",
		Long: `Change a webhook; only the flags you pass change. With no flags on a terminal,
it asks which settings to edit.

<webhook-xid> is the ID column of "sepay webhook list". Prompted for on a terminal; required otherwise.`,
		Example: `  sepay webhook update <webhook-xid> --url https://example.com/new-hook
  sepay webhook update <webhook-xid> --event-type in --skip-if-no-code

  # change one account's config; the other accounts keep theirs
  sepay webhook update <webhook-xid> --bank-config <ba-xid>=off:list --va-xid <va-xid>`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if listenOnly, _ := cmd.Flags().GetBool("listen-only"); !listenOnly {
				if err := f.CheckProdWrite("update webhook", cmdutil.FirstArg(args)); err != nil {
					return err
				}
			}
			xid, err := pickWebhook(cmd.Context(), f, cmdutil.FirstArg(args))
			if err != nil {
				return err
			}
			opts := writeOptions{xid: xid}
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
	cmdutil.AddDryRunFlag(cmd, f)
	return cmdutil.Annotate(cmd, map[string]string{cmdutil.AnnScope: "webhook:write", cmdutil.AnnConfirms: "production=terminal"})
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
	fl.Bool("skip-if-no-code", false, "Skip events without a pay code")
	fl.StringSlice("prefix-filter", nil, "Only forward these pay code prefixes")
	fl.String("oauth2-client-id", "", "OAuth2 client id (auth-type oauth2)")
	fl.String("oauth2-client-secret", "", "OAuth2 client secret (auth-type oauth2), or - to read it from stdin")
	fl.String("oauth2-token-url", "", "OAuth2 access-token URL (auth-type oauth2)")
	fl.String("bank-mode", "", "Targeting: single|multi|all (required on create when not prompting)")
	fl.StringSlice("bank-account", nil, "Bank account xid, repeat for multi (required for --bank-mode single|multi)")
	fl.StringSlice("sub-account", nil, "Virtual account xid to target (repeatable)")
	cmdutil.RenameFlag(fl, "va-xid", "sub-account", "Virtual account xid to target (repeatable)")
	fl.Bool("only-va", false, "Only virtual-account transactions")
	fl.StringSlice("bank-config", nil, "What an account notifies: xid=on|off:all|list|none, account number on/off and which VAs (repeatable)")
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
		if !receivePrimary && vaMode == vaModeNone {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("--bank-config %s=off:none notifies nothing; turn the account number on or pick a VA mode", xid)}
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
	auth       bool     // prompt for the auth scheme and its secrets
	targeting  bool     // prompt for bank_mode and its accounts
	listenOnly bool     // stored transport; narrows auth schemes
	current    *Webhook // the webhook being edited; its targeting is the default answer
	xid        string   // the webhook a flag update changes; its stored targeting fills the gaps
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
		if url, err = f.InputCheckedOrFlag(url, "url", "Webhook URL", func(v string) error {
			return cmdutil.ValidateDestinationURL(v, "--url")
		}); err != nil {
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

	// An oauth2 chosen by flag skips promptAuth, so a create still asks for, or requires, its fields.
	if forCreate && body["auth_type"] == "oauth2" {
		for _, need := range []struct{ key, flag, label string }{
			{"oauth2_client_id", "oauth2-client-id", "OAuth2 client id"},
			{"oauth2_access_token_url", "oauth2-token-url", "OAuth2 access-token URL"},
		} {
			if v, _ := body[need.key].(string); v != "" {
				continue
			}
			v, err := f.InputOrFlag("", need.flag, need.label)
			if err != nil {
				return nil, err
			}
			if v == "" {
				return nil, &cmdutil.FlagError{Err: fmt.Errorf("--%s is required with --auth-type oauth2", need.flag)}
			}
			body[need.key] = v
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

func runCreate(ctx context.Context, f *cmdutil.Factory, body map[string]any) error {
	if err := f.ConfirmMutation("create webhook", "", false); err != nil {
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
	if f.Global.DryRun {
		masked := maskedBody(body)
		after, err := previewCreate(masked)
		if err != nil {
			return err
		}
		return f.RenderDryRun(cmdutil.DryRun{
			Request:    cmdutil.DryRunRequest{Method: http.MethodPost, Path: "/webhooks", Body: masked},
			After:      after,
			NotChecked: []string{paymentSetupNotChecked},
		}, func() error { return f.RenderFields(after) })
	}
	env, err := client.Post(ctx, "/webhooks", api.WriteOptions{Body: body, IdempotencyKey: key})
	if err != nil {
		return explainTargeting(err)
	}
	w, err := api.DataInto[Webhook](env)
	if err != nil {
		return err
	}
	w = w.redacted()
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

	if err := f.ConfirmMutation("update webhook", xid, false); err != nil {
		return err
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	if f.Global.DryRun {
		return dryRunUpdate(ctx, f, client, xid, xidSeg, body)
	}
	env, err := client.Put(ctx, "/webhooks/"+xidSeg, api.WriteOptions{Body: body})
	if err != nil {
		return explainTargeting(err)
	}
	w, err := api.DataInto[Webhook](env)
	if err != nil {
		return err
	}
	w = w.redacted()
	return f.RenderOutput(w, func() error {
		f.ErrPrintf("%s Updated webhook %s.\n", f.SuccessIcon(), w.ID)
		return renderWebhookDetail(ctx, f, w)
	})
}

// dryRunUpdate shows the webhook as stored and with the fields the body sends, as sent.
func dryRunUpdate(ctx context.Context, f *cmdutil.Factory, client *api.Client, xid, xidSeg string, body map[string]any) error {
	stored, err := fetchWebhook(ctx, client, xid)
	if err != nil {
		return err
	}
	before := stored.redacted()
	p := previewUpdate(before, body)
	masked := maskedBody(body)
	return f.RenderDryRun(cmdutil.DryRun{
		Request:    cmdutil.DryRunRequest{Method: http.MethodPut, Path: "/webhooks/" + xidSeg, Body: masked},
		Before:     before,
		After:      p.after,
		NotChecked: []string{paymentSetupNotChecked},
	}, func() error { return f.RenderChanges(before, p.after, p.replaced...) })
}
