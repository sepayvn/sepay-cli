package pg

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sepayvn/sepay-cli/internal/pg"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

type checkoutOptions struct {
	amount        string
	description   string
	paymentMethod string
	invoice       string
	operation     string
	currency      string
	successURL    string
	errorURL      string
	cancelURL     string
	customer      string
	noOpen        bool
}

// checkoutMethods are the only payment methods the server can serve a checkout with.
var checkoutMethods = []string{"BANK_TRANSFER", "CARD", "NAPAS_BANK_TRANSFER"}

// preflightTimeout bounds the fail-open pre-flight so a rate-limited read cannot stall the checkout.
const preflightTimeout = 3 * time.Second

// preflightInfo is the GET /v1/merchant subset the pre-flight reads; nil PaymentMethods means the key was absent.
type preflightInfo struct {
	PaymentMethods *[]paymentMethod `json:"payment_methods"`
}

// preflightPaymentMethods refuses a checkout the merchant cannot serve, since the server's own failure is a reasonless HTML page.
// It fails open on any error except cancellation.
func preflightPaymentMethods(ctx context.Context, client *pg.Client, want string) error {
	probeCtx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()

	resp, err := client.Get(probeCtx, "/v1/merchant", nil)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return nil
	}
	var info preflightInfo
	if err := resp.Into(&info); err != nil || info.PaymentMethods == nil {
		return nil
	}

	// Active rows outside checkoutMethods still cannot carry a checkout.
	var usable []string
	for _, pm := range *info.PaymentMethods {
		if int(pm.Active) == 1 && slices.Contains(checkoutMethods, pm.PaymentMethod) {
			usable = append(usable, pm.PaymentMethod)
		}
	}
	if len(usable) == 0 {
		return errors.New("this sandbox merchant has no active payment method, so checkout would fail " +
			"with an error page that carries no reason\n" +
			"enable one in the SePay portal under Payment Gateway \u2192 Payment methods, then check with: sepay pg merchant show")
	}
	// Not a FlagError: the value is valid, the merchant just cannot serve it.
	if want != "" && !slices.Contains(usable, want) {
		return fmt.Errorf("--payment-method %s is not active on this sandbox merchant (active: %s)",
			want, strings.Join(usable, ", "))
	}
	return nil
}

func newCheckoutCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "checkout <command>",
		Short: "Create a hosted checkout session",
	}
	cmd.AddCommand(newCheckoutInitCmd(f))
	return cmd
}

func newCheckoutInitCmd(f *cmdutil.Factory) *cobra.Command {
	opts := &checkoutOptions{operation: "PURCHASE", currency: "VND"}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Start a checkout and return its payment page URL",
		Args:  cmdutil.NoArgs,
		Long: "POST a signed checkout/init and return the hosted payment page URL.\n" +
			"On a TTY the URL opens in your browser; otherwise it is printed. Payment\n" +
			"itself completes on the hosted page in a browser — the CLI only hands off the URL.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheckoutInit(cmd.Context(), f, opts)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&opts.amount, "amount", "", "Order amount (required)")
	fl.StringVar(&opts.description, "description", "", "Order description (required)")
	fl.StringVar(&opts.paymentMethod, "payment-method", "", "BANK_TRANSFER|CARD|NAPAS_BANK_TRANSFER (optional)")
	fl.StringVar(&opts.invoice, "invoice", "", "Order invoice number: letters, digits, - and _ (required for PURCHASE)")
	fl.StringVar(&opts.operation, "operation", "PURCHASE", "PURCHASE|VERIFY")
	fl.StringVar(&opts.currency, "currency", "VND", "Currency")
	fl.StringVar(&opts.successURL, "success-url", "", "Redirect URL on success")
	fl.StringVar(&opts.errorURL, "error-url", "", "Redirect URL on error")
	fl.StringVar(&opts.cancelURL, "cancel-url", "", "Redirect URL on cancel")
	fl.StringVar(&opts.customer, "customer", "", "Customer id")
	fl.BoolVar(&opts.noOpen, "no-open", false, "Print the URL without opening a browser")
	return cmdutil.MarkRequired(cmd, "amount", "description")
}

