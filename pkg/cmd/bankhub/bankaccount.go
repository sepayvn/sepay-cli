package bankhub

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/sepayvn/sepay-cli/internal/api"
	ibh "github.com/sepayvn/sepay-cli/internal/bankhub"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// bankAccount is a Bank Hub linked bank account; va_list is the only way to read VAs.
type bankAccount struct {
	XID               string      `json:"xid" yaml:"xid"`
	CompanyXID        string      `json:"company_xid" yaml:"company_xid"`
	BrandName         string      `json:"brand_name" yaml:"brand_name"`
	AccountHolderName string      `json:"account_holder_name" yaml:"account_holder_name"`
	AccountNumber     string      `json:"account_number" yaml:"account_number"`
	Accumulated       api.FlexInt `json:"accumulated" yaml:"accumulated"`
	AccountType       string      `json:"account_type" yaml:"account_type"`
	BankAPIConnected  api.FlexInt `json:"bank_api_connected" yaml:"bank_api_connected"`
	Active            api.FlexInt `json:"active" yaml:"active"`
	LastTransaction   *string     `json:"last_transaction" yaml:"last_transaction"`
	CreatedAt         string      `json:"created_at" yaml:"created_at"`
	UpdatedAt         string      `json:"updated_at" yaml:"updated_at"`
	VAList            []va        `json:"va_list,omitempty" yaml:"va_list,omitempty"`
}

type va struct {
	XID           string      `json:"xid" yaml:"xid"`
	SubAccount    string      `json:"sub_account" yaml:"sub_account"`
	SubHolderName string      `json:"sub_holder_name" yaml:"sub_holder_name"`
	Active        api.FlexInt `json:"active" yaml:"active"`
	CreatedAt     string      `json:"created_at" yaml:"created_at"`
	UpdatedAt     string      `json:"updated_at" yaml:"updated_at"`
}

func newBankAccountCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{Use: "bank-account <command>", Aliases: []string{"ba"}, Short: "List linked bank accounts"}
	cmd.AddCommand(newBankAccountListCmd(f), newBankAccountGetCmd(f))
	return cmd
}

func newBankAccountListCmd(f *cmdutil.Factory) *cobra.Command {
	var company, bank, q string
	var page, perPage int
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List linked bank accounts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := url.Values{}
			for param, val := range map[string]string{
				"company_xid": company,
				"bank_id":     bank,
				"q":           q,
			} {
				if val != "" {
					query.Set(param, val)
				}
			}
			if page > 0 {
				query.Set("page", strconv.Itoa(page))
			}
			if perPage > 0 {
				query.Set("per_page", strconv.Itoa(perPage))
			}
			return runBankAccountList(cmd.Context(), f, query)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&company, "company", "", "Filter by company xid")
	fl.StringVar(&bank, "bank", "", "Filter by bank id")
	fl.StringVar(&q, "q", "", "Search bank accounts")
	fl.IntVar(&page, "page", 0, "Page number")
	fl.IntVar(&perPage, "per-page", 0, "Rows per page (max 100)")
	return cmd
}

func runBankAccountList(ctx context.Context, f *cmdutil.Factory, query url.Values) error {
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/bank-account", query)
	if err != nil {
		return err
	}
	accounts, meta, err := ibh.ListInto[bankAccount](resp)
	if err != nil {
		return err
	}

	return f.RenderOutput(accounts, func() error {
		if len(accounts) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut,
				"No bank accounts linked, so no transaction is reported.\nLink one: sepay bankhub link-token create --company <xid> --purpose LINK_BANK_ACCOUNT")
			return nil
		}
		t := jsonfmt.NewTable("XID", "BANK", "ACCOUNT", "HOLDER", "TYPE", "ACTIVE").Truncatable("BANK", "HOLDER")
		for _, a := range accounts {
			t.AddRow(a.XID, a.BrandName, a.AccountNumber, a.AccountHolderName, a.AccountType, f.YesNo(int(a.Active) == 1))
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		if meta != nil && meta.Total > 0 {
			fmt.Fprintf(f.IOStreams.ErrOut, "page %d/%d · %d of %d\n",
				meta.CurrentPage, meta.PageCount, len(accounts), meta.Total)
		}
		return nil
	})
}

func newBankAccountGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get [xid]",
		Short: "Show a bank account and its virtual accounts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var arg string
			if len(args) > 0 {
				arg = args[0]
			}
			xid, err := pickBankAccount(cmd.Context(), f, arg, "", "Select a bank account")
			if err != nil {
				return err
			}
			a, err := getBankAccount(cmd.Context(), f, xid)
			if err != nil {
				return err
			}
			return f.RenderOutput(a, func() error {
				f.Printf(
					"XID:      %s\nBank:     %s\nAccount:  %s\nHolder:   %s\nType:     %s\nBalance:  %s\nActive:   %s\n",
					a.XID, a.BrandName, a.AccountNumber, a.AccountHolderName, a.AccountType,
					jsonfmt.FormatVND(int64(a.Accumulated)), f.YesNo(int(a.Active) == 1))
				if len(a.VAList) == 0 {
					return nil
				}
				fmt.Fprintln(f.IOStreams.Out)
				return renderVATable(f, a.VAList)
			})
		},
	}
}

// getBankAccount fetches one bank account with its va_list.
func getBankAccount(ctx context.Context, f *cmdutil.Factory, xid string) (bankAccount, error) {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return bankAccount{}, err
	}
	client, err := f.NewBankHubClient()
	if err != nil {
		return bankAccount{}, err
	}
	resp, err := client.Get(ctx, "/v1/bank-account/"+xidSeg, nil)
	if err != nil {
		return bankAccount{}, err
	}
	return ibh.DataInto[bankAccount](resp)
}

func renderVATable(f *cmdutil.Factory, vas []va) error {
	t := jsonfmt.NewTable("VA XID", "SUB-ACCOUNT", "HOLDER", "ACTIVE", "CREATED").Truncatable("HOLDER")
	for _, v := range vas {
		t.AddRow(v.XID, v.SubAccount, jsonfmt.OrDash(v.SubHolderName), f.YesNo(int(v.Active) == 1), v.CreatedAt)
	}
	return f.RenderTable(t)
}
