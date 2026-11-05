// Package pg implements "sepay pg" (Payment Gateway).
package pg

import (
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// NewCmdPG builds the "sepay pg" command group.
func NewCmdPG(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pg <command>",
		Short: "Payment Gateway orders and merchant",
		Long: `Payment Gateway is SePay's hosted checkout: you create an order, send the buyer
to a SePay-hosted page, and they pay there by card, QR Banking or QR NAPAS. It is
the alternative to watching a bank account you own — one integration instead of a
contract per bank.

Start an order with ` + "`sepay pg checkout init`" + `, follow it with ` + "`sepay pg order`" + `.
A paid order reports back over the gateway's own IPN channel, not over
` + "`sepay webhook`" + ` — stream it with ` + "`sepay pg ipn listen`" + `.
Walkthrough: https://developer.sepay.vn/en/agents/cli/cong-thanh-toan.`,
		// Product credentials are brokered for the sandbox only.
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return f.UseSandbox("pg")
		},
	}
	cmd.AddCommand(newAuthCmd(f), newOrderCmd(f), newMerchantCmd(f), newCheckoutCmd(f), newIPNCmd(f))
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnEnvs: "sandbox",
		cmdutil.AnnGuidance: "That restriction is on these commands, not on the gateway: it has a production host " +
			"an application reaches directly (https://developer.sepay.vn/en/cong-thanh-toan/gioi-thieu). A gateway order notifies over IPN, so " +
			"`sepay webhook listen` never carries one — use `sepay pg ipn listen`.",
	})
}
