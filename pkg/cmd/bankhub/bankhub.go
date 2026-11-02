// Package bankhub implements "sepay bankhub", the Bank Hub server API.
package bankhub

import (
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// transferTypes are Bank Hub's transaction directions.
var transferTypes = []string{"credit", "debit"}

// NewCmdBankHub builds the "sepay bankhub" command group.
func NewCmdBankHub(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "bankhub <command>",
		Aliases: []string{"bh"},
		Short:   "Bank Hub companies, accounts, and link tokens",
		Long: `Bank Hub connects *other businesses'* bank accounts, for a platform onboarding
merchants. Create a company per merchant, mint a link token, send them to the
hosted page where they link their own bank; you then read the resulting accounts
and transactions, and receive balance-change notifications.

Use ` + "`sepay bank-account`" + ` and ` + "`sepay transaction`" + ` when the account is your own.
Bank Hub runs on its own host with a short-lived credential the CLI re-mints for
you (` + "`sepay bankhub auth`" + `). Endpoint reference: ` + "`sepay docs api`" + `.`,
		// Product credentials are brokered for the sandbox only.
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return f.UseSandbox("bankhub")
		},
	}
	cmd.AddCommand(
		newAuthCmd(f),
		newCompanyCmd(f),
		newBankAccountCmd(f),
		newVACmd(f),
		newTransactionCmd(f),
		newWebhookCmd(f),
		newIpnCmd(f),
		newLinkTokenCmd(f),
		newSandboxCmd(f),
	)
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnEnvs: "sandbox",
	})
}

// newVACmd is a stub: Bank Hub has no /v1/va endpoint; VAs come from `bank-account get`.
func newVACmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{Use: "va <command>", Short: "List virtual accounts"}
	cmd.AddCommand(
		cmdutil.Stub(&cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List virtual accounts", Args: cobra.NoArgs}),
		cmdutil.Stub(&cobra.Command{Use: "get <id>", Short: "Show a virtual account", Args: cobra.ExactArgs(1)}),
	)
	return cmdutil.Stub(cmd)
}
