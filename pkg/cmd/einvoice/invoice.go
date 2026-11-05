package einvoice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	iei "github.com/sepayvn/sepay-cli/internal/einvoice"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// invoice is an E-Invoice document; only the detail carries Items.
type invoice struct {
	ReferenceCode    string        `json:"reference_code" yaml:"reference_code"`
	InvoiceNumber    string        `json:"invoice_number" yaml:"invoice_number"`
	InvoiceSeries    string        `json:"invoice_series,omitempty" yaml:"invoice_series,omitempty"`
	DocumentType     int           `json:"document_type,omitempty" yaml:"document_type,omitempty"`
	IssuedDate       string        `json:"issued_date" yaml:"issued_date"`
	PdfURL           *string       `json:"pdf_url" yaml:"pdf_url"`
	XMLURL           *string       `json:"xml_url" yaml:"xml_url"`
	Status           string        `json:"status" yaml:"status"`
	TaxAuthorityCode *string       `json:"tax_authority_code,omitempty" yaml:"tax_authority_code,omitempty"`
	ProviderStatus   *string       `json:"provider_status,omitempty" yaml:"provider_status,omitempty"`
	PaymentMethod    string        `json:"payment_method,omitempty" yaml:"payment_method,omitempty"`
	Buyer            *buyer        `json:"buyer,omitempty" yaml:"buyer,omitempty"`
	TotalBeforeTax   api.FlexNum   `json:"total_before_tax" yaml:"total_before_tax"`
	TaxAmount        api.FlexNum   `json:"tax_amount" yaml:"tax_amount"`
	TotalAmount      api.FlexNum   `json:"total_amount" yaml:"total_amount"`
	Notes            string        `json:"notes,omitempty" yaml:"notes,omitempty"`
	Source           string        `json:"source,omitempty" yaml:"source,omitempty"`
	Items            []invoiceItem `json:"items,omitempty" yaml:"items,omitempty"`
}

type buyer struct {
	Type      string `json:"type,omitempty" yaml:"type,omitempty"`
	Name      string `json:"name" yaml:"name"`
	LegalName string `json:"legal_name,omitempty" yaml:"legal_name,omitempty"`
	TaxCode   string `json:"tax_code,omitempty" yaml:"tax_code,omitempty"`
	Address   string `json:"address,omitempty" yaml:"address,omitempty"`
	Email     string `json:"email,omitempty" yaml:"email,omitempty"`
	Phone     string `json:"phone,omitempty" yaml:"phone,omitempty"`
}

type invoiceItem struct {
	LineNumber  api.FlexInt `json:"line_number" yaml:"line_number"`
	LineType    api.FlexInt `json:"line_type" yaml:"line_type"`
	ItemCode    string      `json:"item_code,omitempty" yaml:"item_code,omitempty"`
	ItemName    string      `json:"item_name" yaml:"item_name"`
	Unit        string      `json:"unit,omitempty" yaml:"unit,omitempty"`
	Quantity    api.FlexNum `json:"quantity" yaml:"quantity"`
	UnitPrice   api.FlexNum `json:"unit_price" yaml:"unit_price"`
	TotalAmount api.FlexNum `json:"total_amount" yaml:"total_amount"`
	TaxRate     api.FlexNum `json:"tax_rate" yaml:"tax_rate"`
	TaxAmount   api.FlexNum `json:"tax_amount" yaml:"tax_amount"`
}

func newInvoiceCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invoice <command>",
		Short: "Invoices (create and issue are async: submit, then poll)",
	}
	cmd.AddCommand(
		newInvoiceListCmd(f),
		newInvoiceGetCmd(f),
		newInvoiceCreateCmd(f),
		newInvoiceCheckCmd(f, "create-status", "/v1/invoices/create/check/", "Poll a create submission to Success/Failed", "create"),
		newInvoiceIssueCmd(f),
		newInvoiceCheckCmd(f, "issue-status", "/v1/invoices/issue/check/", "Poll an issue submission to Success/Failed", "issue"),
		// No backend route yet (404).
		cmdutil.Stub(&cobra.Command{Use: "download <reference-code>", Short: "Download the invoice PDF/XML", Args: cobra.ExactArgs(1)}),
		cmdutil.Stub(&cobra.Command{Use: "delete <reference-code>", Aliases: []string{"rm", "del"}, Short: "Delete a draft invoice", Args: cobra.ExactArgs(1)}),
	)
	return cmd
}

