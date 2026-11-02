package bankhub

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	ibh "github.com/sepayvn/sepay-cli/internal/bankhub"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

type transaction struct {
	TransactionID      string      `json:"transaction_id" yaml:"transaction_id"`
	TransactionDate    string      `json:"transaction_date" yaml:"transaction_date"`
	BankAccountXID     string      `json:"bank_account_xid" yaml:"bank_account_xid"`
	CompanyXID         string      `json:"company_xid" yaml:"company_xid"`
	BrandName          string      `json:"brand_name" yaml:"brand_name"`
	AccountNumber      string      `json:"account_number" yaml:"account_number"`
	VA                 *string     `json:"va" yaml:"va"`
	VAXID              *string     `json:"va_xid" yaml:"va_xid"`
	ReferenceNumber    string      `json:"reference_number" yaml:"reference_number"`
	TransferType       string      `json:"transfer_type" yaml:"transfer_type"`
	Amount             api.FlexInt `json:"amount" yaml:"amount"`
	TransactionContent string      `json:"transaction_content" yaml:"transaction_content"`
	PaymentCode        *string     `json:"payment_code" yaml:"payment_code"`
	WebhookSuccess     api.FlexInt `json:"webhook_success" yaml:"webhook_success"`
}

func newTransactionCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{Use: "transaction <command>", Aliases: []string{"tx", "txn"}, Short: "List transactions"}
	cmd.AddCommand(newTransactionListCmd(f), newTransactionGetCmd(f))
	return cmd
}

func newTransactionListCmd(f *cmdutil.Factory) *cobra.Command {
	var company, bankAccount, transferType, from, to, q string
	var page, perPage int
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List transactions",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.ValidateChoice(cmd, "type", transferTypes); err != nil {
				return err
			}
			if err := cmdutil.ValidateDays(cmd, "from", "to"); err != nil {
				return err
			}
			query := url.Values{}
			for param, val := range map[string]string{
				"company_xid":            company,
				"bank_account_xid":       bankAccount,
				"transfer_type":          transferType,
				"start_transaction_date": from,
				"end_transaction_date":   to,
				"q":                      q,
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
			return runTransactionList(cmd.Context(), f, query)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&company, "company", "", "Filter by company xid")
	fl.StringVar(&bankAccount, "bank-account", "", "Filter by bank account xid")
	fl.StringVar(&transferType, "type", "", "Filter by transfer type: "+strings.Join(transferTypes, "|"))
	fl.StringVar(&from, "from", "", "From transaction date (YYYY-MM-DD)")
	fl.StringVar(&to, "to", "", "To transaction date (YYYY-MM-DD)")
	fl.StringVar(&q, "q", "", "Search transactions")
	fl.IntVar(&page, "page", 0, "Page number")
	fl.IntVar(&perPage, "per-page", 0, "Rows per page (max 100)")
	return cmd
}

func runTransactionList(ctx context.Context, f *cmdutil.Factory, query url.Values) error {
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/transaction", query)
	if err != nil {
		return err
	}
	txns, meta, err := ibh.ListInto[transaction](resp)
	if err != nil {
		return err
	}

	return f.RenderOutput(txns, func() error {
		if len(txns) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No transactions found.")
			return nil
		}
		t := jsonfmt.NewTable("TRANSACTION", "DATE", "TYPE", "AMOUNT", "CONTENT", "WEBHOOK").Truncatable("CONTENT")
		for _, tx := range txns {
			t.AddRow(tx.TransactionID, tx.TransactionDate, tx.TransferType,
				jsonfmt.FormatVND(int64(tx.Amount)), tx.TransactionContent, f.YesNo(int(tx.WebhookSuccess) == 1))
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		if meta != nil && meta.Total > 0 {
			fmt.Fprintf(f.IOStreams.ErrOut, "page %d/%d · %d of %d\n",
				meta.CurrentPage, meta.PageCount, len(txns), meta.Total)
		}
		return nil
	})
}

func newTransactionGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get <transaction-id>",
		Short: "Show a transaction",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// An empty id routes to the list endpoint.
			if args[0] == "" {
				return &cmdutil.FlagError{Err: fmt.Errorf("a transaction id is required")}
			}
			return runTransactionGet(cmd.Context(), f, args[0])
		},
	}
}

func runTransactionGet(ctx context.Context, f *cmdutil.Factory, id string) error {
	idSeg, err := cmdutil.PathSegment("id", id)
	if err != nil {
		return err
	}
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/transaction/"+idSeg, nil)
	if err != nil {
		return err
	}
	tx, err := ibh.DataInto[transaction](resp)
	if err != nil {
		return err
	}

	vaLabel := "-"
	if tx.VA != nil && *tx.VA != "" {
		vaLabel = *tx.VA
	}
	return f.RenderOutput(tx, func() error {
		f.Printf(
			"Transaction: %s\nDate:        %s\nType:        %s\nAmount:      %s\nAccount:     %s (%s)\nVA:          %s\nReference:   %s\nContent:     %s\nWebhook:     %s\n",
			tx.TransactionID, tx.TransactionDate, tx.TransferType, jsonfmt.FormatVND(int64(tx.Amount)),
			tx.AccountNumber, tx.BrandName, vaLabel, jsonfmt.OrDash(tx.ReferenceNumber),
			tx.TransactionContent, f.YesNo(int(tx.WebhookSuccess) == 1))
		return nil
	})
}
