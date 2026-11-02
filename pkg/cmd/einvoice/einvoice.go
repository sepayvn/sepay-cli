// Package einvoice implements "sepay einvoice".
package einvoice

import (
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// NewCmdEInvoice builds the "sepay einvoice" command group.
func NewCmdEInvoice(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "einvoice <command>",
		Aliases: []string{"inv"},
		Short:   "Issue Vietnamese electronic invoices",
		Long: `Vietnamese electronic invoicing under Decree 123/2020/NĐ-CP: create a draft, issue
it to the tax authority through a configured provider account, then read the
issued invoice back with its PDF and XML.

Creating and issuing are asynchronous — each command submits and then polls, so
the status you get on return is the one to act on. ` + "`sepay einvoice quota`" + ` is what
is left of the allowance. Endpoint reference: ` + "`sepay docs api`" + `.`,
		// Product credentials are brokered for the sandbox only.
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return f.UseSandbox("einvoice")
		},
	}
	cmd.AddCommand(
		newAuthCmd(f),
		newQuotaCmd(f),
		newAccountCmd(f),
		newInvoiceCmd(f),
		newErrorNotificationCmd(f),
	)
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnEnvs: "sandbox",
	})
}

func newErrorNotificationCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "error-notification <command>",
		Short: "Manage invoice error notifications",
	}
	cmd.AddCommand(
		cmdutil.Stub(&cobra.Command{Use: "create", Short: "Create an error notification", Args: cobra.NoArgs}),
		cmdutil.Stub(&cobra.Command{Use: "list", Short: "List error notifications", Args: cobra.NoArgs}),
		cmdutil.Stub(&cobra.Command{Use: "get <id>", Short: "Show an error notification", Args: cobra.ExactArgs(1)}),
	)
	return cmdutil.Stub(cmd)
}
