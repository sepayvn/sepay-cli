// Package root builds the top-level "sepay" command and its global flags.
package root

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmd/api"
	"github.com/sepayvn/sepay-cli/pkg/cmd/apikey"
	"github.com/sepayvn/sepay-cli/pkg/cmd/auth"
	bankaccount "github.com/sepayvn/sepay-cli/pkg/cmd/bank-account"
	"github.com/sepayvn/sepay-cli/pkg/cmd/bankhub"
	"github.com/sepayvn/sepay-cli/pkg/cmd/completion"
	configcmd "github.com/sepayvn/sepay-cli/pkg/cmd/config"
	"github.com/sepayvn/sepay-cli/pkg/cmd/docs"
	"github.com/sepayvn/sepay-cli/pkg/cmd/einvoice"
	"github.com/sepayvn/sepay-cli/pkg/cmd/paycode"
	"github.com/sepayvn/sepay-cli/pkg/cmd/pg"
	"github.com/sepayvn/sepay-cli/pkg/cmd/quota"
	"github.com/sepayvn/sepay-cli/pkg/cmd/report"
	"github.com/sepayvn/sepay-cli/pkg/cmd/sandbox"
	"github.com/sepayvn/sepay-cli/pkg/cmd/transaction"
	"github.com/sepayvn/sepay-cli/pkg/cmd/va"
	"github.com/sepayvn/sepay-cli/pkg/cmd/version"
	"github.com/sepayvn/sepay-cli/pkg/cmd/webhook"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// NewCmdRoot builds the top-level "sepay" command with its global flags and subcommands.
