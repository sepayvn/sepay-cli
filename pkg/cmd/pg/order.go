package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	ipg "github.com/sepayvn/sepay-cli/internal/pg"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// order is a PG order, one shape on both envs: amounts are whole VND, null when the server has none.
type order struct {
	ID                   string        `json:"id" yaml:"id"`
	CustomerID           *string       `json:"customer_id" yaml:"customer_id"`
	OrderID              string        `json:"order_id" yaml:"order_id"`
	OrderInvoiceNumber   string        `json:"order_invoice_number" yaml:"order_invoice_number"`
	OrderStatus          string        `json:"order_status" yaml:"order_status"`
	OrderAmount          *int64        `json:"order_amount" yaml:"order_amount"`
	OrderCurrency        string        `json:"order_currency" yaml:"order_currency"`
	OrderDescription     string        `json:"order_description" yaml:"order_description"`
	AuthenticationStatus *string       `json:"authentication_status" yaml:"authentication_status"`
	CreatedAt            string        `json:"created_at" yaml:"created_at"`
	UpdatedAt            string        `json:"updated_at" yaml:"updated_at"`
	Transactions         []transaction `json:"transactions,omitempty" yaml:"transactions,omitempty"`
}

// transaction is one card/bank transaction under an order; card is set only for a card payment.
type transaction struct {
	ID                         string  `json:"id" yaml:"id"`
	PaymentMethod              *string `json:"payment_method" yaml:"payment_method"`
	TransactionType            string  `json:"transaction_type" yaml:"transaction_type"`
	TransactionAmount          *int64  `json:"transaction_amount" yaml:"transaction_amount"`
	TransactionCurrency        string  `json:"transaction_currency" yaml:"transaction_currency"`
	TransactionStatus          string  `json:"transaction_status" yaml:"transaction_status"`
	AuthenticationStatus       *string `json:"authentication_status" yaml:"authentication_status"`
	Card                       *card   `json:"card,omitempty" yaml:"card,omitempty"`
	TransactionDate            string  `json:"transaction_date" yaml:"transaction_date"`
	TransactionLastUpdatedDate string  `json:"transaction_last_updated_date" yaml:"transaction_last_updated_date"`
}

type card struct {
	Brand string `json:"brand" yaml:"brand"`
	Last4 string `json:"last4" yaml:"last4"`
}

// wireOrder decodes an order from either source. API v2 sends integer amounts and card; the sandbox
// gateway, read by the cancel and void pickers, sends decimal strings and the card number, which
// never leaves the decoder.
type wireOrder struct {
	ID                   string            `json:"id"`
	CustomerID           *string           `json:"customer_id"`
	OrderID              string            `json:"order_id"`
	OrderInvoiceNumber   string            `json:"order_invoice_number"`
	OrderStatus          string            `json:"order_status"`
	OrderAmount          vnd               `json:"order_amount"`
	OrderCurrency        string            `json:"order_currency"`
	OrderDescription     string            `json:"order_description"`
	AuthenticationStatus *string           `json:"authentication_status"`
	CreatedAt            string            `json:"created_at"`
	UpdatedAt            string            `json:"updated_at"`
	Transactions         []wireTransaction `json:"transactions"`
}

type wireTransaction struct {
	ID                         string  `json:"id"`
	PaymentMethod              *string `json:"payment_method"`
	TransactionType            string  `json:"transaction_type"`
	TransactionAmount          vnd     `json:"transaction_amount"`
	TransactionCurrency        string  `json:"transaction_currency"`
	TransactionStatus          string  `json:"transaction_status"`
	AuthenticationStatus       *string `json:"authentication_status"`
	Card                       *card   `json:"card"`
	CardNumber                 string  `json:"card_number"`
	CardBrand                  string  `json:"card_brand"`
	TransactionDate            string  `json:"transaction_date"`
	TransactionLastUpdatedDate string  `json:"transaction_last_updated_date"`
}

// vnd reads an amount sent as a JSON number or a decimal string ("150000.00") and rounds it to
// whole VND, which has no minor unit. A null or empty amount stays unset.
type vnd struct {
	n   int64
	set bool
}

// vndSyntax is a plain decimal of at most 20 integer and 20 fractional digits, optionally with a
// short exponent. big.Rat parses in time quadratic in the digits, so the bounds keep a hostile amount
// from stalling the read; 20 digits hold every int64 and the gateway's decimal(20,2).
var vndSyntax = regexp.MustCompile(`^-?\d{1,20}(\.\d{1,20})?([eE][+-]?\d{1,3})?$`)