func runCheckoutInit(ctx context.Context, f *cmdutil.Factory, opts *checkoutOptions) error {
	// The server rejects a bad checkout with an HTML page and no reason, so validate up front.
	if err := validateCheckoutFlags(opts); err != nil {
		return err
	}

	client, err := f.NewPGClient()
	if err != nil {
		return err
	}
	if err := preflightPaymentMethods(ctx, client, opts.paymentMethod); err != nil {
		return err
	}

	if opts.amount, err = f.InputOrFlag(opts.amount, "amount", "Order amount (VND)"); err != nil {
		return err
	}
	if err := validateCheckoutAmount(opts); err != nil {
		return err
	}
	if opts.description, err = f.InputOrFlag(opts.description, "description", "Order description"); err != nil {
		return err
	}
	if opts.operation == "PURCHASE" {
		if opts.invoice, err = f.InputOrFlag(opts.invoice, "invoice", "Order invoice number"); err != nil {
			return err
		}
	}
	if err := validateCheckoutInvoice(opts); err != nil {
		return err
	}

	loc, err := client.InitCheckout(ctx, pg.CheckoutParams{
		Operation:     opts.operation,
		PaymentMethod: opts.paymentMethod,
		OrderAmount:   opts.amount,
		Currency:      opts.currency,
		InvoiceNumber: opts.invoice,
		Description:   opts.description,
		CustomerID:    opts.customer,
		SuccessURL:    opts.successURL,
		ErrorURL:      opts.errorURL,
		CancelURL:     opts.cancelURL,
	})
	if err != nil {
		return err
	}

	out := struct {
		CheckoutURL string `json:"checkout_url" yaml:"checkout_url"`
	}{CheckoutURL: loc}

	return f.RenderOutput(out, func() error {
		ios := f.IOStreams
		// The Location is server-supplied: open a browser only for a trusted SePay origin, else print it.
		if ios.IsStdoutTTY() && !opts.noOpen {
			originErr := cmdutil.ValidateCredentialOrigin(loc)
			if originErr == nil {
				originErr = cmdutil.AssertCheckoutHost(loc, "Location")
			}
			if originErr != nil {
				fmt.Fprintf(ios.ErrOut, "! not opening a browser: %v\n", originErr)
			} else if cmdutil.OpenBrowser(loc) {
				fmt.Fprintln(ios.ErrOut, "opened checkout in your browser")
			}
		}
		fmt.Fprintln(ios.Out, jsonfmt.Clean(loc))
		return nil
	})
}

func validateCheckoutFlags(opts *checkoutOptions) error {
	if !slices.Contains([]string{"PURCHASE", "VERIFY"}, opts.operation) {
		return &cmdutil.FlagError{Err: fmt.Errorf("--operation must be PURCHASE or VERIFY")}
	}
	if opts.paymentMethod != "" && !slices.Contains(checkoutMethods, opts.paymentMethod) {
		return &cmdutil.FlagError{Err: fmt.Errorf("--payment-method must be one of: %s", strings.Join(checkoutMethods, ", "))}
	}
	if opts.currency != "VND" {
		return &cmdutil.FlagError{Err: fmt.Errorf("--currency must be VND")}
	}
	for _, u := range []struct{ flag, value string }{
		{"success-url", opts.successURL},
		{"error-url", opts.errorURL},
		{"cancel-url", opts.cancelURL},
	} {
		if err := validateRedirectURL(u.flag, u.value); err != nil {
			return err
		}
	}
	return nil
}

// invoiceNumberPattern is the server's order_invoice_number rule (no "/").
var invoiceNumberPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func validateCheckoutInvoice(opts *checkoutOptions) error {
	if opts.invoice == "" {
		return nil
	}
	if len(opts.invoice) > 100 {
		return &cmdutil.FlagError{Err: fmt.Errorf("--invoice must be at most 100 characters")}
	}
	if !invoiceNumberPattern.MatchString(opts.invoice) {
		return &cmdutil.FlagError{Err: fmt.Errorf("--invoice may contain only letters, digits, %q and %q, got %q", "-", "_", opts.invoice)}
	}
	return nil
}

// validateRedirectURL mirrors the server's valid_redirect_url scheme check.
func validateRedirectURL(flag, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 2048 || strings.ContainsAny(value, " \t\r\n") {
		return &cmdutil.FlagError{Err: fmt.Errorf("--%s must be at most 2048 characters and contain no whitespace", flag)}
	}
	scheme, _, found := strings.Cut(value, ":")
	if !found || scheme == "" {
		return &cmdutil.FlagError{Err: fmt.Errorf("--%s must include a scheme, e.g. https://example.com/done, got %q", flag, value)}
	}
	blocked := []string{"javascript", "data", "vbscript", "file", "blob", "about"}
	if slices.Contains(blocked, strings.ToLower(scheme)) {
		return &cmdutil.FlagError{Err: fmt.Errorf("--%s scheme %q is not allowed", flag, scheme)}
	}
	return nil
}

// validateCheckoutAmount requires whole VND, non-zero for PURCHASE and zero for VERIFY.
func validateCheckoutAmount(opts *checkoutOptions) error {
	amount, err := strconv.ParseInt(opts.amount, 10, 64)
	if err != nil || amount < 0 {
		return &cmdutil.FlagError{Err: fmt.Errorf("--amount must be a whole number of VND, got %q", opts.amount)}
	}
	if opts.operation == "PURCHASE" && amount == 0 {
		return &cmdutil.FlagError{Err: fmt.Errorf("--amount must be greater than 0 for a PURCHASE")}
	}
	if opts.operation == "VERIFY" && amount != 0 {
		return &cmdutil.FlagError{Err: fmt.Errorf("--amount must be 0 for a VERIFY (it authorizes nothing)")}
	}
	return nil
}