// tracking is the async 202 response for create/issue.
type tracking struct {
	TrackingCode string `json:"tracking_code" yaml:"tracking_code"`
	TrackingURL  string `json:"tracking_url,omitempty" yaml:"tracking_url,omitempty"`
	Message      string `json:"message,omitempty" yaml:"message,omitempty"`
}

// checkResult is a create/issue status poll (Pending|Success|Failed).
type checkResult struct {
	ReferenceCode string   `json:"reference_code" yaml:"reference_code"`
	Status        string   `json:"status" yaml:"status"`
	Message       string   `json:"message,omitempty" yaml:"message,omitempty"`
	Invoice       *invoice `json:"invoice,omitempty" yaml:"invoice,omitempty"`
}

func newInvoiceCreateCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "create <payload>",
		Aliases: []string{"new"},
		Short:   "Submit an invoice (async); payload is @file, - for stdin, or inline JSON",
		Long: `Submit an invoice for creation. <payload> is the invoice JSON: @path/to/file.json,
- to read stdin, or an inline JSON string. Prompted for on a terminal; required otherwise.

This is asynchronous: the command prints a tracking code, and
"einvoice invoice create-status <tracking-code>" reports the result.`,
		Example: `  sepay einvoice invoice create @invoice.json
  cat invoice.json | sepay einvoice invoice create -`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg, err := f.InputOrArg(cmdutil.FirstArg(args), "an invoice payload", "Invoice JSON (@file, - for stdin, or inline)")
			if err != nil {
				return err
			}
			body, err := readInvoicePayload(f, arg)
			if err != nil {
				return err
			}
			return runInvoiceSubmit(cmd.Context(), f, "/v1/invoices/create", body, "create", "create-status", false)
		},
	}
}

func newInvoiceIssueCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:   "issue <reference-code>",
		Short: "Issue a drafted invoice (async); returns a tracking code",
		Long: `Issue a drafted invoice to the tax authority. This is asynchronous: the command
submits it and prints a tracking code; "einvoice invoice issue-status" reports the result.

<reference-code> is the REF column of "sepay einvoice invoice list". Prompted for on a terminal; required otherwise.`,
		Example: `  sepay einvoice invoice issue <reference-code>
  # then poll the tracking code it prints
  sepay einvoice invoice issue-status <tracking-code>`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := f.InputOrArg(cmdutil.FirstArg(args), "an invoice reference code", "Invoice reference code")
			if err != nil {
				return err
			}
			return runInvoiceSubmit(cmd.Context(), f, "/v1/invoices/issue", map[string]any{"reference_code": ref}, "issue", "issue-status", true)
		},
	}, map[string]string{
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnConfirms:    "always",
		cmdutil.AnnGuidance:    "Issuing sends the invoice to the tax authority. An issued invoice cannot be un-issued; it can only be replaced or adjusted.",
	})
}

// runInvoiceSubmit posts an async create/issue and prints the tracking code.
// irreversible makes the confirm apply in sandbox too (issue goes to the tax authority).
func runInvoiceSubmit(ctx context.Context, f *cmdutil.Factory, path string, body any, action, pollVerb string, irreversible bool) error {
	if err := f.ConfirmMutation(action+" einvoice", irreversible); err != nil {
		return err
	}
	client, err := f.NewEInvoiceClient()
	if err != nil {
		return err
	}
	resp, err := client.Post(ctx, path, body)
	if err != nil {
		return err
	}
	tr, err := iei.DataInto[tracking](resp)
	if err != nil {
		return err
	}
	return f.RenderOutput(tr, func() error {
		f.Printf("%s submitted (tracking %s)\n", f.SuccessIcon(), tr.TrackingCode)
		f.ErrPrintf("poll: sepay einvoice invoice %s %s\n", pollVerb, tr.TrackingCode)
		return nil
	})
}