func (v *vnd) UnmarshalJSON(b []byte) error {
	raw := strings.Trim(string(b), `"`)
	if raw == "" || raw == "null" {
		*v = vnd{}
		return nil
	}
	n, ok := roundVND(raw)
	if !ok {
		return fmt.Errorf("amount %q is not a VND amount", raw)
	}
	*v = vnd{n: n, set: true}
	return nil
}

// roundVND rounds half away from zero in exact arithmetic: the sandbox gateway returns amounts up to
// 999999999999999999.99, past the 2^53 where a float64 stops holding every whole number. The
// magnitude must fit an int64, so math.MinInt64, which has no positive counterpart, is refused too.
func roundVND(raw string) (int64, bool) {
	if !vndSyntax.MatchString(raw) {
		return 0, false
	}
	r, ok := new(big.Rat).SetString(raw)
	if !ok {
		return 0, false
	}
	q, rem := new(big.Int).QuoRem(new(big.Int).Abs(r.Num()), r.Denom(), new(big.Int))
	if rem.Lsh(rem, 1).Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, false
	}
	if r.Sign() < 0 {
		return -q.Int64(), true
	}
	return q.Int64(), true
}

func (v vnd) ptr() *int64 {
	if !v.set {
		return nil
	}
	n := v.n
	return &n
}

// formatAmount prints whole VND with thousands dots, or "-" when the server sent none.
func formatAmount(n *int64) string {
	if n == nil {
		return "-"
	}
	return jsonfmt.FormatVND(*n)
}