func NewCmdRoot(f *cmdutil.Factory) *cobra.Command {
	g := f.Global

	var mapFormat string
	var showVersion bool

	// Compose group PersistentPreRunE hooks (pg's sandbox pin) with root's instead of shadowing it.
	cobra.EnableTraverseRunHooks = true

	cmd := &cobra.Command{
		Use:   "sepay <command> <subcommand> [flags]",
		Short: "SePay's command-line tool for developers",
		Long: `sepay is the command-line tool for SePay, a Vietnamese payment platform whose
core service reports money arriving on the bank accounts a business owns, in near
real time. Around that sit a hosted checkout (pg), access to other businesses'
accounts (bankhub), and electronic invoicing (einvoice).

Use it to watch transactions, register and stream webhooks, decide how payments
match orders, charge a buyer on a hosted page, and simulate all of it in sandbox.
It provisions and tests an integration; a deployed application calls the same
public API itself and needs no sepay binary.

Runs against the sandbox environment by default; add --env production to hit live.
Every read/list command supports --json for scripts and AI agents.

Start with ` + "`sepay docs overview`" + `.`,
		Example: `  sepay docs overview
  sepay docs glossary
  sepay --map=paths
  sepay version --json`,

		// main() prints errors and sets the exit code.
		SilenceErrors: true,
		SilenceUsage:  true,

		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.ValidatePaging(cmd); err != nil {
				return err
			}
			switch g.Output {
			case "table", "json", "yaml":
			default:
				return &cmdutil.FlagError{Err: fmt.Errorf(
					"invalid value %q for --output (must be table, json, or yaml)", g.Output)}
			}
			switch g.Env {
			case "", config.EnvSandbox, config.EnvProduction:
			default:
				return &cmdutil.FlagError{Err: fmt.Errorf(
					"invalid value %q for --env (must be sandbox or production)", g.Env)}
			}
			if g.NoColor || g.Output != "table" {
				f.IOStreams.SetColorEnabled(false)
			}
			// slog to stderr, silent unless --verbose, so stdout stays clean for --json.
			level := slog.LevelWarn
			if g.Verbose {
				level = slog.LevelDebug
			}
			slog.SetDefault(slog.New(slog.NewTextHandler(f.IOStreams.ErrOut, &slog.HandlerOptions{Level: level})))
			return nil
		},

		// Root is runnable so --map is reachable.
		RunE: func(cmd *cobra.Command, _ []string) error {
			if showVersion {
				return version.Print(f)
			}
			if mapFormat != "" {
				return writeCommandMap(f.IOStreams.Out, cmd.Root(), f, mapFormat)
			}
			return cmd.Help()
		},
	}
	installUsageFunc(cmd, f)

	cmd.CompletionOptions.DisableDefaultCmd = true

	pf := cmd.PersistentFlags()
	pf.StringVarP(&g.Profile, "profile", "p", "", "Profile (account) to use")
	pf.StringVar(&g.Env, "env", "", "Environment: sandbox|production (default sandbox)")
	pf.StringVarP(&g.Output, "output", "o", "table", "Output format: table|json|yaml")
	pf.Var(jsonShorthand{output: &g.Output}, "json", "Shorthand for -o json")
	pf.Lookup("json").NoOptDefVal = "true"
	pf.BoolVarP(&g.Yes, "yes", "y", false, "Skip confirmation (cannot bypass OTP)")
	pf.BoolVarP(&g.Quiet, "quiet", "q", false, "Suppress non-essential output")
	pf.BoolVarP(&g.Verbose, "verbose", "v", false, "Enable debug logging (tokens/OTP always redacted)")
	pf.BoolVar(&g.NoColor, "no-color", false, "Disable ANSI color")
	pf.BoolVar(&g.NoInput, "no-input", false, "Never prompt; a missing required value is an error")
	pf.StringVar(&g.ConfigPath, "config", "", "Config file path (default: <os-config-dir>/sepay/config.yml, see: sepay docs environment)")
	pf.StringVar(&g.APIBase, "api-base", "", "Override the API host (advanced/testing)")
	pf.StringVar(&g.IdempotencyKey, "idempotency-key", "",
		"Reuse this X-Idempotency-Key for the mutation (retry an interrupted write without repeating it)")

	// Root-local so it does not show in every subcommand's help.
	cmd.Flags().StringVar(&mapFormat, "map", "",
		"Print the command tree and exit: "+strings.Join(mapFormats, "|"))
	cmd.Flags().Lookup("map").NoOptDefVal = "tree"
	cmd.Flags().BoolVar(&showVersion, "version", false, "Print version and exit")

	// Map flag errors to the usage exit code, dropping pflag's parser cause.
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		var ive *pflag.InvalidValueError
		if errors.As(err, &ive) {
			fl := ive.GetFlag()
			name := "--" + fl.Name
			if fl.Shorthand != "" && fl.ShorthandDeprecated == "" {
				name = fmt.Sprintf("-%s, --%s", fl.Shorthand, fl.Name)
			}
			return &cmdutil.FlagError{Err: fmt.Errorf("invalid argument %q for %q flag", ive.GetValue(), name)}
		}
		return &cmdutil.FlagError{Err: err}
	})

	cmd.AddCommand(version.NewCmdVersion(f))
	cmd.AddCommand(auth.NewCmdAuth(f))
	// Fresh instances: a cobra command attaches to one parent only.
	cmd.AddCommand(auth.NewCmdLogin(f))
	cmd.AddCommand(auth.NewCmdLogout(f))

	cmd.AddCommand(webhook.NewCmdWebhook(f))
	cmd.AddCommand(transaction.NewCmdTransaction(f))
	cmd.AddCommand(report.NewCmdReport(f))
	cmd.AddCommand(bankaccount.NewCmdBankAccount(f))
	cmd.AddCommand(va.NewCmdVA(f))
	cmd.AddCommand(quota.NewCmdQuota(f))
	cmd.AddCommand(paycode.NewCmdPaycode(f))
	cmd.AddCommand(apikey.NewCmdAPIKey(f))
	cmd.AddCommand(sandbox.NewCmdSandbox(f))

	cmd.AddCommand(pg.NewCmdPG(f))
	cmd.AddCommand(einvoice.NewCmdEInvoice(f))
	cmd.AddCommand(bankhub.NewCmdBankHub(f))
	cmd.AddCommand(api.NewCmdAPI(f))
	cmd.AddCommand(configcmd.NewCmdConfig(f))
	cmd.AddCommand(docs.NewCmdDocs(f))
	cmd.AddCommand(completion.NewCmdCompletion(f))
	rejectUnknownSubcommands(cmd)
	_ = cmd.RegisterFlagCompletionFunc("profile", cmdutil.CompleteProfiles(f))
	cmdutil.CompleteEnumFlags(cmd)
	return cmd
}

// jsonShorthand sets --output during parsing, early enough for the errors cobra raises before PersistentPreRunE.
type jsonShorthand struct{ output *string }

func (j jsonShorthand) String() string   { return "false" }
func (j jsonShorthand) Type() string     { return "bool" }
func (j jsonShorthand) IsBoolFlag() bool { return true }

func (j jsonShorthand) Set(v string) error {
	on, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("invalid boolean value %q", v)
	}
	if on {
		*j.output = "json"
	}
	return nil
}

// rejectUnknownSubcommands makes a mistyped subcommand a usage error; cobra otherwise prints help with exit 0.
// In RunE, not Args: cobra returns ErrHelp for a non-runnable command before validating args.
func rejectUnknownSubcommands(cmd *cobra.Command) {
	for _, c := range cmd.Commands() {
		rejectUnknownSubcommands(c)
	}
	if cmd.Runnable() || !cmd.HasSubCommands() {
		return
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			return &cmdutil.FlagError{Err: fmt.Errorf(
				"unknown command %q for %q", args[0], c.CommandPath())}
		}
		return c.Help()
	}
}