func newInvoiceCheckCmd(f *cmdutil.Factory, use, pathPrefix, short, submitVerb string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <tracking-code>",
		Short: short,
		Long: "Report the status of a submission from \"einvoice invoice " + submitVerb + "\", with the reason when it failed.\n\n" +
			"<tracking-code> is the code \"einvoice invoice " + submitVerb + "\" prints. Prompted for on a terminal; required otherwise.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := f.InputOrArg(cmdutil.FirstArg(args), "a tracking code", "Tracking / reference code")
			if err != nil {
				return err
			}
			refSeg, err := cmdutil.PathSegment("tracking code", ref)
			if err != nil {
				return err
			}
			return runInvoiceCheck(cmd.Context(), f, pathPrefix+refSeg)
		},
	}
}

// runInvoiceCheck polls a create/issue status once.
func runInvoiceCheck(ctx context.Context, f *cmdutil.Factory, path string) error {
	client, err := f.NewEInvoiceClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, path, nil)
	if err != nil {
		return err
	}
	r, err := iei.DataInto[checkResult](resp)
	if err != nil {
		return err
	}
	failed := strings.EqualFold(r.Status, "Failed")
	if err := f.RenderOutput(r, func() error {
		f.Printf("Ref:     %s\nStatus:  %s\n", r.ReferenceCode, r.Status)
		// Only a Failed message carries information (the provider's reason).
		if failed && r.Message != "" {
			f.Printf("Reason:  %s\n", r.Message)
		}
		if r.Invoice != nil && r.Invoice.InvoiceNumber != "" {
			f.Printf("Number:  %s\nTotal:   %s\n",
				r.Invoice.InvoiceNumber, jsonfmt.FormatMoney(float64(r.Invoice.TotalAmount)))
		}
		return nil
	}); err != nil {
		return err
	}
	// Failed exits non-zero so scripts can detect it.
	if failed {
		return cmdutil.ErrSilent
	}
	return nil
}

// readInvoicePayload reads the create payload from @file, - or inline JSON and requires a boolean is_draft.
// Bytes are returned verbatim so large numeric IDs survive.
func readInvoicePayload(f *cmdutil.Factory, arg string) (json.RawMessage, error) {
	var raw []byte
	var err error
	switch {
	case arg == "-":
		raw, err = io.ReadAll(f.IOStreams.In)
	case strings.HasPrefix(arg, "@"):
		raw, err = os.ReadFile(filepath.Clean(arg[1:]))
	default:
		raw = []byte(arg)
	}
	if err != nil {
		return nil, fmt.Errorf("could not read payload: %w", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, &cmdutil.FlagError{Err: fmt.Errorf("invalid JSON payload: %w", err)}
	}
	v, ok := probe["is_draft"]
	if !ok {
		return nil, &cmdutil.FlagError{Err: fmt.Errorf("payload must include is_draft (true or false)")}
	}
	if _, isBool := v.(bool); !isBool {
		return nil, &cmdutil.FlagError{Err: fmt.Errorf("is_draft must be a boolean (true or false)")}
	}
	return json.RawMessage(raw), nil
}

// invoiceSources are the only `source` values the server filters on; anything else returns an unfiltered page.
var invoiceSources = []string{"manual", "api"}

func newInvoiceListCmd(f *cmdutil.Factory) *cobra.Command {
	var source string
	var page, perPage int
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List invoices",
		Long: `List invoices with their reference code, number, status and total. --source
separates invoices made in the portal (manual) from those made through the API.

The REF column is the <reference-code> "einvoice invoice get" and "issue" take.`,
		Example: `  sepay einvoice invoice list --source api
  sepay einvoice invoice list --page 2 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.ValidateChoice(cmd, "source", invoiceSources); err != nil {
				return err
			}
			query := url.Values{}
			if source != "" {
				query.Set("source", source)
			}
			if page > 0 {
				query.Set("page", strconv.Itoa(page))
			}
			if perPage > 0 {
				query.Set("per_page", strconv.Itoa(perPage))
			}
			return runInvoiceList(cmd.Context(), f, query)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&source, "source", "", "Filter by source: "+strings.Join(invoiceSources, "|"))
	fl.IntVar(&page, "page", 0, "Page number")
	fl.IntVar(&perPage, "per-page", 0, "Rows per page (max 100)")
	return cmd
}

func runInvoiceList(ctx context.Context, f *cmdutil.Factory, query url.Values) error {
	client, err := f.NewEInvoiceClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/invoices", query)
	if err != nil {
		return err
	}
	invoices, paging, err := iei.ListInto[invoice](resp)
	if err != nil {
		return err
	}

	return f.RenderOutput(invoices, func() error {
		if len(invoices) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No invoices found.")
			return nil
		}
		t := jsonfmt.NewTable("REF", "NUMBER", "STATUS", "ISSUED", "TOTAL")
		for _, in := range invoices {
			t.AddRow(in.ReferenceCode, in.InvoiceNumber, in.Status, in.IssuedDate, jsonfmt.FormatMoney(float64(in.TotalAmount)))
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		printPaging(f, paging, len(invoices))
		return nil
	})
}

func newInvoiceGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get <reference-code>",
		Short: "Show an invoice and its line items",
		Long: `Show an invoice and its line items.

<reference-code> is the REF column of "sepay einvoice invoice list". Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := f.InputOrArg(cmdutil.FirstArg(args), "an invoice reference code", "Invoice reference code")
			if err != nil {
				return err
			}
			return runInvoiceGet(cmd.Context(), f, ref)
		},
	}
}

