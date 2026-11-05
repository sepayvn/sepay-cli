// Package quota implements "sepay quota".
package quota

import (
	"context"
	"fmt"
	"strconv"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// Quota is the GET /v2/quota response; available=false means no plan.
type Quota struct {
	Available             bool            `json:"available" yaml:"available"`
	Cycle                 *Cycle          `json:"cycle,omitempty" yaml:"cycle,omitempty"`
	Quotas                map[string]Line `json:"quotas,omitempty" yaml:"quotas,omitempty"`
	VAPerBankAccountLimit int             `json:"va_per_bank_account_limit,omitempty" yaml:"va_per_bank_account_limit,omitempty"`
	Message               string          `json:"message,omitempty" yaml:"message,omitempty"`
}

// Cycle is the production billing window; absent in sandbox.
type Cycle struct {
	Begin        string `json:"begin" yaml:"begin"`
	End          string `json:"end" yaml:"end"`
	BillingCycle string `json:"billing_cycle" yaml:"billing_cycle"`
}

// Line is one resource's usage; nil Limit/Remaining means absent, not zero.
type Line struct {
	Used        int    `json:"used" yaml:"used"`
	Limit       *int   `json:"limit,omitempty" yaml:"limit,omitempty"`
	Remaining   *int   `json:"remaining,omitempty" yaml:"remaining,omitempty"`
	Period      string `json:"period" yaml:"period"`
	Enforcement string `json:"enforcement,omitempty" yaml:"enforcement,omitempty"`
}

// quotaOrder fixes the table row order.
var quotaOrder = []string{"transaction", "bank_account", "webhook", "api_key"}

// NewCmdQuota builds the "sepay quota" command.
func NewCmdQuota(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "quota",
		Short: "Show your plan quota and usage",
		Long: `Show how much of each quota your company has used. On production that is the
plan's transaction quota for the current billing cycle (company admins only); on
sandbox, the sandbox caps on transactions, bank accounts, webhooks and API keys.

USED is counted over PERIOD: cycle (the plan's billing cycle, printed below the
table), daily or total. LIMIT and REMAINING are "-" when the server sends no
figure. ENFORCEMENT says what happens at the limit:

  capped     new bank transactions are rejected once USED reaches LIMIT
  exempt     going over the limit does not stop transactions
  unlimited  the plan sets no limit
  unknown    the plan has no billing window, so usage cannot be measured
  blocked    sandbox: a create past the limit fails with quota_exceeded`,
		Example: `  sepay quota
  sepay quota --env production --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuota(cmd.Context(), f)
		},
	}
}

func runQuota(ctx context.Context, f *cmdutil.Factory) error {
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/quota", nil)
	if err != nil {
		return err
	}
	q, err := api.DataInto[Quota](env)
	if err != nil {
		return err
	}

	return f.RenderOutput(q, func() error {
		if !q.Available {
			fmt.Fprintln(f.IOStreams.ErrOut, "Quota is not available in this environment.")
			// available=false is also the zero value, so an empty body cannot be told from "no plan".
			if q.Message == "" && len(q.Quotas) == 0 && q.Cycle == nil {
				fmt.Fprintln(f.IOStreams.ErrOut,
					"The response carried nothing else, so the CLI may have failed to read it; check with: sepay api /quota")
			}
			return nil
		}
		t := jsonfmt.NewTable("RESOURCE", "USED", "LIMIT", "REMAINING", "PERIOD", "ENFORCEMENT")
		for _, name := range quotaOrder {
			line, ok := q.Quotas[name]
			if !ok {
				continue
			}
			t.AddRow(
				name,
				strconv.Itoa(line.Used),
				intOrDash(line.Limit),
				intOrDash(line.Remaining),
				line.Period,
				jsonfmt.OrDash(line.Enforcement),
			)
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		if q.Cycle != nil && q.Cycle.Begin != "" && q.Cycle.End != "" {
			f.Printf("Cycle: %s → %s (%s)\n", q.Cycle.Begin, q.Cycle.End, q.Cycle.BillingCycle)
		}
		if q.VAPerBankAccountLimit > 0 {
			f.Printf("VA per bank account: %d\n", q.VAPerBankAccountLimit)
		}
		return nil
	})
}

// intOrDash renders nil as "-", since "0" would read as no capacity.
func intOrDash(v *int) string {
	if v == nil {
		return "-"
	}
	return strconv.Itoa(*v)
}
