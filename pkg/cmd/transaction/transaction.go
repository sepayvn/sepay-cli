// Package transaction implements "sepay transaction".
package transaction

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// Transaction is one bank transaction from GET /v2/transactions.
// Maskable fields are pointers: the API sends null without per-bank permission.
type Transaction struct {
	ID                 string  `json:"id" yaml:"id"`
	TransactionDate    string  `json:"transaction_date" yaml:"transaction_date"`
	AccountNumber      string  `json:"account_number" yaml:"account_number"`
	VA                 *string `json:"va" yaml:"va"`
	TransferType       string  `json:"transfer_type" yaml:"transfer_type"`
	AmountIn           *int64  `json:"amount_in" yaml:"amount_in"`
	AmountOut          *int64  `json:"amount_out" yaml:"amount_out"`
	Accumulated        *int64  `json:"accumulated" yaml:"accumulated"`
	TransactionContent *string `json:"transaction_content" yaml:"transaction_content"`
	ReferenceNumber    *string `json:"reference_number" yaml:"reference_number"`
	Code               *string `json:"code" yaml:"code"`
	BankBrandName      string  `json:"bank_brand_name" yaml:"bank_brand_name"`
	BankAccountID      string  `json:"bank_account_id" yaml:"bank_account_id"`
	VAID               *string `json:"va_id" yaml:"va_id"`
	WebhookSuccess     *int    `json:"webhook_success" yaml:"webhook_success"`
}

// NewCmdTransaction builds the "sepay transaction" command group.
func NewCmdTransaction(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "transaction <command>",
		Aliases: []string{"tx", "txn"},
		Short:   "Transactions on your bank accounts",
		Long: `A transaction is one movement of money, in or out, that SePay observed on one of
your bank accounts. It is read-only: SePay records what the bank reported.

"transaction list" filters by bank account, VA, direction, amount and date;
"transaction get" shows one by xid. To hear about new ones as they arrive, use a
webhook (sepay webhook).`,
	}
	cmd.AddCommand(newListCmd(f), newGetCmd(f))
	return cmd
}

func newListCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List transactions",
		Long: `List transactions on your bank accounts, a page at a time. Filter by bank
account, VA, bank, direction, amount, date or content, and sort by date or amount.
--since-id returns only transactions after a known one, for polling; --fields
trims the columns.

The ID column is the <transaction-xid> "transaction get" takes.`,
		Example: `  # money in on one account this month
  sepay transaction list --bank-account <bank-account-xid> --transfer-type in --from 2026-07-01

  # poll for anything newer than the last transaction you saw
  sepay transaction list --since-id <transaction-xid> --json`,
		Args: cobra.NoArgs,
	}
	collect := cmdutil.BindListFlags(cmd.Flags(), map[string]string{
		"q":                     "Search reference number, content, or code",
		"bank-account-id":       "Filter by bank account xid",
		"va-id":                 "Filter by VA xid",
		"bank-brand-name":       "Filter by bank (e.g. ACB, BIDV)",
		"transaction-date-from": "Start date, inclusive (YYYY-MM-DD)",
		"transaction-date-to":   "End date, inclusive (YYYY-MM-DD)",
		"amount-in-min":         "Minimum money-in amount",
		"amount-in-max":         "Maximum money-in amount",
		"amount-out-min":        "Minimum money-out amount",
		"amount-out-max":        "Maximum money-out amount",
		"reference-number":      "Filter by reference number (exact)",
		"transaction-content":   "Filter by content",
		"transfer-type":         "Filter by direction: in|out",
		"webhook-success":       "Filter by webhook status: 0|1",
		"since-id":              "Return transactions after this xid",
		"transaction-date-sort": "Sort by date: asc|desc",
		"amount-in-sort":        "Sort by money-in: asc|desc",
		"amount-out-sort":       "Sort by money-out: asc|desc",
		"fields":                "Comma-separated fields to return",
	})
	fl := cmd.Flags()
	cmdutil.RenameFlag(fl, "search", "q", "Search reference number, content, or code")
	cmdutil.RenameFlag(fl, "bank-account", "bank-account-id", "Filter by bank account xid")
	cmdutil.RenameFlag(fl, "va-xid", "va-id", "Filter by virtual account xid")
	cmdutil.RenameFlag(fl, "bank", "bank-brand-name", "Filter by bank (e.g. ACB, BIDV)")
	cmdutil.RenameFlag(fl, "from", "transaction-date-from", "Start date, inclusive (YYYY-MM-DD)")
	cmdutil.RenameFlag(fl, "to", "transaction-date-to", "End date, inclusive (YYYY-MM-DD)")
	// The API ignores a malformed filter instead of rejecting it.
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		sort := []string{"asc", "desc"}
		if err := cmdutil.ValidateChoices(cmd, map[string][]string{
			"transfer-type":         {"in", "out"},
			"webhook-success":       {"0", "1"},
			"transaction-date-sort": sort,
			"amount-in-sort":        sort,
			"amount-out-sort":       sort,
		}); err != nil {
			return err
		}
		if err := cmdutil.ValidateAmounts(cmd, "amount-in-min", "amount-in-max", "amount-out-min", "amount-out-max"); err != nil {
			return err
		}
		if err := cmdutil.ValidateDates(cmd, "from", "to", "transaction-date-from", "transaction-date-to"); err != nil {
			return err
		}
		if err := cmdutil.ValidateDateOrder(cmd, "transaction-date-from", "transaction-date-to"); err != nil {
			return err
		}
		if err := cmdutil.IncludeEndDay(cmd, "to", "transaction-date-to"); err != nil {
			return err
		}
		for _, pair := range [][2]string{{"amount-in-min", "amount-in-max"}, {"amount-out-min", "amount-out-max"}} {
			if err := cmdutil.ValidateAmountOrder(cmd, pair[0], pair[1]); err != nil {
				return err
			}
		}
		if err := validateFields(cmd); err != nil {
			return err
		}
		return runList(cmd.Context(), f, collect())
	}
	return cmd
}

