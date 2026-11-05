package einvoice

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/sepayvn/sepay-cli/internal/api"
	iei "github.com/sepayvn/sepay-cli/internal/einvoice"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// providerAccount is an E-Invoice provider account; Id is create's provider_account_id.
type providerAccount struct {
	ID                       string       `json:"id" yaml:"id"`
	Provider                 string       `json:"provider" yaml:"provider"`
	Active                   api.FlexBool `json:"active" yaml:"active"`
	TaxAuthorityApprovedDate *string      `json:"tax_authority_approved_date,omitempty" yaml:"tax_authority_approved_date,omitempty"`
	Templates                []template   `json:"templates,omitempty" yaml:"templates,omitempty"`
}

type template struct {
	TemplateCode  string `json:"template_code" yaml:"template_code"`
	InvoiceSeries string `json:"invoice_series" yaml:"invoice_series"`
	InvoiceLabel  string `json:"invoice_label" yaml:"invoice_label"`
}

func newAccountCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account <command>",
		Short: "E-invoice provider accounts",
	}
	cmd.AddCommand(newAccountListCmd(f), newAccountGetCmd(f))
	return cmd
}

func newAccountListCmd(f *cmdutil.Factory) *cobra.Command {
	var page, perPage int
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List provider accounts",
		Example: `  sepay einvoice account list
  sepay einvoice account list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := url.Values{}
			if page > 0 {
				query.Set("page", strconv.Itoa(page))
			}
			if perPage > 0 {
				query.Set("per_page", strconv.Itoa(perPage))
			}
			return runAccountList(cmd.Context(), f, query)
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&page, "page", 0, "Page number")
	fl.IntVar(&perPage, "per-page", 0, "Rows per page (max 100)")
	return cmd
}

func runAccountList(ctx context.Context, f *cmdutil.Factory, query url.Values) error {
	client, err := f.NewEInvoiceClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/provider-accounts", query)
	if err != nil {
		return err
	}
	accounts, paging, err := iei.ListInto[providerAccount](resp)
	if err != nil {
		return err
	}

	return f.RenderOutput(accounts, func() error {
		if len(accounts) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No provider accounts found, so no invoice can be issued.")
			return nil
		}
		t := jsonfmt.NewTable("ID", "PROVIDER", "ACTIVE")
		for _, a := range accounts {
			t.AddRow(a.ID, a.Provider, f.YesNo(bool(a.Active)))
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		printPaging(f, paging, len(accounts))
		return nil
	})
}

func newAccountGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get <account-id>",
		Short: "Show a provider account and its invoice templates",
		Long: `Show a provider account and the invoice templates it can issue with.

<account-id> is the ID column of "sepay einvoice account list". Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var arg string
			if len(args) > 0 {
				arg = args[0]
			}
			id, err := pickProviderAccount(cmd.Context(), f, arg)
			if err != nil {
				return err
			}
			return runAccountGet(cmd.Context(), f, id)
		},
	}
}

func runAccountGet(ctx context.Context, f *cmdutil.Factory, id string) error {
	idSeg, err := cmdutil.PathSegment("id", id)
	if err != nil {
		return err
	}
	client, err := f.NewEInvoiceClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/provider-accounts/"+idSeg, nil)
	if err != nil {
		return err
	}
	a, err := iei.DataInto[providerAccount](resp)
	if err != nil {
		return err
	}

	return f.RenderOutput(a, func() error {
		approved := ""
		if a.TaxAuthorityApprovedDate != nil {
			approved = *a.TaxAuthorityApprovedDate
		}
		f.Printf("ID:            %s\nProvider:      %s\nActive:        %s\nTax approved:  %s\n",
			a.ID, a.Provider, f.YesNo(bool(a.Active)), jsonfmt.OrDash(approved))
		if len(a.Templates) == 0 {
			return nil
		}
		fmt.Fprintln(f.IOStreams.Out)
		t := jsonfmt.NewTable("TEMPLATE", "SERIES", "LABEL").Truncatable("LABEL")
		for _, tpl := range a.Templates {
			t.AddRow(tpl.TemplateCode, tpl.InvoiceSeries, tpl.InvoiceLabel)
		}
		return f.RenderTable(t)
	})
}
