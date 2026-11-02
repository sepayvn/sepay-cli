package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	ipg "github.com/sepayvn/sepay-cli/internal/pg"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// order is a PG order; amounts are decimal strings.
type order struct {
	ID                   string        `json:"id" yaml:"id"`
	CustomerID           *string       `json:"customer_id" yaml:"customer_id"`
	OrderID              string        `json:"order_id" yaml:"order_id"`
	OrderInvoiceNumber   string        `json:"order_invoice_number" yaml:"order_invoice_number"`
	OrderStatus          string        `json:"order_status" yaml:"order_status"`
	OrderAmount          string        `json:"order_amount" yaml:"order_amount"`
	OrderCurrency        string        `json:"order_currency" yaml:"order_currency"`
	OrderDescription     string        `json:"order_description" yaml:"order_description"`
	AuthenticationStatus *string       `json:"authentication_status" yaml:"authentication_status"`
	CreatedAt            string        `json:"created_at" yaml:"created_at"`
	UpdatedAt            string        `json:"updated_at" yaml:"updated_at"`
	Transactions         []transaction `json:"transactions,omitempty" yaml:"transactions,omitempty"`
}

// transaction is one card/bank transaction under an order.
type transaction struct {
	ID                         string  `json:"id" yaml:"id"`
	PaymentMethod              string  `json:"payment_method" yaml:"payment_method"`
	TransactionType            string  `json:"transaction_type" yaml:"transaction_type"`
	TransactionAmount          string  `json:"transaction_amount" yaml:"transaction_amount"`
	TransactionCurrency        string  `json:"transaction_currency" yaml:"transaction_currency"`
	TransactionStatus          string  `json:"transaction_status" yaml:"transaction_status"`
	AuthenticationStatus       *string `json:"authentication_status" yaml:"authentication_status"`
	CardNumber                 string  `json:"card_number" yaml:"card_number"`
	CardHolderName             string  `json:"card_holder_name" yaml:"card_holder_name"`
	CardExpiry                 string  `json:"card_expiry" yaml:"card_expiry"`
	CardFundingMethod          string  `json:"card_funding_method" yaml:"card_funding_method"`
	CardBrand                  string  `json:"card_brand" yaml:"card_brand"`
	TransactionDate            string  `json:"transaction_date" yaml:"transaction_date"`
	TransactionLastUpdatedDate string  `json:"transaction_last_updated_date" yaml:"transaction_last_updated_date"`
}

// maskCards re-masks any bare PAN at decode, since the API does not guarantee masking.
func maskCards(orders []order) {
	for i := range orders {
		for j := range orders[i].Transactions {
			orders[i].Transactions[j].CardNumber = maskPAN(orders[i].Transactions[j].CardNumber)
		}
	}
}

// maskPAN masks only an all-digit 13-19 char value, leaving gateway-masked ones intact.
func maskPAN(s string) string {
	if len(s) < 13 || len(s) > 19 {
		return s
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return s
		}
	}
	return "****" + s[len(s)-4:]
}

func newOrderCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "order <command>",
		Short: "Manage payment-gateway orders",
	}
	cmd.AddCommand(newOrderListCmd(f), newOrderGetCmd(f), newOrderCancelCmd(f), newOrderVoidCmd(f))
	return cmd
}

// orderStatuses is App\Enums\PgOrderStatus; the filter is a case-sensitive match.
var orderStatuses = []string{
	"AUTHENTICATED", "AUTHENTICATION_INITIATED", "AUTHENTICATION_NOT_NEEDED",
	"AUTHENTICATION_UNSUCCESSFUL", "AUTHORIZED", "CANCELLED", "CAPTURED",
	"CHARGEBACK_PROCESSED", "DISBURSED", "DISPUTED", "EXCESSIVELY_REFUNDED",
	"FAILED", "FUNDING", "INITIATED", "PARTIALLY_CAPTURED", "PARTIALLY_REFUNDED",
	"REFUNDED", "REFUND_REQUESTED", "VERIFIED",
}

