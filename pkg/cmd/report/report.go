// Package report implements "sepay report" (cashflow).
package report

import (
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// NewCmdReport builds the "sepay report" command group.
func NewCmdReport(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "report <command>",
		Aliases: []string{"rpt"},
		Short:   "Financial reports",
	}
	cmd.AddCommand(newCashflowCmd(f))
	return cmdutil.Stub(cmd)
}

func newCashflowCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := cmdutil.Stub(&cobra.Command{
		Use:   "cashflow",
		Short: "Cash-in/out summary over a period",
		Args:  cobra.NoArgs,
	})
	fl := cmd.Flags()
	fl.String("from", "", "Start date (YYYY-MM-DD)")
	fl.String("to", "", "End date (YYYY-MM-DD)")
	fl.String("bank-account", "", "Filter by bank account xid")
	fl.String("group-by", "", "Bucket size: day|week|month")
	return cmd
}
