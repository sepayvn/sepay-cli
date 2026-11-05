package pg

import (
	"context"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// merchantInfo is the unwrapped GET /v1/merchant response; ApplicablePrices is kept as-is.
type merchantInfo struct {
	PaymentMethods   []paymentMethod `json:"payment_methods" yaml:"payment_methods"`
	ApplicablePrices any             `json:"payment_method_applicable_order_prices,omitempty" yaml:"payment_method_applicable_order_prices,omitempty"`
}

type paymentMethod struct {
	PaymentMethod string      `json:"payment_method" yaml:"payment_method"`
	Active        api.FlexInt `json:"active" yaml:"active"`
}

func newMerchantCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "merchant <command>",
		Short: "Payment Gateway merchant info",
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "get",
		Aliases: []string{"show"},
		Short:   "Show merchant details",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMerchantShow(cmd.Context(), f)
		},
	})
	return cmd
}

func runMerchantShow(ctx context.Context, f *cmdutil.Factory) error {
	client, err := f.NewPGClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/merchant", nil)
	if err != nil {
		return err
	}
	var m merchantInfo
	if err := resp.Into(&m); err != nil {
		return err
	}

	return f.RenderOutput(m, func() error {
		t := jsonfmt.NewTable("PAYMENT METHOD", "ACTIVE")
		for _, pm := range m.PaymentMethods {
			t.AddRow(pm.PaymentMethod, f.YesNo(int(pm.Active) == 1))
		}
		return f.RenderTable(t)
	})
}