func newOrderListCmd(f *cmdutil.Factory) *cobra.Command {
	var status, from, to, customer, q string
	var page, perPage int
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List orders",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.ValidateChoice(cmd, "status", orderStatuses); err != nil {
				return err
			}
			if err := cmdutil.ValidateDays(cmd, "from", "to"); err != nil {
				return err
			}
			query := url.Values{}
			for param, val := range map[string]string{
				"order_status":    status,
				"from_created_at": from,
				"to_created_at":   to,
				"customer_id":     customer,
				"q":               q,
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
			return runOrderList(cmd.Context(), f, query)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&status, "status", "", "Filter by order status, e.g. CAPTURED, CANCELLED, AUTHENTICATION_NOT_NEEDED (a wrong value lists the accepted set)")
	fl.StringVar(&from, "from", "", "From date (YYYY-MM-DD)")
	fl.StringVar(&to, "to", "", "To date (YYYY-MM-DD)")
	fl.StringVar(&customer, "customer", "", "Filter by customer id")
	fl.StringVar(&q, "q", "", "Search by order id or invoice number")
	fl.IntVar(&page, "page", 0, "Page number")
	fl.IntVar(&perPage, "per-page", 0, "Rows per page (max 100)")
	return cmd
}

func runOrderList(ctx context.Context, f *cmdutil.Factory, query url.Values) error {

	client, err := f.NewPGClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/order", query)
	if err != nil {
		return err
	}
	orders, meta, err := ipg.ListInto[order](resp)
	if err != nil {
		return err
	}
	maskCards(orders)

	return f.RenderOutput(orders, func() error {
		if len(orders) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No orders found.")
			return nil
		}
		t := jsonfmt.NewTable("INVOICE", "ORDER ID", "STATUS", "AMOUNT", "CREATED")
		for _, o := range orders {
			t.AddRow(o.OrderInvoiceNumber, o.OrderID, o.OrderStatus, o.OrderAmount, o.CreatedAt)
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		if meta != nil && meta.Total > 0 {
			fmt.Fprintf(f.IOStreams.ErrOut, "page %d/%d · %d of %d\n",
				meta.CurrentPage, meta.PageCount, len(orders), meta.Total)
		}
		return nil
	})
}

func newOrderGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get [invoice-number]",
		Short: "Show an order and its transactions",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			invoice, err := pickOrder(cmd.Context(), f, argOrEmpty(args), "Select an order")
			if err != nil {
				return err
			}
			return runOrderGet(cmd.Context(), f, invoice)
		},
	}
}

func runOrderGet(ctx context.Context, f *cmdutil.Factory, invoice string) error {
	invoiceSeg, err := cmdutil.PathSegment("invoice", invoice)
	if err != nil {
		return err
	}

	client, err := f.NewPGClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/order/detail/"+invoiceSeg, nil)
	if err != nil {
		return err
	}
	o, err := ipg.DataInto[order](resp)
	if err != nil {
		return err
	}
	maskCards([]order{o})

	return f.RenderOutput(o, func() error {
		f.Printf("Invoice:  %s\nOrder ID: %s\nStatus:   %s\nAmount:   %s %s\n",
			o.OrderInvoiceNumber, o.OrderID, o.OrderStatus, o.OrderAmount, o.OrderCurrency)
		if len(o.Transactions) == 0 {
			return nil
		}
		fmt.Fprintln(f.IOStreams.Out)
		t := jsonfmt.NewTable("METHOD", "TYPE", "AMOUNT", "STATUS", "CARD", "DATE")
		for _, tx := range o.Transactions {
			t.AddRow(tx.PaymentMethod, tx.TransactionType, tx.TransactionAmount, tx.TransactionStatus, tx.CardNumber, tx.TransactionDate)
		}
		return f.RenderTable(t)
	})
}

func newOrderCancelCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:   "cancel [invoice-number]",
		Short: "Cancel an order (bank-transfer, not yet captured)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOrderCancel(cmd.Context(), f, argOrEmpty(args))
		},
	}, map[string]string{
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnGuidance:    "Rejected unless the status is neither CAPTURED nor CANCELLED. Read `sepay pg order get` first.",
		cmdutil.AnnConfirms:    "always",
	})
}