// transactionFields lists the columns the API serves, read off the struct.
func transactionFields() map[string]bool {
	t := reflect.TypeOf(Transaction{})
	names := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		names[strings.Split(t.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	return names
}

// validateFields rejects unknown columns: the API drops them and the struct would print zeros.
func validateFields(cmd *cobra.Command) error {
	if !cmd.Flags().Changed("fields") {
		return nil
	}
	v, err := cmd.Flags().GetString("fields")
	if err != nil || v == "" {
		return nil
	}
	known := transactionFields()
	for _, name := range strings.Split(v, ",") {
		// Split the way the API does.
		if name = strings.TrimSpace(name); name != "" && !known[name] {
			return &cmdutil.FlagError{Err: fmt.Errorf("--fields has no column %q", name)}
		}
	}
	return nil
}

func newGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get <transaction-xid>",
		Short: "Show a transaction",
		Long: `Show one transaction in full.

<transaction-xid> is the ID column of "sepay transaction list". It is required;
there is no prompt.`,
		Example: `  sepay transaction get <transaction-xid> --json`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// An empty xid routes to the list endpoint.
			if args[0] == "" {
				return &cmdutil.FlagError{Err: fmt.Errorf("a transaction xid is required")}
			}
			return runGet(cmd.Context(), f, args[0])
		},
	}
}

func runList(ctx context.Context, f *cmdutil.Factory, query url.Values) error {
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/transactions", query)
	if err != nil {
		return err
	}
	txns, page, err := api.ListInto[Transaction](env)
	if err != nil {
		return err
	}

	if err := f.RenderOutput(txns, func() error {
		if len(txns) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No transactions found.")
			return nil
		}
		t := jsonfmt.NewTable("ID", "DATE", "AMOUNT", "CONTENT", "CODE", "BANK", "WH").Truncatable("CONTENT", "BANK")
		for _, tx := range txns {
			t.AddRow(tx.ID, tx.TransactionDate, signedAmount(f, tx),
				dashHidden(tx.TransactionContent), dash(tx.Code), tx.BankBrandName, webhookMark(f, tx.WebhookSuccess))
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	f.PrintPageInfo(page, len(txns))
	return nil
}

func runGet(ctx context.Context, f *cmdutil.Factory, xid string) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/transactions/"+xidSeg, nil)
	if err != nil {
		return err
	}
	tx, err := api.DataInto[Transaction](env)
	if err != nil {
		return err
	}

	return f.RenderOutput(tx, func() error {
		t := jsonfmt.NewTable("FIELD", "VALUE").Truncatable("VALUE")
		t.AddRow("ID", tx.ID)
		t.AddRow("Date", tx.TransactionDate)
		t.AddRow("Amount", signedAmount(f, tx))
		t.AddRow("Accumulated", amountOrHidden(tx.Accumulated))
		t.AddRow("Content", dashHidden(tx.TransactionContent))
		t.AddRow("Reference", dashHidden(tx.ReferenceNumber))
		t.AddRow("Code", dash(tx.Code))
		t.AddRow("Bank", tx.BankBrandName)
		t.AddRow("Account", tx.AccountNumber)
		t.AddRow("VA", dash(tx.VA))
		t.AddRow("Webhook", webhookMark(f, tx.WebhookSuccess))
		return f.RenderTable(t)
	})
}

const hidden = "(hidden)"

// signedAmount renders the amount with a sign from TransferType; neither part is guessed when missing.
func signedAmount(f *cmdutil.Factory, tx Transaction) string {
	switch tx.TransferType {
	case "out":
		return f.IOStreams.Red("-" + amountOrHidden(tx.AmountOut))
	case "in":
		return f.IOStreams.Green("+" + amountOrHidden(tx.AmountIn))
	}
	return hidden
}

func dashHidden(s *string) string {
	if s == nil {
		return hidden
	}
	if *s == "" {
		return "-"
	}
	return *s
}

func amountOrHidden(v *int64) string {
	if v == nil {
		return hidden
	}
	return jsonfmt.FormatVND(*v)
}

func webhookMark(f *cmdutil.Factory, s *int) string {
	switch {
	case s == nil:
		return "-"
	case *s == 1:
		return f.IOStreams.Green("ok")
	default:
		return f.IOStreams.Red("fail")
	}
}

func dash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}
