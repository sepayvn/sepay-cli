package einvoice

import (
	"context"
	"strconv"

	iei "github.com/sepayvn/sepay-cli/internal/einvoice"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// usage is the E-Invoice quota (GET /v1/usage).
type usage struct {
	QuotaRemaining           int     `json:"quota_remaining" yaml:"quota_remaining"`
	TaxAuthorityApprovedDate *string `json:"tax_authority_approved_date" yaml:"tax_authority_approved_date"`
}

func newQuotaCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "quota",
		Short: "Show e-invoice usage and quota",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuota(cmd.Context(), f)
		},
	}
}

func runQuota(ctx context.Context, f *cmdutil.Factory) error {
	client, err := f.NewEInvoiceClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/usage", nil)
	if err != nil {
		return err
	}
	u, err := iei.DataInto[usage](resp)
	if err != nil {
		return err
	}

	return f.RenderOutput(u, func() error {
		t := jsonfmt.NewTable("QUOTA REMAINING", "TAX APPROVED")
		approved := ""
		if u.TaxAuthorityApprovedDate != nil {
			approved = *u.TaxAuthorityApprovedDate
		}
		t.AddRow(strconv.Itoa(u.QuotaRemaining), jsonfmt.OrDash(approved))
		return f.RenderTable(t)
	})
}