func runOrderCancel(ctx context.Context, f *cmdutil.Factory, invoice string) error {
	invoice, err := pickOrder(ctx, f, invoice, "Select an order to cancel")
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation("cancel order "+invoice, true); err != nil {
		return err
	}
	client, err := f.NewPGClient()
	if err != nil {
		return err
	}
	resp, err := client.Post(ctx, "/v1/order/cancel", map[string]string{"order_invoice_number": invoice})
	if err != nil {
		return err
	}
	return renderAction(f, invoice, resp, "Order cancelled.")
}

func newOrderVoidCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:   "void [invoice-number]",
		Short: "Void a captured card transaction (before settlement)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOrderVoid(cmd.Context(), f, argOrEmpty(args))
		},
	}, map[string]string{
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnGuidance:    "CAPTURED card orders only, and only while the payment is still unsettled. Once settled, a refund is the only route.",
		cmdutil.AnnConfirms:    "always",
	})
}

func runOrderVoid(ctx context.Context, f *cmdutil.Factory, invoice string) error {
	invoice, err := pickOrder(ctx, f, invoice, "Select an order to void")
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation("void the transaction for order "+invoice, true); err != nil {
		return err
	}
	client, err := f.NewPGClient()
	if err != nil {
		return err
	}
	resp, err := client.Post(ctx, "/v1/order/voidTransaction", map[string]string{"order_invoice_number": invoice})
	if err != nil {
		return err
	}
	return renderAction(f, invoice, resp, "Transaction voided.")
}

func argOrEmpty(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

const pickerMaxPages = 20

func fetchAllOrders(ctx context.Context, client *ipg.Client) ([]order, error) {
	var all []order
	for page := 1; page <= pickerMaxPages; page++ {
		resp, err := client.Get(ctx, "/v1/order", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		orders, meta, err := ipg.ListInto[order](resp)
		if err != nil {
			return nil, err
		}
		all = append(all, orders...)
		if meta == nil || !meta.HasMore {
			break
		}
	}
	return all, nil
}

// pickOrder returns val when set, else prompts on a TTY and returns the invoice number.
func pickOrder(ctx context.Context, f *cmdutil.Factory, val, label string) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.CanPrompt() {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("an invoice number is required")}
	}
	client, err := f.NewPGClient()
	if err != nil {
		return "", err
	}
	orders, err := fetchAllOrders(ctx, client)
	if err != nil {
		return "", err
	}
	if len(orders) == 0 {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("no orders found")}
	}
	choices := make([]cmdutil.PromptChoice, len(orders))
	for i, o := range orders {
		choices[i] = cmdutil.PromptChoice{
			Label: fmt.Sprintf("%s · %s · %s %s", o.OrderInvoiceNumber, o.OrderStatus, o.OrderAmount, o.OrderCurrency),
			Value: o.OrderInvoiceNumber,
		}
	}
	return f.SelectChoiceOrFlag("", "", label, choices)
}

// actionResult is cancel/void output; AgreementID is set only on a 207 partial void.
type actionResult struct {
	InvoiceNumber string `json:"invoice_number" yaml:"invoice_number"`
	Message       string `json:"message" yaml:"message"`
	AgreementID   string `json:"agreement_id,omitempty" yaml:"agreement_id,omitempty"`
}

// renderAction prints a cancel/void outcome, warning on a 207 partial void.
func renderAction(f *cmdutil.Factory, invoice string, resp *ipg.Response, msg string) error {
	var partial struct {
		AgreementID string `json:"agreement_id"`
	}
	_ = json.Unmarshal(resp.Raw, &partial)

	out := actionResult{InvoiceNumber: invoice, Message: msg, AgreementID: partial.AgreementID}

	return f.RenderOutput(out, func() error {
		fmt.Fprintln(f.IOStreams.Out, f.SuccessIcon()+" "+msg)
		if partial.AgreementID != "" {
			f.ErrPrintf(
				"! auto-renew subscription %s was not cancelled; cancel it via the agreement API\n",
				partial.AgreementID)
		}
		return nil
	})
}