func runInvoiceGet(ctx context.Context, f *cmdutil.Factory, ref string) error {
	refSeg, err := cmdutil.PathSegment("reference code", ref)
	if err != nil {
		return err
	}
	client, err := f.NewEInvoiceClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/invoices/"+refSeg, nil)
	if err != nil {
		return err
	}
	in, err := iei.DataInto[invoice](resp)
	if err != nil {
		return err
	}

	return f.RenderOutput(in, func() error {
		out := f.IOStreams.Out
		f.Printf("Ref:      %s\nNumber:   %s\nSeries:   %s\nStatus:   %s\nIssued:   %s\nPayment:  %s\n",
			in.ReferenceCode, jsonfmt.OrDash(in.InvoiceNumber), jsonfmt.OrDash(in.InvoiceSeries),
			in.Status, in.IssuedDate, jsonfmt.OrDash(in.PaymentMethod))
		if in.Buyer != nil {
			f.Printf("Buyer:    %s", in.Buyer.Name)
			if in.Buyer.TaxCode != "" {
				f.Printf(" (MST %s)", in.Buyer.TaxCode)
			}
			fmt.Fprintln(out)
		}
		f.Printf("Subtotal: %s\nTax:      %s\nTotal:    %s\n",
			jsonfmt.FormatMoney(float64(in.TotalBeforeTax)), jsonfmt.FormatMoney(float64(in.TaxAmount)), jsonfmt.FormatMoney(float64(in.TotalAmount)))
		if in.PdfURL != nil && *in.PdfURL != "" {
			f.Printf("PDF:      %s\n", *in.PdfURL)
		}
		if in.XMLURL != nil && *in.XMLURL != "" {
			f.Printf("XML:      %s\n", *in.XMLURL)
		}
		if len(in.Items) == 0 {
			return nil
		}
		fmt.Fprintln(out)
		t := jsonfmt.NewTable("#", "ITEM", "QTY", "UNIT PRICE", "TAX%", "AMOUNT").Truncatable("ITEM")
		for _, it := range in.Items {
			t.AddRow(strconv.Itoa(int(it.LineNumber)), it.ItemName, it.Quantity.String(),
				jsonfmt.FormatMoney(float64(it.UnitPrice)), it.TaxRate.String(), jsonfmt.FormatMoney(float64(it.TotalAmount)))
		}
		return f.RenderTable(t)
	})
}
