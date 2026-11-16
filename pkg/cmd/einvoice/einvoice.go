// Package einvoice implements "sepay einvoice".
package einvoice

import (
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// NewCmdEInvoice builds the "sepay einvoice" command group.
func NewCmdEInvoice(f *cmdutil.Factory) *cobra.Command {
	invoiceCmd, productionReads := newInvoiceCmd(f)
	cmd := &cobra.Command{
		Use:     "einvoice <command>",
		Aliases: []string{"inv"},
		Short:   "Vietnamese electronic invoices",
		Long: `Vietnamese electronic invoicing under Decree 123/2020/NĐ-CP: create a draft, issue
it to the tax authority through a configured provider account, then read the
issued invoice back with its PDF and XML.

Creating and issuing are asynchronous: each command submits and prints a tracking
code, and create-status or issue-status reports the result. ` + "`sepay einvoice quota`" + ` is what
is left of the allowance. Endpoint reference: https://developer.sepay.vn/en/einvoice-api/v1/tong-quan.`,
		// Product credentials are brokered for the sandbox only. "einvoice invoice list" and "get"
		// read production through API v2 with the user's own token instead, so on production they
		// skip the sandbox pin. On sandbox they keep it like every other einvoice command: they read
		// API v2 there too, and the pin refuses an API host that is not the sandbox one.
		PersistentPreRunE: func(c *cobra.Command, _ []string) error {
			if productionReads[c] && f.ActiveEnv() == config.EnvProduction {
				return nil
			}
			return f.UseSandbox("einvoice")
		},
	}
	cmd.AddCommand(
		newAuthCmd(f),
		newQuotaCmd(f),
		newAccountCmd(f),
		invoiceCmd,
		newErrorNotificationCmd(f),
	)
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnEnvs:     "sandbox",
		cmdutil.AnnGuidance: "Every einvoice command except `einvoice invoice list` and `einvoice invoice get` is sandbox-only.",
	})
}

func newErrorNotificationCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "error-notification <command>",
		Short: "Invoice error notifications",
	}
	cmd.AddCommand(
		cmdutil.Stub(&cobra.Command{Use: "create", Short: "Create an error notification", Args: cobra.NoArgs}),
		cmdutil.Stub(&cobra.Command{Use: "list", Short: "List error notifications", Args: cobra.NoArgs}),
		cmdutil.Stub(&cobra.Command{Use: "get <notification-id>", Short: "Show an error notification", Args: cobra.ExactArgs(1)}),
	)
	return cmdutil.Stub(cmd)
}