func (w wireOrder) toOrder() order {
	o := order{
		ID: w.ID, CustomerID: w.CustomerID, OrderID: w.OrderID, OrderInvoiceNumber: w.OrderInvoiceNumber,
		OrderStatus: w.OrderStatus, OrderAmount: w.OrderAmount.ptr(), OrderCurrency: w.OrderCurrency,
		OrderDescription: w.OrderDescription, AuthenticationStatus: w.AuthenticationStatus,
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
	for _, t := range w.Transactions {
		o.Transactions = append(o.Transactions, transaction{
			ID: t.ID, PaymentMethod: t.PaymentMethod, TransactionType: t.TransactionType,
			TransactionAmount: t.TransactionAmount.ptr(), TransactionCurrency: t.TransactionCurrency,
			TransactionStatus: t.TransactionStatus, AuthenticationStatus: t.AuthenticationStatus,
			Card: t.card(), TransactionDate: t.TransactionDate, TransactionLastUpdatedDate: t.TransactionLastUpdatedDate,
		})
	}
	return o
}

// card keeps only the brand and last 4 digits, and only when those are 4 digits: the server's card
// object from API v2, else the tail of the gateway's card number (bare or masked alike).
func (t wireTransaction) card() *card {
	if t.Card != nil {
		if !isLast4(t.Card.Last4) {
			return nil
		}
		return &card{Brand: t.Card.Brand, Last4: t.Card.Last4}
	}
	number := strings.TrimSpace(t.CardNumber)
	if len(number) < 4 || !isLast4(number[len(number)-4:]) {
		return nil
	}
	return &card{Brand: t.CardBrand, Last4: number[len(number)-4:]}
}

func isLast4(s string) bool {
	if len(s) != 4 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// String prints the card as "<brand> ****<last4>", or "-" when the transaction is not a card payment.
func (c *card) String() string {
	if c == nil {
		return "-"
	}
	return strings.TrimSpace(c.Brand + " ****" + c.Last4)
}

// newOrderCmd also returns the two commands that read production, for the pg env hook.
func newOrderCmd(f *cmdutil.Factory) (*cobra.Command, map[*cobra.Command]bool) {
	cmd := &cobra.Command{
		Use:   "order <command>",
		Short: "Payment Gateway orders",
	}
	list, get := newOrderListCmd(f), newOrderGetCmd(f)
	cmd.AddCommand(list, get, newOrderCancelCmd(f), newOrderVoidCmd(f))
	return cmd, map[*cobra.Command]bool{list: true, get: true}
}

// orderScope is the scope production requires of order list and get on top of "read"; the sandbox
// host does not check it. Help, --map and the exit-5 text name it.
const orderScope = "pg:read"

// orderPermission names the permission that grants orderScope, as the user permission page of
// my.sepay.vn shows it (group › box), so an admin can find the box to tick.
const orderPermission = "Cổng thanh toán › Xem danh sách đơn hàng"

// orderReadAnnotations mark the order reads as production-capable; the group's annotations
// describe the sandbox-only commands.
var orderReadAnnotations = map[string]string{
	cmdutil.AnnEnvs:     "both",
	cmdutil.AnnScope:    orderScope,
	cmdutil.AnnGuidance: "A gateway order notifies over IPN, so `sepay webhook listen` never carries one — use `sepay pg ipn listen`.",
}

// orderStatuses is App\Enums\PgOrderStatus; the filter is a case-sensitive match.
var orderStatuses = []string{
	"AUTHENTICATED", "AUTHENTICATION_INITIATED", "AUTHENTICATION_NOT_NEEDED",
	"AUTHENTICATION_UNSUCCESSFUL", "AUTHORIZED", "CANCELLED", "CAPTURED",
	"CHARGEBACK_PROCESSED", "DISBURSED", "DISPUTED", "EXCESSIVELY_REFUNDED",
	"FAILED", "FUNDING", "INITIATED", "PARTIALLY_CAPTURED", "PARTIALLY_REFUNDED",
	"REFUNDED", "REFUND_REQUESTED", "VERIFIED",
}

// orderStatusColor paints an order status by where the money stands: green once paid, yellow
// while it waits, red when it failed or was cancelled or disputed, dim once refunded or for a
// card check. An unknown status stays plain.
func orderStatusColor(ios *iostreams.IOStreams, status string) string {
	switch status {
	case "CAPTURED", "AUTHORIZED", "PARTIALLY_CAPTURED", "DISBURSED", "AUTHENTICATED":
		return ios.Green(status)
	case "AUTHENTICATION_NOT_NEEDED", "INITIATED", "AUTHENTICATION_INITIATED", "FUNDING", "REFUND_REQUESTED":
		return ios.Yellow(status)
	case "FAILED", "CANCELLED", "AUTHENTICATION_UNSUCCESSFUL", "DISPUTED", "CHARGEBACK_PROCESSED":
		return ios.Red(status)
	case "REFUNDED", "PARTIALLY_REFUNDED", "EXCESSIVELY_REFUNDED", "VERIFIED":
		return ios.Dim(status)
	}
	return status
}

// transactionStatusColor paints a gateway transaction status (App\Enums\PgTransactionStatus):
// green when approved, yellow while pending, red when declined or failed. Balance checks and
// unknown values stay plain.
func transactionStatusColor(ios *iostreams.IOStreams, status string) string {
	switch status {
	case "APPROVED", "APPROVED_AUTO", "APPROVED_PENDING_SETTLEMENT", "PARTIALLY_APPROVED":
		return ios.Green(status)
	case "PENDING", "SUBMITTED", "AUTHENTICATION_IN_PROGRESS", "DEFERRED_TRANSACTION_RECEIVED", "REFERRED":
		return ios.Yellow(status)
	case "ABORTED", "ACQUIRER_SYSTEM_ERROR", "AUTHENTICATION_FAILED", "BLOCKED", "CANCELLED",
		"DUPLICATE_BATCH", "EXCEEDED_RETRY_LIMIT", "EXPIRED_CARD", "INSUFFICIENT_FUNDS", "INVALID_CSC",
		"LOCK_FAILURE", "NOT_SUPPORTED", "NO_BALANCE", "SYSTEM_ERROR", "TIMED_OUT", "UNSPECIFIED_FAILURE":
		return ios.Red(status)
	}
	if strings.HasPrefix(status, "DECLINED") {
		return ios.Red(status)
	}
	return status
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func newOrderListCmd(f *cmdutil.Factory) *cobra.Command {
	var status, from, to, customer, q string
	var page, perPage int
	cmd := cmdutil.Annotate(&cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List orders",
		Long: `List Payment Gateway orders. Filter by status, customer or creation date (--from,
--to), or search by pay code or order code.

The ORDER CODE column is the <invoice-number> "pg order get" takes; on sandbox, "cancel" and "void"
take it for an order of the sandbox merchant.`,
		Example: `  sepay pg order list --status CAPTURED --from 2026-07-01
  sepay pg order list --search DH1001`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.ValidateChoice(cmd, "status", orderStatuses); err != nil {
				return err
			}
			if err := cmdutil.ValidateDays(cmd, "from", "to"); err != nil {
				return err
			}
			if err := cmdutil.ValidateDateOrder(cmd, "from", "to"); err != nil {
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
	}, orderReadAnnotations)
	fl := cmd.Flags()
	fl.StringVar(&status, "status", "", "Filter by order status, e.g. CAPTURED, CANCELLED, AUTHENTICATION_NOT_NEEDED (a wrong value lists the accepted set)")
	fl.StringVar(&from, "from", "", "From date (YYYY-MM-DD)")
	fl.StringVar(&to, "to", "", "To date (YYYY-MM-DD)")
	fl.StringVar(&customer, "customer", "", "Filter by customer id")
	fl.StringVar(&q, "search", "", "Search by pay code or order code")
	cmdutil.AliasFlag(fl, "q", "search")
	fl.IntVar(&page, "page", 0, "Page number")
	fl.IntVar(&perPage, "per-page", 0, "Rows per page (max 100)")
	return cmd
}

// pageInfo is the paging both envs report, for the stderr footer and the picker's page loop.
type pageInfo struct {
	total, current, pages int
	hasMore               bool
}

// listOrders reads one page of the company's orders, every merchant, through API v2 with the user's
// token on both envs.
func listOrders(ctx context.Context, f *cmdutil.Factory, query url.Values) ([]order, *pageInfo, error) {
	client, err := f.NewAPIClient()
	if err != nil {
		return nil, nil, err
	}
	env, err := client.Get(ctx, "/pg/orders", query)
	if err != nil {
		return nil, nil, cmdutil.ReadScopeError(err, orderScope, orderPermission)
	}
	wire, p, err := api.ListInto[wireOrder](env)
	if err != nil {
		return nil, nil, err
	}
	var info *pageInfo
	if p != nil {
		info = &pageInfo{total: p.Total, current: p.CurrentPage, pages: p.LastPage, hasMore: p.HasMore}
	}
	return toOrders(wire), info, nil
}

// listGatewayOrders reads one page from the sandbox gateway with the brokered merchant credential:
// the orders cancel and void can act on.
func listGatewayOrders(ctx context.Context, f *cmdutil.Factory, query url.Values) ([]order, *pageInfo, error) {
	client, err := f.NewPGClient()
	if err != nil {
		return nil, nil, err
	}
	resp, err := client.Get(ctx, "/v1/order", query)
	if err != nil {
		return nil, nil, err
	}
	wire, meta, err := ipg.ListInto[wireOrder](resp)
	if err != nil {
		return nil, nil, err
	}
	var info *pageInfo
	if meta != nil {
		info = &pageInfo{total: meta.Total, current: meta.CurrentPage, pages: meta.PageCount, hasMore: meta.HasMore}
	}
	return toOrders(wire), info, nil
}

func toOrders(wire []wireOrder) []order {
	orders := make([]order, len(wire))
	for i, w := range wire {
		orders[i] = w.toOrder()
	}
	return orders
}

func runOrderList(ctx context.Context, f *cmdutil.Factory, query url.Values) error {
	orders, page, err := listOrders(ctx, f, query)
	if err != nil {
		return err
	}

	return f.RenderOutput(orders, func() error {
		if len(orders) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No orders found.")
			return nil
		}
		t := jsonfmt.NewTable("ORDER CODE", "PAY CODE", "STATUS", "AMOUNT", "CUSTOMER", "CREATED").Droppable("CUSTOMER", "CREATED")
		for _, o := range orders {
			t.AddRow(o.OrderInvoiceNumber, o.OrderID, orderStatusColor(f.IOStreams, o.OrderStatus),
				formatAmount(o.OrderAmount), jsonfmt.OrDash(deref(o.CustomerID)), o.CreatedAt)
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		if page != nil && page.total > 0 {
			fmt.Fprintf(f.IOStreams.ErrOut, "page %d/%d · %d of %d\n", page.current, page.pages, len(orders), page.total)
		}
		return nil
	})
}

func newOrderGetCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:   "get <invoice-number>",
		Short: "Show an order and its transactions",
		Long: `Show an order and its transactions.

<invoice-number> is the ORDER CODE column of "sepay pg order list". Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			invoice, err := pickOrder(cmd.Context(), f, argOrEmpty(args), "Select an order", listOrders)
			if err != nil {
				return err
			}
			return runOrderGet(cmd.Context(), f, invoice)
		},
	}, orderReadAnnotations)
}

func runOrderGet(ctx context.Context, f *cmdutil.Factory, invoice string) error {
	invoiceSeg, err := cmdutil.PathSegment("invoice", invoice)
	if err != nil {
		return err
	}
	// The server's router urldecodes the path, which reads a bare "+" as a space.
	o, err := getOrder(ctx, f, strings.ReplaceAll(invoiceSeg, "+", "%2B"))
	if err != nil {
		return err
	}

	return f.RenderOutput(o, func() error {
		f.Printf("Order code:   %s\nPay code:     %s\nStatus:       %s\nAmount:       %s %s\n",
			o.OrderInvoiceNumber, o.OrderID, orderStatusColor(f.IOStreams, o.OrderStatus), formatAmount(o.OrderAmount), o.OrderCurrency)
		for _, line := range [][2]string{
			{"Customer:", deref(o.CustomerID)},
			{"Description:", o.OrderDescription},
			{"3-D Secure:", deref(o.AuthenticationStatus)},
			{"Created:", o.CreatedAt},
			{"Updated:", o.UpdatedAt},
		} {
			if line[1] != "" {
				f.Printf("%-13s %s\n", line[0], line[1])
			}
		}
		if len(o.Transactions) == 0 {
			return nil
		}
		fmt.Fprintln(f.IOStreams.Out)
		t := jsonfmt.NewTable("METHOD", "TYPE", "AMOUNT", "STATUS", "CARD", "DATE")
		for _, tx := range o.Transactions {
			t.AddRow(deref(tx.PaymentMethod), tx.TransactionType, formatAmount(tx.TransactionAmount),
				transactionStatusColor(f.IOStreams, tx.TransactionStatus), tx.Card.String(), tx.TransactionDate)
		}
		return f.RenderTable(t)
	})
}

func getOrder(ctx context.Context, f *cmdutil.Factory, invoiceSeg string) (order, error) {
	client, err := f.NewAPIClient()
	if err != nil {
		return order{}, err
	}
	env, err := client.Get(ctx, "/pg/orders/"+invoiceSeg, nil)
	if err != nil {
		return order{}, cmdutil.ReadScopeError(err, orderScope, orderPermission)
	}
	w, err := api.DataInto[wireOrder](env)
	return w.toOrder(), err
}

func newOrderCancelCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:   "cancel <invoice-number>",
		Short: "Cancel an order (bank-transfer, not yet captured)",
		Long: `Cancel a bank-transfer order that has not been captured.

<invoice-number> is the ORDER CODE column of "sepay pg order list" for an order of the sandbox merchant. Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
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
	invoice, err := pickOrder(ctx, f, invoice, "Select an order to cancel", listGatewayOrders)
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation("cancel order "+invoice, invoice, true); err != nil {
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
		Use:   "void <invoice-number>",
		Short: "Void a captured card transaction (before settlement)",
		Long: `Void a captured card payment before it settles.

<invoice-number> is the ORDER CODE column of "sepay pg order list" for an order of the sandbox merchant. Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
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
	invoice, err := pickOrder(ctx, f, invoice, "Select an order to void", listGatewayOrders)
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation("void the transaction for order "+invoice, invoice, true); err != nil {
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

// orderLister reads one page of orders for a picker: listOrders for get, listGatewayOrders for the
// sandbox writes.
type orderLister func(context.Context, *cmdutil.Factory, url.Values) ([]order, *pageInfo, error)

func fetchAllOrders(ctx context.Context, f *cmdutil.Factory, list orderLister) ([]order, error) {
	var all []order
	for page := 1; page <= pickerMaxPages; page++ {
		orders, info, err := list(ctx, f, url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		all = append(all, orders...)
		if info == nil || !info.hasMore {
			break
		}
	}
	return all, nil
}

// pickOrder returns val when set, else prompts on a TTY and returns the invoice number.
func pickOrder(ctx context.Context, f *cmdutil.Factory, val, label string, list orderLister) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.CanPrompt() {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("an invoice number is required")}
	}
	orders, err := fetchAllOrders(ctx, f, list)
	if err != nil {
		return "", err
	}
	if len(orders) == 0 {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("no orders found")}
	}
	// Rows come newest first and only the invoice number is sent. get resolves a shared number to
	// its newest order, so each number is offered once, as that order; an older one is
	// found with list --search. cancel and void send the same number and the sandbox gateway picks
	// which order of it to change, so the row shown is not a promise for them.
	seen := make(map[string]bool, len(orders))
	var choices []cmdutil.PromptChoice
	for _, o := range orders {
		if seen[o.OrderInvoiceNumber] {
			continue
		}
		seen[o.OrderInvoiceNumber] = true
		choices = append(choices, cmdutil.PromptChoice{
			Label: fmt.Sprintf("%s · %s · %s %s", o.OrderInvoiceNumber, o.OrderStatus, formatAmount(o.OrderAmount), o.OrderCurrency),
			Value: o.OrderInvoiceNumber,
		})
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
